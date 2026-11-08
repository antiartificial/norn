package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"

	"norn/v2/api/effect"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleetdeploy"
	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

type etcdWorkloadReleaseExecutor struct {
	operations *etcdstore.V3OperationStore
	nomad      *nomad.Client
	route      fleetdeploy.ClaimedFleetRouteTransport
	target     *pipeline.WorkloadReleaseTargetHealthController
	stop       *pipeline.WorkloadReleaseStopEffects
}

func newEtcdWorkloadReleaseExecutor(operations *etcdstore.V3OperationStore, nomadClient *nomad.Client, route fleetdeploy.ClaimedFleetRouteTransport) (*etcdWorkloadReleaseExecutor, error) {
	if operations == nil || nomadClient == nil || route.Listen == nil {
		return nil, fmt.Errorf("etcd workload release executor dependencies are incomplete")
	}
	effects, err := etcdstore.NewV3WorkloadReleaseEffectReservations(operations)
	if err != nil {
		return nil, err
	}
	target, err := pipeline.NewWorkloadReleaseTargetHealthController(operations, nomadClient)
	if err != nil {
		return nil, err
	}
	stop, err := pipeline.NewWorkloadReleaseStopEffectsWithStore(effects, operations, nomadClient)
	if err != nil {
		return nil, err
	}
	return &etcdWorkloadReleaseExecutor{operations: operations, nomad: nomadClient, route: route, target: target, stop: stop}, nil
}

func (e *etcdWorkloadReleaseExecutor) ExecuteOperation(context.Context, *model.Operation, store.OperationClaim) (*pipeline.OperationResult, error) {
	return nil, fmt.Errorf("workload release requires the claimed app lock")
}

func (e *etcdWorkloadReleaseExecutor) ExecuteOperationWithAppLock(ctx context.Context, op *model.Operation, claim store.OperationClaim, lock store.AppOperationLock) (*pipeline.OperationResult, error) {
	if e == nil || e.operations == nil || op == nil || op.Kind != "app.workload-release" || claim.OperationID() != op.ID || lock == nil || lock.Context().Err() != nil {
		return nil, fmt.Errorf("claimed workload release executor is unavailable")
	}
	return reconcileWorkloadRelease(ctx, claim, lock, e)
}

// workloadReleaseDriver is the narrow seam between the deterministic
// lifecycle reconciler and its durable/runtime effects. Tests can inject a
// lost response after any completed boundary while retaining the same
// reconciler used by the etcd worker.
type workloadReleaseDriver interface {
	Verify(context.Context, store.OperationClaim, store.AppOperationLock) (pipeline.WorkloadReleaseIntent, error)
	Load(context.Context, pipeline.WorkloadReleaseIntent) (*store.WorkloadReleaseReceiptRecord, pipeline.WorkloadReleaseReceipt, error)
	Initialize(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) error
	Advance(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent, string, pipeline.WorkloadReleaseReceipt) (store.WorkloadReleaseReceiptRecord, error)
	ObserveHealth(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) error
	PublishRouteStep(context.Context, store.OperationClaim, store.AppOperationLock, pipeline.WorkloadReleaseIntent, int) error
	StopAndReadback(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) error
	Complete(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) (store.WorkloadReleaseReceiptRecord, error)
}

func reconcileWorkloadRelease(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, driver workloadReleaseDriver) (*pipeline.OperationResult, error) {
	intent, err := driver.Verify(ctx, claim, lock)
	if err != nil {
		return nil, err
	}
	record, _, err := driver.Load(ctx, intent)
	if err != nil {
		return nil, err
	}
	if record == nil {
		if err := driver.Initialize(ctx, claim, intent); err != nil {
			return nil, err
		}
	}
	for remaining := len(intent.RouteSteps) + 6; remaining > 0; remaining-- {
		if ctx.Err() != nil || lock.Context().Err() != nil {
			return nil, store.ErrOperationOwnershipLost
		}
		intent, err = driver.Verify(ctx, claim, lock)
		if err != nil {
			return nil, err
		}
		record, receipt, err := driver.Load(ctx, intent)
		if err != nil || record == nil || record.OperationID != claim.OperationID() {
			return nil, fmt.Errorf("claimed workload receipt is unavailable: %v", err)
		}
		if receipt.Phase == pipeline.WorkloadReleaseComplete {
			return &pipeline.OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: "workload release verified", Metadata: map[string]interface{}{"intentId": intent.ID, "receiptDigest": record.ReceiptDigest}}, nil
		}
		if receipt.Phase == pipeline.WorkloadReleaseHeld {
			return &pipeline.OperationResult{Claim: claim, Status: model.OperationFailed, Message: "workload release held: " + receipt.HeldReason,
				Metadata: map[string]interface{}{"manualRecoveryRequired": true, "workloadReleaseHeld": true, "intentId": intent.ID}}, nil
		}
		if receipt.Phase == pipeline.WorkloadReleasePlanned {
			warming := receipt
			warming.Phase = pipeline.WorkloadReleaseWarming
			if _, err := driver.Advance(ctx, claim, intent, record.ReceiptDigest, warming); err != nil {
				return nil, err
			}
			continue
		}
		if receipt.Phase != pipeline.WorkloadReleaseWarming && receipt.Phase != pipeline.WorkloadReleaseRouting && receipt.Phase != pipeline.WorkloadReleaseDraining {
			return nil, fmt.Errorf("unknown workload release phase %q", receipt.Phase)
		}
		if err := driver.ObserveHealth(ctx, claim, intent); err != nil {
			return nil, err
		}
		record, receipt, err = driver.Load(ctx, intent)
		if err != nil || record == nil {
			return nil, fmt.Errorf("workload health receipt is unavailable: %v", err)
		}
		if receipt.Phase == pipeline.WorkloadReleaseHeld {
			continue
		}
		if healthyWorkloadTargets(receipt) != intent.TargetReplicas {
			return nil, &effect.PendingError{Resource: workloadReleaseResource(intent), Reason: "target does not have full healthy replica capacity"}
		}
		switch receipt.Phase {
		case pipeline.WorkloadReleaseWarming, pipeline.WorkloadReleaseRouting:
			step := 0
			if receipt.RouteGeneration > 0 {
				step = receipt.RouteStep + 1
			}
			if step >= len(intent.RouteSteps) {
				return nil, fmt.Errorf("workload release route sequence is exhausted before drain completion")
			}
			if err := driver.PublishRouteStep(ctx, claim, lock, intent, step); err != nil {
				return nil, err
			}
		case pipeline.WorkloadReleaseDraining:
			if len(receipt.DrainedOldIDs) != len(intent.OldAllocationIDs) {
				if err := driver.StopAndReadback(ctx, claim, intent); err != nil {
					return nil, err
				}
				continue
			}
			if receipt.RouteStep < len(intent.RouteSteps)-1 {
				if err := driver.PublishRouteStep(ctx, claim, lock, intent, receipt.RouteStep+1); err != nil {
					return nil, err
				}
				continue
			}
			completed, err := driver.Complete(ctx, claim, intent)
			if err != nil {
				return nil, err
			}
			return &pipeline.OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: "workload release route, target health, and old allocation drain verified",
				Metadata: map[string]interface{}{"intentId": intent.ID, "receiptDigest": completed.ReceiptDigest, "routeGeneration": receipt.RouteGeneration}}, nil
		}
	}
	return nil, &effect.PendingError{Resource: workloadReleaseResource(intent), Reason: "workload release exceeded its bounded reconciliation steps"}
}

func (e *etcdWorkloadReleaseExecutor) Verify(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock) (pipeline.WorkloadReleaseIntent, error) {
	intent, _, err := e.operations.VerifyClaimedWorkloadRelease(ctx, claim, lock)
	return intent, err
}

func (e *etcdWorkloadReleaseExecutor) Load(ctx context.Context, intent pipeline.WorkloadReleaseIntent) (*store.WorkloadReleaseReceiptRecord, pipeline.WorkloadReleaseReceipt, error) {
	return e.loadReceipt(ctx, intent)
}

func (e *etcdWorkloadReleaseExecutor) Initialize(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	planned, err := json.Marshal(pipeline.WorkloadReleaseReceipt{IntentID: intent.ID, Phase: pipeline.WorkloadReleasePlanned})
	if err != nil {
		return err
	}
	_, err = e.operations.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, intent.ID, "", planned)
	return err
}

func (e *etcdWorkloadReleaseExecutor) Advance(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent, expected string, receipt pipeline.WorkloadReleaseReceipt) (store.WorkloadReleaseReceiptRecord, error) {
	return e.advanceReceipt(ctx, claim, intent, expected, receipt)
}

func (e *etcdWorkloadReleaseExecutor) ObserveHealth(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	if _, err := e.target.ObserveTargetHealth(ctx, claim, intent); err != nil {
		return &effect.PendingError{Resource: workloadReleaseResource(intent), Reason: "fresh target allocation health is unavailable", Cause: err}
	}
	return nil
}

func (e *etcdWorkloadReleaseExecutor) PublishRouteStep(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, intent pipeline.WorkloadReleaseIntent, step int) error {
	return e.publishRouteStep(ctx, claim, lock, intent, step)
}

func (e *etcdWorkloadReleaseExecutor) StopAndReadback(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	result, err := e.stop.ExecuteStop(ctx, claim, intent)
	if err != nil {
		return err
	}
	if _, err := e.stop.RecordDrainReadback(ctx, claim, intent, result); err != nil {
		return &effect.PendingError{EffectID: result.EffectID, Resource: workloadReleaseResource(intent), Reason: "old allocation drain readback requires same-intent reconciliation", Cause: err}
	}
	return nil
}

func (e *etcdWorkloadReleaseExecutor) Complete(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) (store.WorkloadReleaseReceiptRecord, error) {
	return e.stop.MarkComplete(ctx, claim, intent)
}

func (e *etcdWorkloadReleaseExecutor) loadReceipt(ctx context.Context, intent pipeline.WorkloadReleaseIntent) (*store.WorkloadReleaseReceiptRecord, pipeline.WorkloadReleaseReceipt, error) {
	record, err := e.operations.LoadWorkloadReleaseReceipt(ctx, intent.ID)
	if err != nil || record == nil {
		return record, pipeline.WorkloadReleaseReceipt{}, err
	}
	var receipt pipeline.WorkloadReleaseReceipt
	if err := json.Unmarshal(record.Receipt, &receipt); err != nil || receipt.ValidateAgainst(intent) != nil {
		return nil, pipeline.WorkloadReleaseReceipt{}, fmt.Errorf("workload release receipt failed validation")
	}
	return record, receipt, nil
}

func (e *etcdWorkloadReleaseExecutor) advanceReceipt(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent, expected string, receipt pipeline.WorkloadReleaseReceipt) (store.WorkloadReleaseReceiptRecord, error) {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	return e.operations.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, intent.ID, expected, encoded)
}

func (e *etcdWorkloadReleaseExecutor) publishRouteStep(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, intent pipeline.WorkloadReleaseIntent, step int) error {
	listener, err := e.route.Listen()
	if err != nil {
		return &effect.PendingError{Resource: workloadReleaseResource(intent), Reason: "private workload route authority listener is unavailable", Cause: err}
	}
	defer listener.Close()
	authority, err := ingress.NewControlRouteAuthorityHandler(e.route.NodeURIs, func(ctx context.Context, intentID, nodeID string) (*ingress.AuthorizedRoutePublication, error) {
		return e.operations.AuthorizeClaimedWorkloadReleaseRouteForNode(ctx, claim, lock, intentID, nodeID, e.route.ObserverPort)
	})
	if err != nil {
		return err
	}
	serveCtx, stopServing := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() {
		served <- ingress.ServePrivateControlRouteAuthority(serveCtx, lock.Context(), listener, authority,
			e.route.AuthorityCertPEM, e.route.AuthorityKeyPEM, e.route.NodeCAPEM)
	}()
	_, publishErr := e.operations.PublishProveCommitClaimedWorkloadReleaseRouteStep(ctx, claim, lock, step,
		e.route.ObserverPort, e.route.PublisherPort, e.route.EndpointPort,
		e.route.PublisherCAPEM, e.route.PublisherCertPEM, e.route.PublisherKeyPEM,
		e.route.ObserverCAPEM, e.route.ObserverCertPEM, e.route.ObserverKeyPEM, e.route.PublicRoots)
	stopServing()
	_ = listener.Close()
	serveErr := <-served
	if publishErr != nil {
		return publishErr
	}
	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		log.Printf("workload route authority shutdown after route commit: %v", serveErr)
	}
	return nil
}

func healthyWorkloadTargets(receipt pipeline.WorkloadReleaseReceipt) int {
	count := 0
	for _, allocation := range receipt.TargetAllocations {
		if allocation.Healthy {
			count++
		}
	}
	return count
}

func workloadReleaseResource(intent pipeline.WorkloadReleaseIntent) string {
	return "app/" + intent.App + "/workload-release/" + intent.Region
}

// etcdFleetReleaseOperationExecutor serializes deploy and workload route
// writers because both use the same private ingress authority bind address.
type etcdFleetReleaseOperationExecutor struct {
	mu       sync.Mutex
	deploy   *fleetdeploy.ClaimedFleetDeploymentExecutor
	workload *etcdWorkloadReleaseExecutor
}

func (e *etcdFleetReleaseOperationExecutor) ExecuteOperation(ctx context.Context, op *model.Operation, claim store.OperationClaim) (*pipeline.OperationResult, error) {
	if op != nil && op.Kind == "app.deploy" {
		return e.deploy.ExecuteOperation(ctx, op, claim)
	}
	if op == nil || op.Kind != "app.workload-release" {
		return nil, fmt.Errorf("unsupported Fleet release operation")
	}
	return e.workload.ExecuteOperation(ctx, op, claim)
}

func (e *etcdFleetReleaseOperationExecutor) ExecuteOperationWithAppLock(ctx context.Context, op *model.Operation, claim store.OperationClaim, lock store.AppOperationLock) (*pipeline.OperationResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if op != nil && op.Kind == "app.deploy" {
		return e.deploy.ExecuteOperationWithAppLock(ctx, op, claim, lock)
	}
	if op == nil || op.Kind != "app.workload-release" {
		return nil, fmt.Errorf("unsupported Fleet release operation")
	}
	return e.workload.ExecuteOperationWithAppLock(ctx, op, claim, lock)
}
