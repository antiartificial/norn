package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/connector"
	"norn/v2/api/hub"
	"norn/v2/api/model"
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
	if prev == nil {
		return "", "", fmt.Errorf("rollback target deployment is required")
	}
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "rollback")
	started := time.Now()
	deploy, err := newRollbackDeployment(spec.App, sg.ID, current, *prev, started)
	if err != nil {
		return "", "", err
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

func newRollbackDeployment(app, sagaID string, current, previous model.Deployment, started time.Time) (*model.Deployment, error) {
	environment, err := rollbackEnvironment(current, previous)
	if err != nil {
		return nil, err
	}
	return &model.Deployment{
		ID:            uuid.New().String(),
		App:           app,
		CommitSHA:     previous.CommitSHA,
		ImageTag:      previous.ImageTag,
		Environment:   environment,
		SagaID:        sagaID,
		Status:        model.StatusQueued,
		SourceKind:    "rollback",
		SourceRef:     previous.ID,
		SourceDirty:   previous.SourceDirty,
		SourceChanges: previous.SourceChanges,
		StartedAt:     started,
	}, nil
}

// rollbackEnvironment preserves the lane of the running deployment. Historical
// rows created before environments were recorded may inherit a non-empty target
// lane, but two explicit, different lanes must never be joined by a rollback.
func rollbackEnvironment(current, previous model.Deployment) (string, error) {
	currentEnvironment := strings.TrimSpace(current.Environment)
	previousEnvironment := strings.TrimSpace(previous.Environment)
	if currentEnvironment != "" && previousEnvironment != "" && currentEnvironment != previousEnvironment {
		return "", fmt.Errorf("rollback deployment environments do not match")
	}
	if currentEnvironment != "" {
		return currentEnvironment, nil
	}
	return previousEnvironment, nil
}

func (p *Pipeline) runRollback(ctx context.Context, spec *model.InfraSpec, deploy *model.Deployment, sg *saga.Saga, imageTag string, operationID string, attempt int, requestedRegions []string) {
	regions := selectedResolvedRegions(spec, requestedRegions)
	workloads := p.workloadConnector()
	var startErr error
	failureBody := "Rollback could not start because the workload connector is not available."
	if len(regions) == 0 {
		startErr = fmt.Errorf("rollback has no valid region targets")
		failureBody = "Rollback was blocked because no valid region target was selected."
	} else if p.productionReleaseLane() {
		if err := p.reAdmitProductionRollback(ctx, spec, deploy, imageTag); err != nil {
			startErr = fmt.Errorf("production rollback admission failed: %w", err)
			failureBody = "Rollback was blocked because the prior release no longer satisfies immutable production admission."
		}
	}
	if startErr == nil && workloads == nil {
		startErr = fmt.Errorf("workload connector is not configured")
	} else if startErr == nil {
		startErr = workloads.Validate(spec, p.productionReleaseLane())
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
				evalID, err := workloads.Submit(ctx, connector.SubmitRequest{
					Spec: spec, Image: imageTag, Environment: env,
					Region: region, DeploymentID: deploy.ID,
				})
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
				if err := workloads.WaitHealthy(ctx, spec.App, region, 5*time.Minute); err != nil {
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

// reAdmitProductionRollback replays immutable admission immediately before a
// production rollback can submit work. Queue-time checks alone are not enough:
// registry state, attestations, and vulnerability policy can change while an
// operation waits for a worker lease.
func (p *Pipeline) reAdmitProductionRollback(ctx context.Context, spec *model.InfraSpec, rollback *model.Deployment, imageTag string) error {
	if p == nil || p.DB == nil || spec == nil || rollback == nil {
		return fmt.Errorf("production rollback evidence store is unavailable")
	}
	if rollback.Environment != "production" || strings.TrimSpace(rollback.SourceRef) == "" {
		return fmt.Errorf("production rollback lacks an exact production source deployment")
	}
	target, err := p.DB.GetDeployment(ctx, rollback.SourceRef)
	if err != nil {
		return fmt.Errorf("load rollback source deployment: %w", err)
	}
	if target.Environment != "production" || target.Status != model.StatusDeployed || target.ImageTag != imageTag || !model.IsContentAddressedImage(target.ImageTag) {
		return fmt.Errorf("rollback source deployment is not the exact admitted production artifact")
	}
	promotion, err := p.DB.GetPromotionOperationByDeploymentID(ctx, target.ID)
	if err != nil {
		return fmt.Errorf("load rollback promotion evidence: %w", err)
	}
	candidate, err := promotionCandidate(p.TrustedQualificationSigningKeys, *promotion)
	if err != nil {
		return err
	}
	if err := model.ValidateReleaseSpecBinding(spec, candidate, target.ImageTag, p.RegistryURL); err != nil {
		return fmt.Errorf("re-admit rollback source binding: %w", err)
	}
	if err := p.VerifyReleaseArtifact(ctx, target.CommitSHA, target.ImageTag, candidate); err != nil {
		return fmt.Errorf("re-admit rollback source artifact: %w", err)
	}
	return nil
}

func promotionCandidate(trustedSigningKeys []string, operation model.Operation) (model.ReleaseCandidate, error) {
	if operation.Kind != "app.deploy" || operation.Status != model.OperationSucceeded {
		return model.ReleaseCandidate{}, fmt.Errorf("rollback source has no successful promotion evidence")
	}
	raw, ok := operation.Metadata["promotionQualification"]
	if !ok {
		return model.ReleaseCandidate{}, fmt.Errorf("rollback source has no signed promotion qualification")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return model.ReleaseCandidate{}, fmt.Errorf("encode rollback promotion qualification: %w", err)
	}
	var qualification model.ReleaseQualification
	if err := json.Unmarshal(encoded, &qualification); err != nil {
		return model.ReleaseCandidate{}, fmt.Errorf("rollback source promotion qualification is invalid")
	}
	if err := model.VerifyReleaseQualificationSignature(trustedSigningKeys, qualification); err != nil {
		return model.ReleaseCandidate{}, fmt.Errorf("rollback source promotion qualification is not currently trusted: %w", err)
	}
	if qualification.App != operation.App || qualification.SourceSHA != stringFromMap(operation.Payload, "sourceSha") || qualification.Artifact != stringFromMap(operation.Payload, "artifact") || qualification.Candidate.Attestation.MaterialSHA != qualification.SourceSHA || qualification.Candidate.Attestation.SubjectDigest != artifactDigest(qualification.Artifact) {
		return model.ReleaseCandidate{}, fmt.Errorf("rollback source promotion qualification is not bound to its promoted deployment")
	}
	return qualification.Candidate, nil
}

func artifactDigest(artifact string) string {
	if before, digest, found := strings.Cut(artifact, "@"); found && before != "" && strings.HasPrefix(digest, "sha256:") {
		return digest
	}
	return ""
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
