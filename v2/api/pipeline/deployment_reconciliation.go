package pipeline

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

type deploymentReconciliationSource interface {
	VerifyAcceptedOperation(context.Context, string) (store.AcceptedOperation, error)
	DeploymentReconciliationCandidate(context.Context, string) (store.DeploymentReconciliationCandidate, error)
}

func (p *Pipeline) reconciliationSource() (deploymentReconciliationSource, error) {
	if p == nil || p.DB == nil || p.Nomad == nil {
		return nil, fmt.Errorf("deployment reconciliation runtime is unavailable")
	}
	source, ok := p.OperationStore.(deploymentReconciliationSource)
	if !ok {
		return nil, fmt.Errorf("signed deployment reconciliation source is unavailable")
	}
	return source, nil
}

func checkedReconciliationCandidate(spec *model.InfraSpec, candidate store.DeploymentReconciliationCandidate) error {
	d := candidate.Acceptance.Deployment
	if spec == nil || d == nil || spec.App != d.App {
		return store.ErrDeploymentReconciliationUnavailable
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil || digest != d.SpecDigest {
		return store.ErrDeploymentReconciliationUnavailable
	}
	current := spec.ResolvedRegions()
	accepted := candidate.Acceptance.Regions
	if len(current) == 0 || len(current) != len(accepted) {
		return store.ErrDeploymentReconciliationUnavailable
	}
	for index := range current {
		if current[index].Name != accepted[index].Name || current[index].NomadRegion != accepted[index].NomadRegion ||
			current[index].TrafficWeight != accepted[index].TrafficWeight || !slices.Equal(current[index].Datacenters, accepted[index].Datacenters) ||
			regionalServiceProcessCount(spec, current[index].Name) == 0 {
			return store.ErrDeploymentReconciliationUnavailable
		}
	}
	return nil
}

// QueueDeploymentReconciliation accepts a new correction intent; the original
// failed deployment operation and its archive remain immutable.
func (p *Pipeline) QueueDeploymentReconciliation(ctx context.Context, spec *model.InfraSpec, sourceOperationID string, request EnqueueRequest) (store.AcceptedOperation, error) {
	source, err := p.reconciliationSource()
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	candidate, err := source.DeploymentReconciliationCandidate(ctx, sourceOperationID)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if err := checkedReconciliationCandidate(spec, candidate); err != nil {
		return store.AcceptedOperation{}, err
	}
	d := candidate.Acceptance.Deployment
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.deployment-reconcile", App: d.App, SagaID: uuid.NewString(), Ref: sourceOperationID,
		Status: model.OperationQueued, Risk: "repair observed deployment projection", Source: "operator", StartedAt: now, MaxAttempts: 1,
		Payload:  map[string]interface{}{"sourceOperationId": sourceOperationID, "deploymentId": d.ID, "imageTag": candidate.ImageTag, "specDigest": d.SpecDigest},
		Metadata: map[string]interface{}{"sourceOperationId": sourceOperationID, "deploymentId": d.ID}}
	request.Admission.OneActiveMutablePerApp = true
	return p.acceptOperation(ctx, request, op, nil, nil)
}

func (p *Pipeline) executeDeploymentReconciliation(ctx context.Context, op *model.Operation, claim store.OperationClaim, spec *model.InfraSpec, sg *saga.Saga) (*OperationResult, error) {
	source, err := p.reconciliationSource()
	if err != nil {
		return nil, err
	}
	verified, err := source.VerifyAcceptedOperation(ctx, op.ID)
	if err != nil {
		return nil, fmt.Errorf("deployment reconciliation acceptance is unavailable: %w", err)
	}
	if verified.Operation.Kind != op.Kind || verified.Operation.ID != op.ID || verified.Operation.Status != model.OperationRunning {
		return nil, store.ErrDeploymentReconciliationUnavailable
	}
	sourceID := stringFromMap(op.Payload, "sourceOperationId")
	candidate, err := source.DeploymentReconciliationCandidate(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	d := candidate.Acceptance.Deployment
	if d == nil || op.App != d.App || op.Ref != sourceID ||
		stringFromMap(op.Payload, "deploymentId") != d.ID || stringFromMap(op.Payload, "imageTag") != candidate.ImageTag ||
		stringFromMap(op.Payload, "specDigest") != d.SpecDigest {
		return nil, store.ErrDeploymentReconciliationUnavailable
	}
	if err := checkedReconciliationCandidate(spec, candidate); err != nil {
		return nil, err
	}
	if err := p.Nomad.VerifyRunningAppImage(ctx, spec, candidate.ImageTag); err != nil {
		return nil, fmt.Errorf("live Nomad image is unproven: %w", err)
	}
	observedAt := time.Now().UTC()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.DB.CompleteDeploymentReconciliation(ctx, claim, candidate, observedAt); err != nil {
		return nil, err
	}
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: "deployment reconciled against live Nomad image",
		Metadata: map[string]interface{}{"sourceOperationId": sourceID, "deploymentId": d.ID, "imageTag": candidate.ImageTag}, finished: true,
		publish: func(publishCtx context.Context) {
			_ = sg.Log(publishCtx, "deployment.reconciled", "deployment projection reconciled against live Nomad image",
				map[string]string{"sourceOperationId": sourceID, "deploymentId": d.ID, "imageTag": candidate.ImageTag})
		}}, nil
}
