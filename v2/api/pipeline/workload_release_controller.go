package pipeline

import (
	"context"
	"fmt"
	"strings"

	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

// WorkloadReleaseTargetObserver reads the exact admitted target revision and
// its allocation health. Implementations must not mutate Nomad or ingress.
type WorkloadReleaseTargetObserver interface {
	ObserveDeploymentJobHealth(context.Context, nomad.CASDeploymentJobRequest) (nomad.DeploymentJobHealthObservation, error)
}

// WorkloadReleaseTargetHealthController records read-only target evidence
// into the operation-fenced receipt. Route publication remains a separate,
// explicitly qualified transition.
type WorkloadReleaseTargetHealthController struct {
	receipts *WorkloadReleaseReceiptRepository
	observer WorkloadReleaseTargetObserver
}

func NewWorkloadReleaseTargetHealthController(receiptStore WorkloadReleaseReceiptCAS, observer WorkloadReleaseTargetObserver) (*WorkloadReleaseTargetHealthController, error) {
	if receiptStore == nil || observer == nil {
		return nil, fmt.Errorf("workload release target health requires durable receipts and a Nomad observer")
	}
	return &WorkloadReleaseTargetHealthController{receipts: &WorkloadReleaseReceiptRepository{store: receiptStore}, observer: observer}, nil
}

// ObserveTargetHealth performs no write until exact target job/source
// provenance has been checked by the Nomad observer. Pending placement is
// durably recorded as warming evidence; only a later route controller can
// move traffic or authorize old-generation drain.
func (c *WorkloadReleaseTargetHealthController) ObserveTargetHealth(ctx context.Context, claim store.OperationClaim, intent WorkloadReleaseIntent) (store.WorkloadReleaseReceiptRecord, error) {
	if c == nil || c.receipts == nil || c.observer == nil || strings.TrimSpace(claim.OperationID()) == "" || strings.TrimSpace(claim.OwnerID()) == "" || claim.Generation() <= 0 {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release target health requires an active operation claim")
	}
	if err := intent.Validate(); err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	record, receipt, err := c.receipts.Load(ctx, intent)
	if err != nil || record == nil {
		if err == nil {
			err = fmt.Errorf("planned workload release receipt is missing")
		}
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	if record.OperationID != claim.OperationID() || (receipt.Phase != WorkloadReleaseWarming && receipt.Phase != WorkloadReleaseRouting && receipt.Phase != WorkloadReleaseDraining) {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release is not active under this operation claim")
	}
	request := nomad.CASDeploymentJobRequest{
		// The generic observer accepts a direct job ID through App when no
		// managed-create identity is being registered. This target already
		// exists, so preserve its exact admitted ID rather than recomputing it.
		App: intent.TargetJobID, Region: intent.NomadRegion, Namespace: intent.Namespace,
		DeploymentID: intent.TargetDeploymentID, SpecDigest: intent.TargetSpecDigest,
		OperationID: intent.TargetOperationID, ExecutionID: intent.TargetExecutionID,
		JobDigest: strings.TrimPrefix(intent.TargetJobDigest, "sha256:"), ImageTag: intent.TargetImage,
	}
	observation, err := c.observer.ObserveDeploymentJobHealth(ctx, request)
	if err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	next, err := receipt.WithTargetAllocationReadback(intent, observation)
	if err != nil {
		healthy := 0
		for _, allocation := range observation.Allocations {
			if allocation.Healthy {
				healthy++
			}
		}
		if healthy != 0 || receipt.RouteGeneration == 0 {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
		next = receipt
		next.Phase = WorkloadReleaseHeld
		next.HeldReason = "all pinned target allocations lost health"
		next, err = next.WithTargetAllocationReadback(intent, observation)
		if err != nil {
			return store.WorkloadReleaseReceiptRecord{}, err
		}
	}
	return c.receipts.Advance(ctx, claim, intent, record.ReceiptDigest, next)
}
