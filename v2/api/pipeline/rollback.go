package pipeline

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/saga"
)

func (p *Pipeline) Rollback(spec *model.InfraSpec, current model.Deployment, prev *model.Deployment) string {
	ctx := context.Background()
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "rollback")
	started := time.Now()
	deploy := &model.Deployment{
		ID:            uuid.New().String(),
		App:           spec.App,
		CommitSHA:     prev.CommitSHA,
		ImageTag:      prev.ImageTag,
		SagaID:        sg.ID,
		Status:        model.StatusQueued,
		SourceKind:    "rollback",
		SourceRef:     prev.ID,
		SourceDirty:   prev.SourceDirty,
		SourceChanges: prev.SourceChanges,
		StartedAt:     started,
	}
	if err := p.DB.InsertDeployment(ctx, deploy); err != nil {
		_ = sg.Log(ctx, "rollback.error", fmt.Sprintf("insert rollback deployment failed: %v", err), nil)
	}

	operationID := uuid.NewString()
	payload := map[string]interface{}{
		"deploymentId":        deploy.ID,
		"app":                 spec.App,
		"imageTag":            prev.ImageTag,
		"sourceDeploymentId":  prev.ID,
		"currentDeploymentId": current.ID,
	}
	if err := p.DB.InsertOperation(ctx, &model.Operation{
		ID:          operationID,
		Kind:        "app.rollback",
		App:         spec.App,
		SagaID:      sg.ID,
		Ref:         prev.ID,
		Status:      model.OperationQueued,
		Risk:        "app rolling update",
		Source:      "pipeline",
		Message:     fmt.Sprintf("queued rollback for %s", spec.App),
		StartedAt:   started,
		MaxAttempts: 1,
		Payload:     payload,
		Metadata:    payload,
	}); err != nil {
		_ = sg.Log(ctx, "rollback.error", fmt.Sprintf("insert rollback operation failed: %v", err), nil)
	}

	_ = sg.Log(ctx, "rollback.queued", fmt.Sprintf("queued rollback for %s to %s", spec.App, prev.ImageTag), map[string]string{
		"deploymentId":        deploy.ID,
		"sourceDeploymentId":  prev.ID,
		"currentDeploymentId": current.ID,
	})
	return sg.ID
}

func (p *Pipeline) runRollback(ctx context.Context, spec *model.InfraSpec, deploy *model.Deployment, sg *saga.Saga, imageTag string, operationID string, attempt int) {
	if p.Nomad == nil {
		err := fmt.Errorf("nomad not connected")
		_ = p.DB.UpdateDeployment(ctx, deploy.ID, model.StatusFailed)
		_ = p.DB.FinishOperation(ctx, operationID, model.OperationFailed, err.Error(), map[string]interface{}{
			"deploymentId": deploy.ID,
			"imageTag":     imageTag,
		})
		_ = sg.Log(ctx, "rollback.failed", err.Error(), nil)
		p.emitBeacon(ctx, model.BeaconEvent{
			App:       spec.App,
			Type:      "rollback.failed",
			Severity:  model.BeaconCritical,
			Title:     fmt.Sprintf("%s rollback failed", spec.App),
			Body:      "Rollback could not start because Nomad is not connected.",
			DedupeKey: fmt.Sprintf("%s:rollback", spec.App),
			Metadata: map[string]interface{}{
				"deploymentId":   deploy.ID,
				"sagaId":         sg.ID,
				"imageTag":       imageTag,
				"correlationKey": fmt.Sprintf("%s:rollback", spec.App),
			},
		})
		return
	}

	steps := []step{
		{name: "resolve-secrets", fn: func(ctx context.Context, st *state, sg *saga.Saga) error {
			env := make(map[string]string)
			if p.Secrets != nil {
				secretEnv, err := p.Secrets.EnvMap(spec.App)
				if err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("resolve secrets: %w", err)
				}
				for k, v := range secretEnv {
					env[k] = v
				}
			}
			job := nomad.Translate(spec, imageTag, env)
			_, err := p.Nomad.SubmitJob(job)
			return err
		}},
		{name: "healthy", fn: func(ctx context.Context, st *state, sg *saga.Saga) error {
			return p.Nomad.WaitHealthy(ctx, spec.App, 5*time.Minute)
		}},
	}

	st := &state{spec: spec, imageTag: imageTag, sourceKind: "rollback", sourceRef: deploy.SourceRef}
	total := fmt.Sprintf("%d", len(steps))
	for i, s := range steps {
		idx := fmt.Sprintf("%d", i+1)
		_ = sg.StepStart(ctx, s.name)
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
			_ = sg.StepFailed(ctx, s.name, err)
			p.recordDeploymentStepFinish(ctx, deploy.ID, s.name, model.DeploymentStepFailed, elapsed, err.Error(), map[string]interface{}{"operationId": operationID})
			p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
				"step":       s.name,
				"sagaId":     sg.ID,
				"status":     "failed",
				"index":      idx,
				"total":      total,
				"durationMs": fmt.Sprintf("%d", elapsed),
			}})
			_ = p.DB.UpdateDeployment(ctx, deploy.ID, model.StatusFailed)
			_ = p.DB.FinishOperation(ctx, operationID, model.OperationFailed, fmt.Sprintf("rollback failed at %s: %v", s.name, err), map[string]interface{}{
				"deploymentId": deploy.ID,
				"step":         s.name,
				"imageTag":     imageTag,
			})
			_ = sg.Log(ctx, "rollback.failed", fmt.Sprintf("rollback failed at %s: %v", s.name, err), nil)
			p.WS.Broadcast(hub.Event{Type: "deploy.failed", AppID: spec.App, Payload: map[string]string{
				"sagaId": sg.ID,
				"error":  err.Error(),
			}})
			p.emitBeacon(ctx, model.BeaconEvent{
				App:       spec.App,
				Type:      "rollback.failed",
				Severity:  model.BeaconCritical,
				Title:     fmt.Sprintf("%s rollback failed", spec.App),
				Body:      fmt.Sprintf("Rollback failed at %s: %v", s.name, err),
				DedupeKey: fmt.Sprintf("%s:rollback", spec.App),
				Metadata: map[string]interface{}{
					"deploymentId":   deploy.ID,
					"sagaId":         sg.ID,
					"imageTag":       imageTag,
					"step":           s.name,
					"correlationKey": fmt.Sprintf("%s:rollback", spec.App),
				},
			})
			return
		}

		_ = sg.StepComplete(ctx, s.name, elapsed)
		p.recordDeploymentStepFinish(ctx, deploy.ID, s.name, model.DeploymentStepComplete, elapsed, "", map[string]interface{}{"operationId": operationID})
		p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
			"step":       s.name,
			"sagaId":     sg.ID,
			"status":     "complete",
			"index":      idx,
			"total":      total,
			"durationMs": fmt.Sprintf("%d", elapsed),
		}})
	}

	deploy.Status = model.StatusDeployed
	_ = p.DB.UpdateDeployment(ctx, deploy.ID, deploy.Status)
	_ = p.DB.FinishOperation(ctx, operationID, model.OperationSucceeded, fmt.Sprintf("rollback complete: %s", spec.App), map[string]interface{}{
		"deploymentId": deploy.ID,
		"imageTag":     imageTag,
	})
	_ = sg.Log(ctx, "rollback.complete", fmt.Sprintf("rollback complete: %s -> %s", spec.App, imageTag), nil)
	p.WS.Broadcast(hub.Event{Type: "deploy.completed", AppID: spec.App, Payload: map[string]string{
		"sagaId":   sg.ID,
		"imageTag": imageTag,
	}})
	p.emitBeacon(ctx, model.BeaconEvent{
		App:       spec.App,
		Type:      "rollback.succeeded",
		Severity:  model.BeaconInfo,
		Title:     fmt.Sprintf("%s rollback succeeded", spec.App),
		Body:      fmt.Sprintf("Rollback to %s completed successfully.", imageTag),
		DedupeKey: fmt.Sprintf("%s:rollback", spec.App),
		Metadata: map[string]interface{}{
			"deploymentId":   deploy.ID,
			"sagaId":         sg.ID,
			"imageTag":       imageTag,
			"correlationKey": fmt.Sprintf("%s:rollback", spec.App),
		},
	})
}
