package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/beacon"
	"norn/v2/api/connector"
	"norn/v2/api/consul"
	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/redpanda"
	containerruntime "norn/v2/api/runtime"
	"norn/v2/api/saga"
	"norn/v2/api/secrets"
	"norn/v2/api/storage"
	"norn/v2/api/store"
)

type Pipeline struct {
	DB                             *store.DB
	Nomad                          *nomad.Client
	Consul                         *consul.Client
	Workloads                      connector.Connector
	ContainerRuntime               *containerruntime.Runtime
	WS                             *hub.Hub
	SagaStore                      saga.Store
	Secrets                        *secrets.Manager
	AppsDir                        string
	GitToken                       string
	GitSSHKey                      string
	RegistryURL                    string
	NetworkMode                    string
	IngressURL                     string
	ExternalIngress                bool
	Production                     bool
	StrictSecrets                  bool
	Beacon                         *beacon.Service
	Storage                        *storage.Client
	Redpanda                       *redpanda.Client
	VerifyArtifact                 func(context.Context, string) error
	VerifySignature                func(context.Context, string) error
	ScanArtifact                   func(context.Context, string) error
	ArtifactSigningPublicKey       string
	ArtifactDenySeverities         []string
	CosignPath                     string
	TrivyPath                      string
	ReleaseAdmissionMode           string
	ReleaseEnvironment             string
	ReleaseAttestationIssuer       string
	ReleaseAttestationTrustMode    string
	ReleaseRegistryAuthFile        string
	ReleaseRegistryNodePullReady   bool
	ReleaseAttestationRepositories []string
	ReleaseAttestationWorkflowRefs []string
	ReleaseRequireSBOM             bool
	// TrustedQualificationSigningKeys are the current staging public keys used
	// to re-authenticate a durable promotion receipt immediately before a
	// production rollback mutates a workload.
	TrustedQualificationSigningKeys []string
	// VerifyKeylessAttestations permits the private GitHub verifier to be
	// injected without coupling pipeline to its credential implementation.
	VerifyKeylessAttestations        func(context.Context, string, string, string, model.ReleaseCandidate) error
	VerifyPrivateKeylessAttestations func(context.Context, string, string, string, model.ReleaseCandidate) error
	VerifyNornPrivateAttestations    func(context.Context, string, string, string, model.ReleaseCandidate) error
	RunArtifactCommand               func(context.Context, string, ...string) ([]byte, error)
}

func (p *Pipeline) workloadConnector() connector.Connector {
	if p.Workloads != nil {
		return p.Workloads
	}
	// Compatibility for tests and embedded callers that still construct the
	// pipeline directly. The server always injects an explicit connector.
	if p.Nomad != nil {
		return connector.NewNomadConsul(p.Nomad, p.Consul)
	}
	return nil
}

// LegacyDeploymentAllowed is the queue-boundary guard for mutable legacy
// deployment entry points. Production releases use signed promotion operations.
func (p *Pipeline) LegacyDeploymentAllowed() bool {
	return p == nil || !p.productionReleaseLane()
}

func (p *Pipeline) productionReleaseLane() bool {
	return p != nil && strings.EqualFold(strings.TrimSpace(p.ReleaseEnvironment), "production")
}

type state struct {
	spec          *model.InfraSpec
	workDir       string
	commitSHA     string
	imageTag      string
	sourceKind    string
	sourcePath    string
	sourceDirty   bool
	sourceChanges []string
	sourceRef     string
	preflight     bool
	deploymentID  string
	regionEvals   map[string]string
	artifactBound bool
	candidate     model.ReleaseCandidate
}

type step struct {
	name string
	fn   func(ctx context.Context, s *state, sg *saga.Saga) error
}

// Run executes the full deploy pipeline for an app.
// Returns the saga ID for event tracking.
func (p *Pipeline) Run(spec *model.InfraSpec, ref string) string {
	if !p.LegacyDeploymentAllowed() {
		log.Printf("pipeline: legacy deployment refused for %s in production release lane", spec.App)
		return ""
	}
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "deploy")
	ctx := context.Background()

	deploy := &model.Deployment{
		ID:        uuid.New().String(),
		App:       spec.App,
		CommitSHA: ref,
		SagaID:    sg.ID,
		Status:    model.StatusQueued,
		SourceRef: ref,
		StartedAt: time.Now(),
	}
	if err := p.DB.InsertDeployment(ctx, deploy); err != nil {
		log.Printf("pipeline: insert deployment: %v", err)
	}
	if err := p.DB.InsertDeploymentRegions(ctx, deploy.ID, spec.ResolvedRegions()); err != nil {
		log.Printf("pipeline: insert deployment regions: %v", err)
	}
	operationID := uuid.New().String()
	if err := p.DB.InsertOperation(ctx, &model.Operation{
		ID:          operationID,
		Kind:        "app.deploy",
		App:         spec.App,
		SagaID:      sg.ID,
		Ref:         ref,
		Status:      model.OperationQueued,
		Risk:        "app rolling update",
		Source:      "pipeline",
		Message:     fmt.Sprintf("queued deploy for %s", spec.App),
		StartedAt:   deploy.StartedAt,
		MaxAttempts: 2,
		Payload: map[string]interface{}{
			"deploymentId": deploy.ID,
			"app":          spec.App,
			"ref":          ref,
		},
		Metadata: map[string]interface{}{
			"deploymentId": deploy.ID,
		},
	}); err != nil {
		log.Printf("pipeline: insert operation: %v", err)
	}

	sg.Log(ctx, "deploy.queued", fmt.Sprintf("queued deploy for %s (ref: %s)", spec.App, ref), nil)
	return sg.ID
}

func (p *Pipeline) ExecuteOperation(ctx context.Context, op *model.Operation) error {
	var specs []*model.InfraSpec
	var err error
	if op.Kind == "app.preflight" {
		specs, err = model.DiscoverAllApps(p.AppsDir)
	} else {
		specs, err = model.DiscoverApps(p.AppsDir)
	}
	if err != nil {
		return fmt.Errorf("discover apps: %w", err)
	}
	var spec *model.InfraSpec
	for _, candidate := range specs {
		if candidate.App == op.App {
			spec = candidate
			break
		}
	}
	if spec == nil {
		return fmt.Errorf("app %s not found", op.App)
	}

	category := "deploy"
	if op.Kind == "app.preflight" {
		category = "preflight"
	} else if op.Kind == "app.migrate" {
		category = "migration"
	} else if strings.HasPrefix(op.Kind, "app.snapshot") {
		category = "snapshot"
	}
	sg := saga.NewWithID(p.SagaStore, op.SagaID, spec.App, "pipeline", category)

	switch op.Kind {
	case "app.snapshot", "app.snapshot-prune", "app.snapshot-restore", "app.migrate":
		return p.executeDataOperation(ctx, op, spec, sg)
	case "app.deploy":
		candidate, err := releaseOperationCandidate(op)
		if err != nil {
			return fmt.Errorf("operation %s release candidate: %w", op.ID, err)
		}
		deploymentID := stringFromMap(op.Payload, "deploymentId")
		if deploymentID == "" {
			deploymentID = stringFromMap(op.Metadata, "deploymentId")
		}
		if deploymentID == "" {
			return fmt.Errorf("operation %s missing deployment id", op.ID)
		}
		deploy, err := p.DB.GetDeployment(ctx, deploymentID)
		if err != nil {
			return fmt.Errorf("load deployment %s: %w", deploymentID, err)
		}
		sg.Log(ctx, "deploy.start", fmt.Sprintf("deploying %s (ref: %s)", spec.App, op.Ref), map[string]string{
			"operationId":  op.ID,
			"deploymentId": deploymentID,
			"attempt":      strconv.Itoa(op.Attempts),
		})
		p.run(ctx, spec, deploy, sg, op.ID, op.Attempts, candidate)
		return nil
	case "app.rollback":
		deploymentID := stringFromMap(op.Payload, "deploymentId")
		if deploymentID == "" {
			deploymentID = stringFromMap(op.Metadata, "deploymentId")
		}
		if deploymentID == "" {
			return fmt.Errorf("operation %s missing deployment id", op.ID)
		}
		imageTag := stringFromMap(op.Payload, "imageTag")
		if imageTag == "" {
			imageTag = stringFromMap(op.Metadata, "imageTag")
		}
		if imageTag == "" {
			return fmt.Errorf("operation %s missing rollback image tag", op.ID)
		}
		deploy, err := p.DB.GetDeployment(ctx, deploymentID)
		if err != nil {
			return fmt.Errorf("load deployment %s: %w", deploymentID, err)
		}
		sg.Log(ctx, "rollback.start", fmt.Sprintf("rolling back %s to %s", spec.App, imageTag), map[string]string{
			"operationId":  op.ID,
			"deploymentId": deploymentID,
			"attempt":      strconv.Itoa(op.Attempts),
		})
		p.runRollback(ctx, spec, deploy, sg, imageTag, op.ID, op.Attempts, stringSliceFromMap(op.Payload, "regions"))
		return nil
	case "app.preflight":
		sg.Log(ctx, "preflight.start", fmt.Sprintf("preflighting %s (ref: %s)", spec.App, op.Ref), map[string]string{
			"operationId": op.ID,
			"attempt":     strconv.Itoa(op.Attempts),
		})
		candidate, err := releaseOperationCandidate(op)
		if err != nil {
			return fmt.Errorf("operation %s release candidate: %w", op.ID, err)
		}
		p.runPreflightWithArtifact(ctx, spec, op.Ref, stringFromMap(op.Payload, "artifact"), candidate, sg, op.ID)
		return nil
	default:
		return fmt.Errorf("unsupported operation kind %s", op.Kind)
	}
}

// releaseOperationCandidate distinguishes a real legacy operation from a
// release-control-api operation. Release operations are never allowed to fall
// back to legacy behavior: malformed or absent evidence must fail before any
// worker stage can skip the source/artifact binding.
func releaseOperationCandidate(op *model.Operation) (model.ReleaseCandidate, error) {
	if op == nil || op.Source != "release-control-api" {
		return model.ReleaseCandidate{}, nil
	}
	if op.Metadata == nil {
		return model.ReleaseCandidate{}, fmt.Errorf("missing release candidate")
	}
	raw, found := op.Metadata["candidate"]
	if !found || raw == nil {
		return model.ReleaseCandidate{}, fmt.Errorf("missing release candidate")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return model.ReleaseCandidate{}, fmt.Errorf("encode release candidate: %w", err)
	}
	var candidate model.ReleaseCandidate
	if err := json.Unmarshal(encoded, &candidate); err != nil || !completeQueuedReleaseCandidate(candidate) {
		return model.ReleaseCandidate{}, fmt.Errorf("malformed release candidate")
	}
	return candidate, nil
}

func completeQueuedReleaseCandidate(candidate model.ReleaseCandidate) bool {
	visibility := candidate.RepositoryVisibility
	return candidate.Provider == "github-actions" && candidate.Repository != "" && candidate.RepositoryID != "" && candidate.OwnerID != "" && (visibility == "public" || visibility == "private" || visibility == "internal") && candidate.RunID != "" && candidate.RunAttempt != "" && candidate.WorkflowRef != "" && candidate.WorkflowSHA != "" && candidate.SignerWorkflowRef != "" && candidate.SignerWorkflowSHA != "" && candidate.Ref != "" && candidate.Attestation.Mode != "" && candidate.Attestation.Verifier != "" && candidate.Attestation.Issuer != "" && candidate.Attestation.SubjectDigest != "" && candidate.Attestation.MaterialSHA != ""
}

func stringFromMap(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func stringSliceFromMap(values map[string]interface{}, key string) []string {
	if values == nil {
		return nil
	}
	switch raw := values[key].(type) {
	case []string:
		return raw
	case []interface{}:
		out := make([]string, 0, len(raw))
		for _, value := range raw {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func (p *Pipeline) run(ctx context.Context, spec *model.InfraSpec, deploy *model.Deployment, sg *saga.Saga, operationID string, attempt int, candidate model.ReleaseCandidate) {
	st := &state{
		spec:          spec,
		commitSHA:     deploy.CommitSHA,
		sourceRef:     deploy.CommitSHA,
		deploymentID:  deploy.ID,
		regionEvals:   make(map[string]string),
		imageTag:      deploy.ImageTag,
		artifactBound: deploy.ImageTag != "",
		candidate:     candidate,
	}

	steps := []step{
		{name: "release-binding", fn: p.releaseBindingAdmission},
		{name: "clone", fn: p.clone},
		{name: "admission", fn: p.admission},
		{name: "build", fn: p.build},
		{name: "artifact-admission", fn: p.artifactAdmission},
		{name: "test", fn: p.test},
		{name: "snapshot", fn: p.snapshot},
		{name: "migrate", fn: p.migrate},
		{name: "submit", fn: p.submit},
		{name: "healthy", fn: p.healthy},
		{name: "forge", fn: p.forge},
		{name: "cleanup", fn: p.cleanup},
	}

	// Insert canary step between healthy and forge when any process has canary config
	if hasCanaryConfig(spec) {
		var withCanary []step
		for _, s := range steps {
			withCanary = append(withCanary, s)
			if s.name == "healthy" {
				withCanary = append(withCanary, step{name: "canary", fn: p.canary})
			}
		}
		steps = withCanary
	}

	total := fmt.Sprintf("%d", len(steps))
	for i, s := range steps {
		idx := fmt.Sprintf("%d", i+1)
		sg.StepStart(ctx, s.name)
		p.recordDeploymentStepStart(ctx, deploy, sg, s.name, operationID, attempt)
		p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
			"step":   s.name,
			"sagaId": sg.ID,
			"status": "running",
			"index":  idx,
			"total":  total,
		}})

		start := time.Now()
		err := s.fn(ctx, st, sg)
		elapsed := time.Since(start).Milliseconds()

		if err != nil {
			sg.StepFailed(ctx, s.name, err)
			p.recordDeploymentStepFinish(ctx, deploy.ID, s.name, model.DeploymentStepFailed, elapsed, err.Error(), map[string]interface{}{
				"operationId": operationID,
			})
			p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
				"step":       s.name,
				"sagaId":     sg.ID,
				"status":     "failed",
				"index":      idx,
				"total":      total,
				"durationMs": fmt.Sprintf("%d", elapsed),
			}})
			deploy.Status = model.StatusFailed
			_ = p.DB.UpdateDeployment(ctx, deploy.ID, deploy.Status)
			_ = p.DB.FailIncompleteDeploymentRegions(ctx, deploy.ID, fmt.Sprintf("deploy failed at %s: %v", s.name, err))
			if operationID != "" {
				_ = p.DB.FinishOperation(ctx, operationID, model.OperationFailed, fmt.Sprintf("deploy failed at %s: %v", s.name, err), map[string]interface{}{
					"deploymentId": deploy.ID,
					"step":         s.name,
				})
			}
			sg.Log(ctx, "deploy.failed", fmt.Sprintf("deploy failed at %s: %v", s.name, err), nil)
			p.WS.Broadcast(hub.Event{Type: "deploy.failed", AppID: spec.App, Payload: map[string]string{
				"sagaId": sg.ID,
				"error":  err.Error(),
			}})
			p.emitBeacon(ctx, model.BeaconEvent{
				App:       spec.App,
				Type:      "deploy.failed",
				Severity:  model.BeaconCritical,
				Title:     fmt.Sprintf("%s deploy failed", spec.App),
				Body:      fmt.Sprintf("Deploy failed at %s: %v", s.name, err),
				DedupeKey: fmt.Sprintf("%s:deploy", spec.App),
				Metadata: map[string]interface{}{
					"deploymentId":   deploy.ID,
					"sagaId":         sg.ID,
					"commitSha":      st.commitSHA,
					"imageTag":       st.imageTag,
					"step":           s.name,
					"correlationKey": fmt.Sprintf("%s:deploy", spec.App),
				},
			})

			// Auto-rollback: only when the healthy step fails and policy allows
			if s.name == "healthy" && spec.AutoRollbackEnabled() {
				prev, prevErr := p.autoRollbackTarget(ctx, deploy)
				if prevErr == nil && prev != nil {
					sg.Log(ctx, "deploy.auto_rollback.start", fmt.Sprintf("auto-rollback %s to %s", spec.App, prev.ImageTag), map[string]string{
						"previousDeploymentId": prev.ID,
						"imageTag":             prev.ImageTag,
					})
					p.Rollback(spec, *deploy, prev)
					p.emitBeacon(ctx, model.BeaconEvent{
						App:       spec.App,
						Type:      "deploy.auto_rollback",
						Severity:  model.BeaconWarning,
						Title:     fmt.Sprintf("%s auto-rollback triggered", spec.App),
						Body:      fmt.Sprintf("Deploy failed at healthy check; auto-rolling back to %s.", prev.ImageTag),
						DedupeKey: fmt.Sprintf("%s:auto_rollback", spec.App),
						Metadata: map[string]interface{}{
							"deploymentId":         deploy.ID,
							"sagaId":               sg.ID,
							"previousDeploymentId": prev.ID,
							"imageTag":             prev.ImageTag,
							"correlationKey":       fmt.Sprintf("%s:deploy", spec.App),
						},
					})
				}
			}
			return
		}

		sg.StepComplete(ctx, s.name, elapsed)
		p.recordDeploymentStepFinish(ctx, deploy.ID, s.name, model.DeploymentStepComplete, elapsed, "", map[string]interface{}{
			"operationId": operationID,
		})
		p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
			"step":       s.name,
			"sagaId":     sg.ID,
			"status":     "complete",
			"index":      idx,
			"total":      total,
			"durationMs": fmt.Sprintf("%d", elapsed),
		}})
	}

	deploy.CommitSHA = st.commitSHA
	deploy.ImageTag = st.imageTag
	deploy.SourceKind = st.sourceKind
	deploy.SourceRef = st.sourceRef
	deploy.SourceDirty = st.sourceDirty
	deploy.SourceChanges = st.sourceChanges
	deploy.Status = model.StatusDeployed
	p.DB.UpdateDeploymentResult(ctx, deploy)
	for _, region := range spec.ResolvedRegions() {
		_ = p.DB.UpdateDeploymentRegion(ctx, deploy.ID, region.Name, model.StatusDeployed, st.regionEvals[region.Name], "", region.TrafficWeight)
	}
	if operationID != "" {
		_ = p.DB.FinishOperation(ctx, operationID, model.OperationSucceeded, fmt.Sprintf("deploy complete: %s", spec.App), map[string]interface{}{
			"deploymentId": deploy.ID,
			"commitSha":    st.commitSHA,
			"imageTag":     st.imageTag,
		})
	}
	sg.Log(ctx, "deploy.complete", fmt.Sprintf("deploy complete: %s → %s", spec.App, st.imageTag), map[string]string{
		"commitSha":  st.commitSHA,
		"imageTag":   st.imageTag,
		"sourceKind": st.sourceKind,
		"sourceRef":  st.sourceRef,
	})
	p.WS.Broadcast(hub.Event{Type: "deploy.completed", AppID: spec.App, Payload: map[string]string{
		"sagaId":   sg.ID,
		"imageTag": st.imageTag,
	}})
	p.emitBeacon(ctx, model.BeaconEvent{
		App:       spec.App,
		Type:      "deploy.succeeded",
		Severity:  model.BeaconInfo,
		Title:     fmt.Sprintf("%s deploy succeeded", spec.App),
		Body:      fmt.Sprintf("Deployment %s completed successfully.", deploy.ID),
		DedupeKey: fmt.Sprintf("%s:deploy", spec.App),
		Metadata: map[string]interface{}{
			"deploymentId":   deploy.ID,
			"sagaId":         sg.ID,
			"commitSha":      st.commitSHA,
			"imageTag":       st.imageTag,
			"sourceKind":     st.sourceKind,
			"sourceRef":      st.sourceRef,
			"correlationKey": fmt.Sprintf("%s:deploy", spec.App),
		},
	})
}

// autoRollbackTarget keeps an automatic production recovery inside its release
// lane and excludes legacy deployments. Production rollback targets must have
// been completed by a promotion carrying durable signed-qualification evidence.
func (p *Pipeline) autoRollbackTarget(ctx context.Context, current *model.Deployment) (*model.Deployment, error) {
	if p == nil {
		return nil, fmt.Errorf("pipeline is required for automatic rollback")
	}
	if current == nil {
		return nil, fmt.Errorf("current deployment is required for automatic rollback")
	}
	if p.productionReleaseLane() {
		if current.Environment != "production" {
			return nil, fmt.Errorf("production automatic rollback requires a production deployment")
		}
		if p.DB == nil {
			return nil, fmt.Errorf("deployment store is required for automatic rollback")
		}
		return p.DB.LatestSuccessfulPromotedDeployment(ctx, current.App, current.Environment, current.ID)
	}
	if p.DB == nil {
		return nil, fmt.Errorf("deployment store is required for automatic rollback")
	}
	return p.DB.LastSuccessfulDeployment(ctx, current.App, current.ID)
}

func (p *Pipeline) recordDeploymentStepStart(ctx context.Context, deploy *model.Deployment, sg *saga.Saga, stepName, operationID string, attempt int) {
	if p.DB == nil || deploy == nil {
		return
	}
	_ = p.DB.StartDeploymentStep(ctx, model.DeploymentStep{
		DeploymentID: deploy.ID,
		App:          deploy.App,
		SagaID:       sg.ID,
		Step:         stepName,
		Kind:         model.StepKind(stepName),
		Status:       model.DeploymentStepRunning,
		Attempt:      attempt,
		Message:      "step started",
		Metadata: map[string]interface{}{
			"operationId": operationID,
		},
	})
}

func (p *Pipeline) recordDeploymentStepFinish(ctx context.Context, deploymentID, stepName string, status model.DeploymentStepStatus, durationMs int64, message string, metadata map[string]interface{}) {
	if p.DB == nil || deploymentID == "" {
		return
	}
	_ = p.DB.FinishDeploymentStep(ctx, deploymentID, stepName, status, durationMs, message, metadata)
}

func (p *Pipeline) emitBeacon(ctx context.Context, event model.BeaconEvent) {
	if p.Beacon == nil {
		return
	}
	if _, err := p.Beacon.Emit(ctx, event); err != nil {
		log.Printf("pipeline: beacon emit %s/%s: %v", event.App, event.Type, err)
	}
}

// hasCanaryConfig returns true if any process in the spec has a canary configuration.
func hasCanaryConfig(spec *model.InfraSpec) bool {
	for _, proc := range spec.Processes {
		if proc.Canary != nil && proc.Canary.Count > 0 {
			return true
		}
	}
	return false
}
