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
	sagaID, _, _ := p.RollbackRegionsOperation(spec, current, prev, nil)
	return sagaID
}

func (p *Pipeline) RollbackRegions(spec *model.InfraSpec, current model.Deployment, prev *model.Deployment, requestedRegions []string) string {
	sagaID, _, _ := p.RollbackRegionsOperation(spec, current, prev, requestedRegions)
	return sagaID
}

// RollbackRegionsOperation exposes the durable operation identifier to typed
// control clients while preserving the legacy saga-returning API.
func (p *Pipeline) RollbackRegionsOperation(spec *model.InfraSpec, current model.Deployment, prev *model.Deployment, requestedRegions []string, extraMetadata ...map[string]interface{}) (string, string, error) {
	return p.RollbackRegionsOperationContext(context.Background(), spec, current, prev, requestedRegions, extraMetadata...)
}

// RollbackRegionsOperationContext durably commits a rollback plan while the
// initiating request is still live. If that request is canceled before the
// transaction commits, the client can retry with the same idempotency key
// instead of receiving a receipt for work it could not observe.
func (p *Pipeline) RollbackRegionsOperationContext(ctx context.Context, spec *model.InfraSpec, current model.Deployment, prev *model.Deployment, requestedRegions []string, extraMetadata ...map[string]interface{}) (string, string, error) {
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
	regions := selectedResolvedRegions(spec, requestedRegions)

	operationID := uuid.NewString()
	payload := map[string]interface{}{
		"deploymentId":        deploy.ID,
		"app":                 spec.App,
		"imageTag":            prev.ImageTag,
		"sourceDeploymentId":  prev.ID,
		"currentDeploymentId": current.ID,
		"regions":             requestedRegions,
	}
	metadata := map[string]interface{}{}
	for key, value := range payload {
		metadata[key] = value
	}
	if len(extraMetadata) > 0 {
		for key, value := range extraMetadata[0] {
			metadata[key] = value
		}
	}
	operation := &model.Operation{
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
		Metadata:    metadata,
	}
	if err := p.DB.InsertRollbackOperation(ctx, deploy, regions, operation); err != nil {
		_ = sg.Log(ctx, "rollback.error", fmt.Sprintf("persist rollback operation failed: %v", err), nil)
		return sg.ID, operationID, err
	}

	_ = sg.Log(ctx, "rollback.queued", fmt.Sprintf("queued rollback for %s to %s", spec.App, prev.ImageTag), map[string]string{
		"deploymentId":        deploy.ID,
		"sourceDeploymentId":  prev.ID,
		"currentDeploymentId": current.ID,
	})
	return sg.ID, operationID, nil
}

func (p *Pipeline) runRollback(ctx context.Context, spec *model.InfraSpec, deploy *model.Deployment, sg *saga.Saga, imageTag string, operationID string, attempt int, requestedRegions []string) {
	regions := selectedResolvedRegions(spec, requestedRegions)
	var startErr error
	failureBody := "Rollback could not start because Nomad is not connected."
	if len(regions) == 0 {
		startErr = fmt.Errorf("rollback has no valid region targets")
		failureBody = "Rollback was blocked because no valid region target was selected."
	} else if p.Production && !model.IsContentAddressedImage(imageTag) {
		startErr = fmt.Errorf("production rollback image must be pinned by sha256 OCI digest")
		failureBody = "Rollback was blocked because the historical image is not content-addressed."
	} else if p.Production {
		if err := p.verifyRegistryArtifact(ctx, imageTag); err != nil {
			startErr = fmt.Errorf("production rollback registry verification failed: %w", err)
			failureBody = "Rollback was blocked because the pinned registry artifact could not be verified."
		}
	}
	if startErr == nil && p.Nomad == nil {
		startErr = fmt.Errorf("nomad not connected")
	}
	if startErr != nil {
		_ = p.DB.UpdateDeployment(ctx, deploy.ID, model.StatusFailed)
		_ = p.DB.FailIncompleteDeploymentRegions(ctx, deploy.ID, startErr.Error())
		_ = p.DB.FinishOperation(ctx, operationID, model.OperationFailed, startErr.Error(), map[string]interface{}{
			"deploymentId": deploy.ID,
			"imageTag":     imageTag,
		})
		_ = sg.Log(ctx, "rollback.failed", startErr.Error(), nil)
		p.emitBeacon(ctx, model.BeaconEvent{
			App:       spec.App,
			Type:      "rollback.failed",
			Severity:  model.BeaconCritical,
			Title:     fmt.Sprintf("%s rollback failed", spec.App),
			Body:      failureBody,
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
			for _, region := range regions {
				if regionalServiceProcessCount(spec, region.Name) == 0 {
					continue
				}
				job := nomad.TranslateForRegion(spec, imageTag, env, region)
				evalID, err := p.Nomad.SubmitJobRegion(job, region.NomadRegion)
				if err != nil {
					_ = p.DB.UpdateDeploymentRegion(ctx, deploy.ID, region.Name, model.StatusFailed, "", err.Error(), 0)
					return fmt.Errorf("submit rollback in region %s: %w", region.Name, err)
				}
				_ = p.DB.UpdateDeploymentRegion(ctx, deploy.ID, region.Name, model.StatusSubmitting, evalID, "", 0)
			}
			return nil
		}},
		{name: "healthy", fn: func(ctx context.Context, st *state, sg *saga.Saga) error {
			for _, region := range regions {
				if regionalServiceProcessCount(spec, region.Name) == 0 {
					continue
				}
				if err := p.Nomad.WaitHealthyRegion(ctx, spec.App, region.NomadRegion, 5*time.Minute); err != nil {
					_ = p.DB.UpdateDeploymentRegion(ctx, deploy.ID, region.Name, model.StatusFailed, "", err.Error(), 0)
					return fmt.Errorf("rollback readiness in region %s: %w", region.Name, err)
				}
				_ = p.DB.UpdateDeploymentRegion(ctx, deploy.ID, region.Name, model.StatusHealthy, "", "", region.TrafficWeight)
			}
			return nil
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
			_ = p.DB.FailIncompleteDeploymentRegions(ctx, deploy.ID, fmt.Sprintf("rollback failed at %s: %v", s.name, err))
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
	for _, region := range regions {
		_ = p.DB.UpdateDeploymentRegion(ctx, deploy.ID, region.Name, model.StatusDeployed, "", "", region.TrafficWeight)
	}
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

func selectedResolvedRegions(spec *model.InfraSpec, requested []string) []model.ResolvedRegion {
	all := spec.ResolvedRegions()
	if len(requested) == 0 {
		return all
	}
	wanted := make(map[string]bool, len(requested))
	for _, region := range requested {
		wanted[region] = true
	}
	selected := make([]model.ResolvedRegion, 0, len(requested))
	for _, region := range all {
		if wanted[region.Name] {
			selected = append(selected, region)
		}
	}
	return selected
}
