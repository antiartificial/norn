package pipeline

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/beacon"
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
	DB          *store.DB
	Nomad       *nomad.Client
	WS          *hub.Hub
	SagaStore   saga.Store
	Secrets     *secrets.Manager
	AppsDir     string
	GitToken    string
	GitSSHKey   string
	RegistryURL string
	NetworkMode string
	Beacon      *beacon.Service
	Storage     *storage.Client
	Redpanda    *redpanda.Client
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
}

type step struct {
	name string
	fn   func(ctx context.Context, s *state, sg *saga.Saga) error
}

// Run executes the full deploy pipeline for an app.
// Returns the saga ID for event tracking.
func (p *Pipeline) Run(spec *model.InfraSpec, ref string) string {
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
	specs, err := model.DiscoverApps(p.AppsDir)
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
	}
	sg := saga.NewWithID(p.SagaStore, op.SagaID, spec.App, "pipeline", category)

	switch op.Kind {
	case "app.deploy":
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
		p.run(ctx, spec, deploy, sg, op.ID, op.Attempts)
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
		p.runRollback(ctx, spec, deploy, sg, imageTag, op.ID, op.Attempts)
		return nil
	case "app.preflight":
		sg.Log(ctx, "preflight.start", fmt.Sprintf("preflighting %s (ref: %s)", spec.App, op.Ref), map[string]string{
			"operationId": op.ID,
			"attempt":     strconv.Itoa(op.Attempts),
		})
		p.runPreflight(ctx, spec, op.Ref, sg, op.ID)
		return nil
	default:
		return fmt.Errorf("unsupported operation kind %s", op.Kind)
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

func (p *Pipeline) run(ctx context.Context, spec *model.InfraSpec, deploy *model.Deployment, sg *saga.Saga, operationID string, attempt int) {
	st := &state{
		spec:      spec,
		commitSHA: deploy.CommitSHA,
		sourceRef: deploy.CommitSHA,
	}

	steps := []step{
		{name: "clone", fn: p.clone},
		{name: "build", fn: p.build},
		{name: "test", fn: p.test},
		{name: "snapshot", fn: p.snapshot},
		{name: "migrate", fn: p.migrate},
		{name: "submit", fn: p.submit},
		{name: "healthy", fn: p.healthy},
		{name: "forge", fn: p.forge},
		{name: "cleanup", fn: p.cleanup},
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
			p.DB.UpdateDeployment(ctx, deploy.ID, deploy.Status)
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
					"deploymentId": deploy.ID,
					"sagaId":       sg.ID,
					"commitSha":    st.commitSHA,
					"imageTag":     st.imageTag,
					"step":         s.name,
				},
			})
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
			"deploymentId": deploy.ID,
			"sagaId":       sg.ID,
			"commitSha":    st.commitSHA,
			"imageTag":     st.imageTag,
			"sourceKind":   st.sourceKind,
			"sourceRef":    st.sourceRef,
		},
	})
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
