package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	nomadapi "github.com/hashicorp/nomad/api"
	"norn/v2/api/connector"
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
	if current.App != spec.App || prev.App != spec.App || prev.Status != model.StatusDeployed || current.Environment != prev.Environment {
		return store.AcceptedOperation{}, fmt.Errorf("rollback source and current deployment identity do not match")
	}
	if prev.SpecDigest != "" {
		digest, err := model.InfraSpecDigest(spec)
		if err != nil || digest != prev.SpecDigest || !model.IsContentAddressedImage(prev.ImageTag) {
			return store.AcceptedOperation{}, fmt.Errorf("rollback source spec does not match the current application spec")
		}
	}
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "rollback")
	started := time.Now()
	deploy := &model.Deployment{
		ID:            uuid.New().String(),
		App:           spec.App,
		CommitSHA:     prev.CommitSHA,
		ImageTag:      prev.ImageTag,
		SpecDigest:    prev.SpecDigest,
		Environment:   current.Environment,
		SagaID:        sg.ID,
		Status:        model.StatusQueued,
		SourceKind:    "rollback",
		SourceRef:     prev.ID,
		SourceDirty:   prev.SourceDirty,
		SourceChanges: prev.SourceChanges,
		StartedAt:     started,
	}
	regions, err := completeRollbackRegions(spec, requestedRegions)
	if err != nil {
		return store.AcceptedOperation{}, err
	}

	operationID := uuid.NewString()
	payload := map[string]interface{}{
		"deploymentId":        deploy.ID,
		"app":                 spec.App,
		"imageTag":            prev.ImageTag,
		"specDigest":          prev.SpecDigest,
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

func (p *Pipeline) runRollback(ctx context.Context, op *model.Operation, spec *model.InfraSpec, deploy *model.Deployment, sg *saga.Saga, imageTag string, claim store.OperationClaim, attempt int, requestedRegions []string) *OperationResult {
	operationID := claim.OperationID()
	regions, regionErr := completeRollbackRegions(spec, requestedRegions)
	workloads := p.workloadConnector()
	var startErr error
	failureBody := "Rollback could not start because Nomad is not connected."
	if regionErr != nil {
		startErr = regionErr
		failureBody = "Rollback was blocked because its region selection cannot represent a whole-app deployment."
	} else if !rollbackIntentMatchesDeployment(op, spec, deploy, imageTag) {
		startErr = fmt.Errorf("rollback signed intent does not match deployment")
		failureBody = "Rollback was blocked because its accepted intent no longer matches the deployment."
	} else if len(regions) == 0 {
		startErr = fmt.Errorf("rollback has no valid region targets")
		failureBody = "Rollback was blocked because no valid region target was selected."
	} else if p.productionReleaseLane() {
		if err := p.reAdmitProductionRollback(ctx, spec, deploy, imageTag); err != nil {
			startErr = fmt.Errorf("production rollback admission failed: %w", err)
			failureBody = "Rollback was blocked because the prior release no longer satisfies immutable production admission."
		}
	}
	if startErr == nil && deploy.SpecDigest != "" {
		digest, err := model.InfraSpecDigest(spec)
		if err != nil || digest != deploy.SpecDigest || deploy.ImageTag != imageTag || !model.IsContentAddressedImage(imageTag) || deploy.SourceRef == "" {
			startErr = fmt.Errorf("rollback source spec provenance changed")
			failureBody = "Rollback was blocked because its source spec provenance changed."
		} else {
			source, lookupErr := p.DB.GetDeployment(ctx, deploy.SourceRef)
			if lookupErr != nil || source == nil || source.App != spec.App || source.Environment != deploy.Environment || source.SpecDigest != deploy.SpecDigest || source.ImageTag != imageTag || source.Status != model.StatusDeployed {
				startErr = fmt.Errorf("rollback source deployment provenance is unavailable")
				failureBody = "Rollback was blocked because its source deployment could not be verified."
			}
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
				evalID, err := workloads.Submit(ctx, connector.SubmitRequest{
					Spec: spec, Image: imageTag, Environment: env, Region: region, DeploymentID: deploy.ID,
					DatabaseRevision: revision, DesiredReplicaCounts: desiredCounts,
					PrepareNomadJob: func(job *nomadapi.Job) error {
						return bindDeploymentJobProvenance(job, deploy.ID, deploy.SpecDigest, op.Payload)
					},
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
			_ = p.recordDeploymentStepFinish(ctx, deploy.ID, s.name, model.DeploymentStepFailed, elapsed, err.Error(), map[string]interface{}{"operationId": operationID})
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

		if err := p.recordDeploymentStepFinish(ctx, deploy.ID, s.name, model.DeploymentStepComplete, elapsed, "", map[string]interface{}{"operationId": operationID}); err != nil {
			return &OperationResult{Claim: claim, Status: model.OperationFailed,
				Message:  fmt.Sprintf("record rollback step %s completion: %v", s.name, err),
				Metadata: map[string]interface{}{"deploymentId": deploy.ID, "step": s.name, "imageTag": imageTag, "manualRecoveryRequired": true}}
		}
		_ = sg.StepComplete(ctx, s.name, elapsed)
		p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: spec.App, Payload: map[string]string{
			"step":       s.name,
			"sagaId":     sg.ID,
			"status":     "complete",
			"index":      idx,
			"total":      total,
			"durationMs": fmt.Sprintf("%d", elapsed),
		}})
	}

	completion := make([]store.DeploymentCompletionRegion, 0, len(regions))
	for _, region := range regions {
		completion = append(completion, store.DeploymentCompletionRegion{Region: region.Name, ActiveWeight: region.TrafficWeight})
	}
	deploy.Status = model.StatusDeployed
	message := fmt.Sprintf("rollback complete: %s", spec.App)
	metadata := map[string]interface{}{"deploymentId": deploy.ID, "imageTag": imageTag}
	if err := p.DB.CompleteDeploymentResult(ctx, claim, deploy, completion, message, metadata); err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: fmt.Sprintf("record rollback result: %v", err), Metadata: map[string]interface{}{"deploymentId": deploy.ID}}
	}
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: message, Metadata: metadata, finished: true, publish: func(publishCtx context.Context) {
		_ = sg.Log(publishCtx, "rollback.complete", fmt.Sprintf("rollback complete: %s -> %s", spec.App, imageTag), nil)
		p.WS.Broadcast(hub.Event{Type: "deploy.completed", AppID: spec.App, Payload: map[string]string{"sagaId": sg.ID, "imageTag": imageTag}})
		p.emitBeacon(publishCtx, model.BeaconEvent{App: spec.App, Type: "rollback.succeeded", Severity: model.BeaconInfo, Title: fmt.Sprintf("%s rollback succeeded", spec.App), Body: fmt.Sprintf("Rollback to %s completed successfully.", imageTag), DedupeKey: fmt.Sprintf("%s:rollback", spec.App), Metadata: map[string]interface{}{"deploymentId": deploy.ID, "sagaId": sg.ID, "imageTag": imageTag, "correlationKey": fmt.Sprintf("%s:rollback", spec.App)}})
	}}
}

func rollbackIntentMatchesDeployment(op *model.Operation, spec *model.InfraSpec, deploy *model.Deployment, imageTag string) bool {
	return op != nil && spec != nil && deploy != nil && op.Kind == "app.rollback" && op.App == spec.App && deploy.App == spec.App &&
		stringFromMap(op.Payload, "app") == spec.App && stringFromMap(op.Payload, "deploymentId") == deploy.ID &&
		stringFromMap(op.Payload, "sourceDeploymentId") == deploy.SourceRef && op.Ref == deploy.SourceRef &&
		stringFromMap(op.Payload, "imageTag") == imageTag && imageTag == deploy.ImageTag &&
		stringFromMap(op.Payload, "specDigest") == deploy.SpecDigest
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
	if err := p.VerifyReleaseArtifact(ctx, spec, target.CommitSHA, target.ImageTag, candidate); err != nil {
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

// A successful rollback is currently recorded as the app's active deployment.
// Until the deployment model has regional lineage, every declared region must
// be restored together; a subset cannot truthfully become that active row.
func completeRollbackRegions(spec *model.InfraSpec, requested []string) ([]model.ResolvedRegion, error) {
	all := spec.ResolvedRegions()
	if len(requested) == 0 {
		return all, nil
	}
	selected := selectedResolvedRegions(spec, requested)
	if len(selected) != len(all) {
		return nil, fmt.Errorf("partial regional rollback requires regional deployment lineage")
	}
	declared := make(map[string]bool, len(all))
	for _, region := range all {
		declared[region.Name] = true
	}
	for _, name := range requested {
		if !declared[name] {
			return nil, fmt.Errorf("rollback region %q is not declared", name)
		}
	}
	return all, nil
}
