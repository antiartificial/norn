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
	"norn/v2/api/consul"
	"norn/v2/api/effect"
	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/redpanda"
	"norn/v2/api/saga"
	"norn/v2/api/secrets"
	"norn/v2/api/storage"
	"norn/v2/api/store"
)

type Pipeline struct {
	DB             *store.DB
	OperationStore store.OperationStore
	// CheckpointStore persists source/build identity for executions that do not
	// use PostgreSQL. When nil, the PostgreSQL DB remains the legacy store.
	CheckpointStore                store.OperationCheckpointStore
	Nomad                          *nomad.Client
	Consul                         *consul.Client
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
	ReleaseAttestationIssuer       string
	ReleaseAttestationRepositories []string
	ReleaseAttestationWorkflowRefs []string
	ReleaseRequireSBOM             bool
	VerifyKeylessAttestations      func(context.Context, string, string, string, model.ReleaseCandidate) error
	// RunArtifactCommand is an injectable command boundary for admission tests.
	// Production uses exec.CommandContext through runArtifactCommand.
	RunArtifactCommand func(context.Context, string, ...string) ([]byte, error)
	// StartDeploymentStep is an injectable persistence boundary for tests.
	// Production uses DB.StartDeploymentStep.
	StartDeploymentStep func(context.Context, model.DeploymentStep) error
	// BuildTestEffects runs build.test through the fenced effect executor.
	// When nil, the legacy unfenced direct execution is used; startup selects
	// this explicitly and never falls back from supervised mode.
	BuildTestEffects *BuildTestEffects
	SnapshotEffects  *SnapshotEffects
	// ScaleEffects fences Nomad's non-idempotent scale endpoint behind a
	// durable external-effect reservation. It is required for app.scale.
	ScaleEffects *NomadScaleEffects
	// RestartEffects records the exact allocations selected for a replacement
	// before asking Nomad to stop any of them. It is required for app.restart.
	RestartEffects *NomadRestartEffects
	// CronPauseEffects fences periodic-job deregistration and its durable state
	// transition. It is required before accepting app.cron-pause.
	CronPauseEffects  *NomadCronPauseEffects
	CronResumeEffects *NomadCronResumeEffects
	// RestartAvailability is a test-only admission seam. Production leaves it
	// nil and requires RestartEffects.
	RestartAvailability func() bool
	// CanaryPromotionEffects fences Nomad deployment promotion behind a durable
	// effect. It is required before accepting app.canary-promote.
	CanaryPromotionEffects *NomadCanaryPromotionEffects
	// FinishScaleIntent is the claim-fenced atomic desired-replica and terminal
	// operation write. Tests may inject a transient failure; production uses DB.
	FinishScaleIntent func(context.Context, store.OperationClaim, string, string, string, int, string, map[string]interface{}) error
	// FinishCronPauseIntent atomically records paused cron state and terminalizes
	// the claimed operation after Nomad has verified the periodic job stopped.
	FinishCronPauseIntent  func(context.Context, store.OperationClaim, string, string, string, string, map[string]interface{}) error
	FinishCronResumeIntent func(context.Context, store.OperationClaim, string, string, string, string, map[string]interface{}) error
	// DatabaseTargets binds database-consuming operations to catalog
	// targets. When nil (no NORN_DATABASE_PROFILE), v2 routing is unchanged.
	DatabaseTargets *DatabaseTargets
	// RunBuildCommand is an injectable boundary for Docker build/push.
	// Production uses exec.CommandContext through runBuildCommand.
	RunBuildCommand func(context.Context, string, ...string) ([]byte, error)
}

type state struct {
	spec          *model.InfraSpec
	workDir       string
	commitSHA     string
	imageTag      string
	artifactBound bool
	sourceKind    string
	sourcePath    string
	sourceDirty   bool
	sourceChanges []string
	sourceRef     string
	preflight     bool
	deploymentID  string
	regionEvals   map[string]string
	candidate     model.ReleaseCandidate
	// claim authorizes external effects started by this execution.
	claim store.OperationClaim
	// sourceIdentity is the digest of the operation's recorded source
	// checkpoint; it is identical on every claim of the operation.
	sourceIdentity string
	// operationPayload carries the accepted payload, including any recorded
	// database target(s); database holds the opened targets (nil in legacy
	// mode).
	operationPayload map[string]interface{}
	database         *boundDatabases
	databaseOpened   bool
	// deliveryRevision is the catalog revision whose staged database
	// delivery this deploy's jobs read (0 when nothing is delivered).
	deliveryRevision int64
}

// OperationResult is a claimed pipeline's proposed terminal record. Workers
// persist it through the claim CAS before Publish exposes terminal events.
type OperationResult struct {
	Claim    store.OperationClaim
	Status   model.OperationStatus
	Message  string
	Metadata map[string]interface{}
	publish  func(context.Context)
	// deferred is an unresolved external effect. The operation is neither
	// failed nor published; ExecuteOperation returns it so the worker defers
	// the claim and a later claim recovers the same execution.
	deferred error
	// finished means the terminal record was already committed atomically
	// with the operation's effect; the worker only publishes.
	finished bool
}

// Finished reports that the operation's terminal record is already durable.
func (r *OperationResult) Finished() bool { return r != nil && r.finished }

func deferredResult(claim store.OperationClaim, err error) *OperationResult {
	return &OperationResult{Claim: claim, deferred: err}
}

func operationOutcome(result *OperationResult) (*OperationResult, error) {
	if result != nil && result.deferred != nil {
		return nil, result.deferred
	}
	return result, nil
}

func (r *OperationResult) Publish(ctx context.Context) {
	if r != nil && r.publish != nil {
		r.publish(ctx)
	}
}

// QueueReleaseDeployment records an immutable-source deployment request using
// the normal app.deploy worker and saga pipeline. The caller supplies a full
// source SHA and may bind an already-published OCI digest; the latter skips
// rebuilding so the exact staged artifact can be promoted.
func (p *Pipeline) QueueReleaseDeployment(ctx context.Context, spec *model.InfraSpec, sourceSHA, artifact, environment string, metadata map[string]interface{}, request EnqueueRequest) (store.AcceptedOperation, error) {
	if p == nil || p.DB == nil || p.SagaStore == nil || spec == nil {
		return store.AcceptedOperation{}, fmt.Errorf("release deployment pipeline is unavailable")
	}
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "deploy")
	now := time.Now().UTC()
	deployment := &model.Deployment{ID: uuid.NewString(), App: spec.App, CommitSHA: sourceSHA, ImageTag: artifact, Environment: environment, SagaID: sg.ID, Status: model.StatusQueued, SourceRef: sourceSHA, StartedAt: now}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadata["deploymentId"] = deployment.ID
	metadata["sourceSha"] = sourceSHA
	metadata["artifact"] = artifact
	metadata["environment"] = environment
	op := &model.Operation{
		ID: uuid.NewString(), Kind: "app.deploy", App: spec.App, SagaID: sg.ID, Ref: sourceSHA,
		Status: model.OperationQueued, Risk: "app rolling update", Source: "release-control-api",
		Message: fmt.Sprintf("queued release deploy for %s", spec.App), StartedAt: now, MaxAttempts: 2,
		Payload: map[string]interface{}{"deploymentId": deployment.ID, "app": spec.App, "sourceSha": sourceSHA, "artifact": artifact, "candidate": metadata["candidate"]}, Metadata: metadata,
	}
	accepted, err := p.acceptOperation(ctx, request, *op, deployment, spec.ResolvedRegions())
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if !accepted.Replayed {
		sg.Log(ctx, "deploy.queued", fmt.Sprintf("queued release deploy for %s (sha: %s)", spec.App, sourceSHA), map[string]string{"operationId": accepted.Operation.ID, "deploymentId": accepted.Intent.DeploymentID, "artifact": artifact})
	}
	return accepted, nil
}

// QueueReleasePreflight creates the existing app.preflight operation with
// explicit release provenance. It remains read-only and therefore has no
// deployment row.
func (p *Pipeline) QueueReleasePreflight(ctx context.Context, spec *model.InfraSpec, sourceSHA, artifact, environment string, metadata map[string]interface{}, request EnqueueRequest) (store.AcceptedOperation, error) {
	if p == nil || spec == nil || (p.DB == nil && p.CheckpointStore == nil) || (p.DB != nil && p.SagaStore == nil) {
		return store.AcceptedOperation{}, fmt.Errorf("release preflight pipeline is unavailable")
	}
	if p.backendNeutralPreflight() {
		if err := p.validateBackendNeutralPreflight(spec); err != nil {
			return store.AcceptedOperation{}, err
		}
	}
	sg := saga.New(p.preflightSagaStore(), spec.App, "pipeline", "preflight")
	now := time.Now().UTC()
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadata["sourceSha"], metadata["artifact"], metadata["environment"] = sourceSHA, artifact, environment
	op := &model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: spec.App, SagaID: sg.ID, Ref: sourceSHA, Status: model.OperationQueued, Risk: "read-only", Source: "release-control-api", Message: fmt.Sprintf("queued release preflight for %s", spec.App), StartedAt: now, MaxAttempts: 3, Payload: map[string]interface{}{"app": spec.App, "sourceSha": sourceSHA, "artifact": artifact, "candidate": metadata["candidate"]}, Metadata: metadata}
	accepted, err := p.acceptOperation(ctx, request, *op, nil, nil)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if !accepted.Replayed {
		sg.Log(ctx, "preflight.queued", fmt.Sprintf("queued release preflight for %s (sha: %s)", spec.App, sourceSHA), map[string]string{"operationId": accepted.Operation.ID, "artifact": artifact})
	}
	return accepted, nil
}

type step struct {
	name string
	fn   func(ctx context.Context, s *state, sg *saga.Saga) error
}

func runClaimedStep(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := fn()
	if err != nil {
		return err
	}
	return ctx.Err()
}

// Run atomically accepts a deploy operation and its deployment/regions. It
// returns the original accepted identity on replay.
func (p *Pipeline) Run(ctx context.Context, spec *model.InfraSpec, ref string, request EnqueueRequest) (store.AcceptedOperation, error) {
	if p == nil || p.DB == nil || p.SagaStore == nil || spec == nil {
		return store.AcceptedOperation{}, fmt.Errorf("deploy pipeline is unavailable")
	}
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "deploy")
	deploy := &model.Deployment{
		ID:        uuid.New().String(),
		App:       spec.App,
		CommitSHA: ref,
		SagaID:    sg.ID,
		Status:    model.StatusQueued,
		SourceRef: ref,
		StartedAt: time.Now(),
	}
	operationID := uuid.New().String()
	operation := model.Operation{
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
	}
	accepted, err := p.acceptOperation(ctx, request, operation, deploy, spec.ResolvedRegions())
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if !accepted.Replayed {
		sg.Log(ctx, "deploy.queued", fmt.Sprintf("queued deploy for %s (ref: %s)", spec.App, ref), map[string]string{"operationId": accepted.Operation.ID, "deploymentId": accepted.Intent.DeploymentID})
	}
	return accepted, nil
}

func (p *Pipeline) ExecuteOperation(ctx context.Context, op *model.Operation, claim store.OperationClaim) (*OperationResult, error) {
	if op == nil || claim.OperationID() != op.ID || claim.OwnerID() == "" || claim.Generation() <= 0 {
		return nil, fmt.Errorf("operation claim does not match execution")
	}
	if op.Kind == CatalogActivationKind {
		return p.executeCatalogActivation(ctx, op, claim)
	}
	if op.Kind == "app.scale" {
		return operationOutcome(p.executeScale(ctx, op, claim))
	}
	if op.Kind == "app.restart" {
		return operationOutcome(p.executeRestart(ctx, op, claim))
	}
	if op.Kind == "app.cron-pause" {
		return operationOutcome(p.executeCronPause(ctx, op, claim))
	}
	if op.Kind == "app.cron-resume" {
		return operationOutcome(p.executeCronResume(ctx, op, claim))
	}
	if op.Kind == "app.canary-promote" {
		return operationOutcome(p.executeCanaryPromotion(ctx, op, claim))
	}
	var specs []*model.InfraSpec
	var err error
	if op.Kind == "app.preflight" {
		specs, err = model.DiscoverAllApps(p.AppsDir)
	} else {
		specs, err = model.DiscoverApps(p.AppsDir)
	}
	if err != nil {
		return nil, fmt.Errorf("discover apps: %w", err)
	}
	var spec *model.InfraSpec
	for _, candidate := range specs {
		if candidate.App == op.App {
			spec = candidate
			break
		}
	}
	if spec == nil {
		return nil, fmt.Errorf("app %s not found", op.App)
	}
	if op.Kind == "app.preflight" && p.backendNeutralPreflight() {
		if err := p.validateBackendNeutralPreflight(spec); err != nil {
			return nil, err
		}
	}

	category := "deploy"
	if op.Kind == "app.preflight" {
		category = "preflight"
	} else if op.Kind == "app.migrate" {
		category = "migration"
	} else if strings.HasPrefix(op.Kind, "app.snapshot") {
		category = "snapshot"
	}
	sagaStore := p.SagaStore
	if op.Kind == "app.preflight" && p.backendNeutralPreflight() {
		sagaStore = saga.DiscardStore{}
	}
	sg := saga.NewWithID(sagaStore, op.SagaID, spec.App, "pipeline", category)

	switch op.Kind {
	case "app.snapshot", "app.snapshot-prune", "app.snapshot-restore", "app.migrate":
		return p.executeDataOperation(ctx, op, claim, spec, sg)
	case DatabaseBaselineKind:
		return p.executeDatabaseBaseline(ctx, op, claim, spec)
	case "app.deploy":
		deploymentID := stringFromMap(op.Payload, "deploymentId")
		if deploymentID == "" {
			deploymentID = stringFromMap(op.Metadata, "deploymentId")
		}
		if deploymentID == "" {
			return nil, fmt.Errorf("operation %s missing deployment id", op.ID)
		}
		deploy, err := p.DB.GetDeployment(ctx, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("load deployment %s: %w", deploymentID, err)
		}
		sg.Log(ctx, "deploy.start", fmt.Sprintf("deploying %s (ref: %s)", spec.App, op.Ref), map[string]string{
			"operationId":  op.ID,
			"deploymentId": deploymentID,
			"attempt":      strconv.Itoa(op.Attempts),
		})
		return operationOutcome(p.run(ctx, spec, deploy, sg, claim, op.Attempts))
	case "app.rollback":
		deploymentID := stringFromMap(op.Payload, "deploymentId")
		if deploymentID == "" {
			deploymentID = stringFromMap(op.Metadata, "deploymentId")
		}
		if deploymentID == "" {
			return nil, fmt.Errorf("operation %s missing deployment id", op.ID)
		}
		imageTag := stringFromMap(op.Payload, "imageTag")
		if imageTag == "" {
			imageTag = stringFromMap(op.Metadata, "imageTag")
		}
		if imageTag == "" {
			return nil, fmt.Errorf("operation %s missing rollback image tag", op.ID)
		}
		deploy, err := p.DB.GetDeployment(ctx, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("load deployment %s: %w", deploymentID, err)
		}
		sg.Log(ctx, "rollback.start", fmt.Sprintf("rolling back %s to %s", spec.App, imageTag), map[string]string{
			"operationId":  op.ID,
			"deploymentId": deploymentID,
			"attempt":      strconv.Itoa(op.Attempts),
		})
		return p.runRollback(ctx, spec, deploy, sg, imageTag, claim, op.Attempts, stringSliceFromMap(op.Payload, "regions")), nil
	case "app.preflight":
		sg.Log(ctx, "preflight.start", fmt.Sprintf("preflighting %s (ref: %s)", spec.App, op.Ref), map[string]string{
			"operationId": op.ID,
			"attempt":     strconv.Itoa(op.Attempts),
		})
		var candidate model.ReleaseCandidate
		encoded, _ := json.Marshal(op.Metadata["candidate"])
		_ = json.Unmarshal(encoded, &candidate)
		return operationOutcome(p.runPreflightWithArtifact(ctx, spec, op.Ref, stringFromMap(op.Payload, "artifact"), candidate, sg, claim))
	default:
		return nil, fmt.Errorf("unsupported operation kind %s", op.Kind)
	}
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

func (p *Pipeline) run(ctx context.Context, spec *model.InfraSpec, deploy *model.Deployment, sg *saga.Saga, claim store.OperationClaim, attempt int) *OperationResult {
	operationID := claim.OperationID()
	var candidate model.ReleaseCandidate
	var payload map[string]interface{}
	if operationID != "" && p.DB != nil {
		if op, err := p.DB.GetOperation(ctx, operationID); err == nil {
			encoded, _ := json.Marshal(op.Metadata["candidate"])
			_ = json.Unmarshal(encoded, &candidate)
			payload = op.Payload
		}
	}
	st := &state{
		spec:             spec,
		commitSHA:        deploy.CommitSHA,
		sourceRef:        deploy.CommitSHA,
		deploymentID:     deploy.ID,
		regionEvals:      make(map[string]string),
		imageTag:         deploy.ImageTag,
		artifactBound:    deploy.ImageTag != "",
		candidate:        candidate,
		claim:            claim,
		operationPayload: payload,
	}
	defer func() { _ = st.database.Close() }()

	steps := []step{
		{name: "clone", fn: p.checkpointedClone},
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
		if err := ctx.Err(); err != nil {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: fmt.Sprintf("deploy canceled before %s: %v", s.name, err), Metadata: map[string]interface{}{"deploymentId": deploy.ID, "step": s.name}}
		}
		idx := fmt.Sprintf("%d", i+1)
		sg.StepStart(ctx, s.name)
		if err := p.recordDeploymentStepStart(ctx, deploy, sg, s.name, operationID, attempt); err != nil {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: fmt.Sprintf("record deploy step %s before execution: %v", s.name, err), Metadata: map[string]interface{}{"deploymentId": deploy.ID, "step": s.name}}
		}
		p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
			"step":   s.name,
			"sagaId": sg.ID,
			"status": "running",
			"index":  idx,
			"total":  total,
		}})

		start := time.Now()
		err := runClaimedStep(ctx, func() error { return s.fn(ctx, st, sg) })
		elapsed := time.Since(start).Milliseconds()

		if err != nil && effect.IsDeferred(err) {
			// The deployment, its regions and the step stay non-terminal; the
			// worker requeues this claim and recovery resumes the same effect.
			// The checkout is kept: the supervised command may still be using it.
			sg.Log(ctx, "deploy.step.pending", fmt.Sprintf("%s awaiting external effect recovery: %v", s.name, err), map[string]string{"step": s.name, "operationId": operationID})
			return deferredResult(claim, err)
		}
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
			stepName, stepErr := s.name, err
			message := fmt.Sprintf("deploy failed at %s: %v", stepName, stepErr)
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: message,
				Metadata: map[string]interface{}{"deploymentId": deploy.ID, "step": stepName},
				publish: func(publishCtx context.Context) {
					sg.Log(publishCtx, "deploy.failed", message, nil)
					p.WS.Broadcast(hub.Event{Type: "deploy.failed", AppID: spec.App, Payload: map[string]string{"sagaId": sg.ID, "error": stepErr.Error()}})
					p.emitBeacon(publishCtx, model.BeaconEvent{App: spec.App, Type: "deploy.failed", Severity: model.BeaconCritical,
						Title: fmt.Sprintf("%s deploy failed", spec.App), Body: fmt.Sprintf("Deploy failed at %s: %v", stepName, stepErr), DedupeKey: fmt.Sprintf("%s:deploy", spec.App),
						Metadata: map[string]interface{}{"deploymentId": deploy.ID, "sagaId": sg.ID, "commitSha": st.commitSHA, "imageTag": st.imageTag, "step": stepName, "correlationKey": fmt.Sprintf("%s:deploy", spec.App)}})
					if stepName == "healthy" && spec.AutoRollbackEnabled() {
						prev, prevErr := p.DB.LastSuccessfulDeployment(publishCtx, deploy.App, deploy.ID)
						if prevErr == nil && prev != nil {
							enqueue, enqueueErr := p.systemEnqueueRequest(publishCtx, "pipeline:auto-rollback", "auto-rollback:"+operationID, "pipeline-auto-rollback", map[string]interface{}{"parentOperationId": operationID, "failedDeploymentId": deploy.ID, "sourceDeploymentId": prev.ID, "imageTag": prev.ImageTag})
							if enqueueErr == nil {
								_, enqueueErr = p.QueueRollback(publishCtx, spec, *deploy, prev, nil, enqueue, map[string]interface{}{"autoRollback": true, "parentOperationId": operationID})
							}
							if enqueueErr != nil {
								sg.Log(publishCtx, "deploy.auto_rollback.error", fmt.Sprintf("auto-rollback acceptance failed: %v", enqueueErr), map[string]string{"previousDeploymentId": prev.ID})
								p.emitBeacon(publishCtx, model.BeaconEvent{App: spec.App, Type: "deploy.auto_rollback_failed", Severity: model.BeaconCritical, Title: fmt.Sprintf("%s auto-rollback could not be accepted", spec.App), Body: enqueueErr.Error(), DedupeKey: fmt.Sprintf("%s:auto_rollback_acceptance", spec.App), Metadata: map[string]interface{}{"deploymentId": deploy.ID, "operationId": operationID, "previousDeploymentId": prev.ID}})
								return
							}
							sg.Log(publishCtx, "deploy.auto_rollback.start", fmt.Sprintf("auto-rollback %s to %s", spec.App, prev.ImageTag), map[string]string{"previousDeploymentId": prev.ID, "imageTag": prev.ImageTag})
							p.emitBeacon(publishCtx, model.BeaconEvent{App: spec.App, Type: "deploy.auto_rollback", Severity: model.BeaconWarning,
								Title: fmt.Sprintf("%s auto-rollback triggered", spec.App), Body: fmt.Sprintf("Deploy failed at healthy check; auto-rolling back to %s.", prev.ImageTag), DedupeKey: fmt.Sprintf("%s:auto_rollback", spec.App),
								Metadata: map[string]interface{}{"deploymentId": deploy.ID, "sagaId": sg.ID, "previousDeploymentId": prev.ID, "imageTag": prev.ImageTag, "correlationKey": fmt.Sprintf("%s:deploy", spec.App)}})
						}
					}
				}}
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
	var digestErr error
	deploy.SpecDigest, digestErr = model.InfraSpecDigest(spec)
	if digestErr != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: fmt.Sprintf("record deployment spec digest: %v", digestErr), Metadata: map[string]interface{}{"deploymentId": deploy.ID}}
	}
	for _, region := range spec.ResolvedRegions() {
		if err := p.DB.UpdateDeploymentRegion(ctx, deploy.ID, region.Name, model.StatusDeployed, st.regionEvals[region.Name], "", region.TrafficWeight); err != nil {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: fmt.Sprintf("record deployment region: %v", err), Metadata: map[string]interface{}{"deploymentId": deploy.ID}}
		}
	}
	deploy.Status = model.StatusDeployed
	if err := p.DB.UpdateDeploymentResult(ctx, deploy); err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: fmt.Sprintf("record deployment result: %v", err), Metadata: map[string]interface{}{"deploymentId": deploy.ID}}
	}
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: fmt.Sprintf("deploy complete: %s", spec.App),
		Metadata: map[string]interface{}{"deploymentId": deploy.ID, "commitSha": st.commitSHA, "imageTag": st.imageTag},
		publish: func(publishCtx context.Context) {
			sg.Log(publishCtx, "deploy.complete", fmt.Sprintf("deploy complete: %s → %s", spec.App, st.imageTag), map[string]string{"commitSha": st.commitSHA, "imageTag": st.imageTag, "sourceKind": st.sourceKind, "sourceRef": st.sourceRef})
			p.WS.Broadcast(hub.Event{Type: "deploy.completed", AppID: spec.App, Payload: map[string]string{"sagaId": sg.ID, "imageTag": st.imageTag}})
			p.emitBeacon(publishCtx, model.BeaconEvent{App: spec.App, Type: "deploy.succeeded", Severity: model.BeaconInfo,
				Title: fmt.Sprintf("%s deploy succeeded", spec.App), Body: fmt.Sprintf("Deployment %s completed successfully.", deploy.ID), DedupeKey: fmt.Sprintf("%s:deploy", spec.App),
				Metadata: map[string]interface{}{"deploymentId": deploy.ID, "sagaId": sg.ID, "commitSha": st.commitSHA, "imageTag": st.imageTag, "sourceKind": st.sourceKind, "sourceRef": st.sourceRef, "correlationKey": fmt.Sprintf("%s:deploy", spec.App)}})
		}}
}

func (p *Pipeline) recordDeploymentStepStart(ctx context.Context, deploy *model.Deployment, sg *saga.Saga, stepName, operationID string, attempt int) error {
	if deploy == nil || sg == nil {
		return fmt.Errorf("deployment step identity is unavailable")
	}
	step := model.DeploymentStep{
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
	}
	if p.StartDeploymentStep != nil {
		return p.StartDeploymentStep(ctx, step)
	}
	if p.DB == nil {
		return fmt.Errorf("deployment step store is unavailable")
	}
	return p.DB.StartDeploymentStep(ctx, step)
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
