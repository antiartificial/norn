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
	"norn/v2/api/store"
)

// QueueRollback atomically accepts a rollback deployment and returns the
// original identities on replay.
func (p *Pipeline) QueueRollback(ctx context.Context, spec *model.InfraSpec, current model.Deployment, prev *model.Deployment, requestedRegions []string, request EnqueueRequest, extraMetadata map[string]interface{}) (store.AcceptedOperation, error) {
	if p == nil || p.DB == nil || p.SagaStore == nil || spec == nil || prev == nil {
		return store.AcceptedOperation{}, fmt.Errorf("rollback pipeline is unavailable")
	}
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "rollback")
	started := time.Now()
	deploy := &model.Deployment{
		ID:            uuid.New().String(),
		App:           spec.App,
		CommitSHA:     prev.CommitSHA,
		ImageTag:      prev.ImageTag,
		Environment:   current.Environment,
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
	if extraMetadata != nil {
		for key, value := range extraMetadata {
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
	accepted, err := p.acceptOperation(ctx, request, *operation, deploy, regions)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if !accepted.Replayed {
		_ = sg.Log(ctx, "rollback.queued", fmt.Sprintf("queued rollback for %s to %s", spec.App, prev.ImageTag), map[string]string{
			"deploymentId": accepted.Intent.DeploymentID, "sourceDeploymentId": prev.ID, "currentDeploymentId": current.ID, "operationId": accepted.Operation.ID,
		})
	}
	return accepted, nil
}

func (p *Pipeline) runRollback(ctx context.Context, spec *model.InfraSpec, deploy *model.Deployment, sg *saga.Saga, imageTag string, claim store.OperationClaim, attempt int, requestedRegions []string) *OperationResult {
	operationID := claim.OperationID()
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
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: startErr.Error(), Metadata: map[string]interface{}{"deploymentId": deploy.ID, "imageTag": imageTag}, publish: func(publishCtx context.Context) {
			_ = sg.Log(publishCtx, "rollback.failed", startErr.Error(), nil)
			p.emitBeacon(publishCtx, model.BeaconEvent{App: spec.App, Type: "rollback.failed", Severity: model.BeaconCritical, Title: fmt.Sprintf("%s rollback failed", spec.App), Body: failureBody, DedupeKey: fmt.Sprintf("%s:rollback", spec.App), Metadata: map[string]interface{}{"deploymentId": deploy.ID, "sagaId": sg.ID, "imageTag": imageTag, "correlationKey": fmt.Sprintf("%s:rollback", spec.App)}})
		}}
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
			// A rollback changes the image, never the database target: its
			// job references the promoted delivery revision, revalidated
			// against the running targets (no mutable fallback, no
			// substitution). A secret must not shadow delivered variables.
			if conflicts := spec.DatabaseEnvConflicts(env); len(conflicts) > 0 {
				return databaseEnvConflictError(conflicts)
			}
			if err := p.requireLiveClaim(ctx, st); err != nil {
				return err
			}
			for _, region := range regions {
				if regionalServiceProcessCount(spec, region.Name) == 0 {
					continue
				}
				revision := int64(0)
				if nomad.HasRuntimeDatabases(spec) {
					material, err := p.RunningDeliveryRevision(ctx, spec, region.NomadRegion, spec.App)
					if err != nil {
						return fmt.Errorf("rollback database delivery in region %s: %w", region.Name, err)
					}
					revision = material.Revision
				}
				desiredCounts, err := p.DB.DesiredReplicaCounts(ctx, spec.App, region.Name)
				if err != nil {
					return fmt.Errorf("load desired replica intent: %w", err)
				}
				job := nomad.TranslateForRegionAt(spec, imageTag, env, region, revision)
				nomad.ApplyDesiredReplicaCounts(job, desiredCounts)
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

	st := &state{spec: spec, imageTag: imageTag, sourceKind: "rollback", sourceRef: deploy.SourceRef, claim: claim}
	total := fmt.Sprintf("%d", len(steps))
	for i, s := range steps {
		if err := ctx.Err(); err != nil {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: fmt.Sprintf("rollback canceled before %s: %v", s.name, err), Metadata: map[string]interface{}{"deploymentId": deploy.ID, "step": s.name, "imageTag": imageTag}}
		}
		idx := fmt.Sprintf("%d", i+1)
		_ = sg.StepStart(ctx, s.name)
		if err := p.recordDeploymentStepStart(ctx, deploy, sg, s.name, operationID, attempt); err != nil {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: fmt.Sprintf("record rollback step %s before execution: %v", s.name, err), Metadata: map[string]interface{}{"deploymentId": deploy.ID, "step": s.name, "imageTag": imageTag}}
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
			stepName, stepErr := s.name, err
			message := fmt.Sprintf("rollback failed at %s: %v", stepName, stepErr)
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: message, Metadata: map[string]interface{}{"deploymentId": deploy.ID, "step": stepName, "imageTag": imageTag}, publish: func(publishCtx context.Context) {
				_ = sg.Log(publishCtx, "rollback.failed", message, nil)
				p.WS.Broadcast(hub.Event{Type: "deploy.failed", AppID: spec.App, Payload: map[string]string{"sagaId": sg.ID, "error": stepErr.Error()}})
				p.emitBeacon(publishCtx, model.BeaconEvent{App: spec.App, Type: "rollback.failed", Severity: model.BeaconCritical, Title: fmt.Sprintf("%s rollback failed", spec.App), Body: fmt.Sprintf("Rollback failed at %s: %v", stepName, stepErr), DedupeKey: fmt.Sprintf("%s:rollback", spec.App), Metadata: map[string]interface{}{"deploymentId": deploy.ID, "sagaId": sg.ID, "imageTag": imageTag, "step": stepName, "correlationKey": fmt.Sprintf("%s:rollback", spec.App)}})
			}}
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
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: fmt.Sprintf("rollback complete: %s", spec.App), Metadata: map[string]interface{}{"deploymentId": deploy.ID, "imageTag": imageTag}, publish: func(publishCtx context.Context) {
		_ = sg.Log(publishCtx, "rollback.complete", fmt.Sprintf("rollback complete: %s -> %s", spec.App, imageTag), nil)
		p.WS.Broadcast(hub.Event{Type: "deploy.completed", AppID: spec.App, Payload: map[string]string{"sagaId": sg.ID, "imageTag": imageTag}})
		p.emitBeacon(publishCtx, model.BeaconEvent{App: spec.App, Type: "rollback.succeeded", Severity: model.BeaconInfo, Title: fmt.Sprintf("%s rollback succeeded", spec.App), Body: fmt.Sprintf("Rollback to %s completed successfully.", imageTag), DedupeKey: fmt.Sprintf("%s:rollback", spec.App), Metadata: map[string]interface{}{"deploymentId": deploy.ID, "sagaId": sg.ID, "imageTag": imageTag, "correlationKey": fmt.Sprintf("%s:rollback", spec.App)}})
	}}
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
