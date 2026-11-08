package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

type workloadReleaseTestDriver struct {
	intent       pipeline.WorkloadReleaseIntent
	receipt      pipeline.WorkloadReleaseReceipt
	initialized  bool
	stopWrites   int
	stopChecks   int
	routeWrites  map[int]int
	loseAfter    string
	lossInjected bool
}

func newWorkloadReleaseTestDriver(loseAfter string) *workloadReleaseTestDriver {
	return &workloadReleaseTestDriver{
		intent: pipeline.WorkloadReleaseIntent{
			ID: "intent-1", App: "web", Region: "nyc3", TargetReplicas: 2,
			OldAllocationIDs: []string{"old-allocation"},
			RouteSteps: []pipeline.WorkloadReleaseRouteStep{
				{OldWeight: 100, TargetWeight: 0},
				{OldWeight: 50, TargetWeight: 50},
				{OldWeight: 0, TargetWeight: 100},
			},
		},
		routeWrites: make(map[int]int), loseAfter: loseAfter,
	}
}

func (d *workloadReleaseTestDriver) Verify(context.Context, store.OperationClaim, store.AppOperationLock) (pipeline.WorkloadReleaseIntent, error) {
	return d.intent, nil
}

func (d *workloadReleaseTestDriver) Load(context.Context, pipeline.WorkloadReleaseIntent) (*store.WorkloadReleaseReceiptRecord, pipeline.WorkloadReleaseReceipt, error) {
	if !d.initialized {
		return nil, pipeline.WorkloadReleaseReceipt{}, nil
	}
	return &store.WorkloadReleaseReceiptRecord{IntentID: d.intent.ID, OperationID: "operation-1", ReceiptDigest: "sha256:receipt"}, d.receipt, nil
}

func (d *workloadReleaseTestDriver) Initialize(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) error {
	if !d.initialized {
		d.initialized = true
		d.receipt = pipeline.WorkloadReleaseReceipt{IntentID: d.intent.ID, Phase: pipeline.WorkloadReleasePlanned}
	}
	return d.lost("initialize")
}

func (d *workloadReleaseTestDriver) Advance(_ context.Context, _ store.OperationClaim, _ pipeline.WorkloadReleaseIntent, _ string, receipt pipeline.WorkloadReleaseReceipt) (store.WorkloadReleaseReceiptRecord, error) {
	d.receipt = receipt
	return store.WorkloadReleaseReceiptRecord{IntentID: d.intent.ID, OperationID: "operation-1", ReceiptDigest: "sha256:receipt"}, nil
}

func (d *workloadReleaseTestDriver) ObserveHealth(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) error {
	d.receipt.TargetAllocations = []pipeline.WorkloadReleaseAllocationObservation{
		{AllocationID: "target-a", Healthy: true}, {AllocationID: "target-b", Healthy: true},
	}
	return d.lost("health")
}

func (d *workloadReleaseTestDriver) PublishRouteStep(_ context.Context, _ store.OperationClaim, _ store.AppOperationLock, _ pipeline.WorkloadReleaseIntent, step int) error {
	if d.receipt.RouteGeneration == 0 || step > d.receipt.RouteStep {
		d.routeWrites[step]++
		d.receipt.RouteStep = step
		d.receipt.RouteGeneration = uint64(11 + step)
		d.receipt.RouteOldWeight = d.intent.RouteSteps[step].OldWeight
		d.receipt.RouteTargetWeight = d.intent.RouteSteps[step].TargetWeight
		if step == 0 {
			d.receipt.Phase = pipeline.WorkloadReleaseRouting
		} else {
			d.receipt.Phase = pipeline.WorkloadReleaseDraining
		}
	}
	return d.lost(fmt.Sprintf("route-%d", step))
}

func (d *workloadReleaseTestDriver) StopAndReadback(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) error {
	d.stopChecks++
	if d.stopWrites == 0 {
		d.stopWrites++ // the fake's durable effect marker permits only one write
		if err := d.lost("stop"); err != nil {
			return err
		}
	}
	d.receipt.OldJobStoppedVersion = 7
	d.receipt.OldJobStoppedModifyIndex = 900
	d.receipt.DrainedOldIDs = []string{"old-allocation"}
	return d.lost("drain")
}

func (d *workloadReleaseTestDriver) Complete(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) (store.WorkloadReleaseReceiptRecord, error) {
	d.receipt.Phase = pipeline.WorkloadReleaseComplete
	return store.WorkloadReleaseReceiptRecord{IntentID: d.intent.ID, OperationID: "operation-1", ReceiptDigest: "sha256:complete"}, d.lost("complete")
}

func (d *workloadReleaseTestDriver) lost(boundary string) error {
	if d.loseAfter == boundary && !d.lossInjected {
		d.lossInjected = true
		return &effect.PendingError{Resource: "workload-release-test", Reason: "simulated response loss after durable boundary"}
	}
	return nil
}

type workloadReleaseTestLock struct{ ctx context.Context }

func (l workloadReleaseTestLock) Context() context.Context { return l.ctx }
func (workloadReleaseTestLock) Fence() string              { return "fence-1" }
func (workloadReleaseTestLock) Release()                   {}

func TestWorkloadReleaseReconcilerRecoversLostResponsesAtEveryBoundary(t *testing.T) {
	boundaries := []string{"initialize", "health", "route-0", "route-1", "stop", "drain", "route-2", "complete"}
	for _, boundary := range boundaries {
		t.Run(boundary, func(t *testing.T) {
			claim, err := store.NewOperationClaim("operation-1", "worker-1", 1)
			if err != nil {
				t.Fatal(err)
			}
			driver := newWorkloadReleaseTestDriver(boundary)
			lock := workloadReleaseTestLock{ctx: context.Background()}
			result, err := reconcileWorkloadRelease(context.Background(), claim, lock, driver)
			var pending *effect.PendingError
			if !errors.As(err, &pending) || result != nil {
				t.Fatalf("first reconciliation = (%#v, %v), want pending lost response", result, err)
			}
			result, err = reconcileWorkloadRelease(context.Background(), claim, lock, driver)
			if err != nil || result == nil || result.Status != model.OperationSucceeded {
				t.Fatalf("recovery = (%#v, %v), want succeeded", result, err)
			}
			if driver.receipt.Phase != pipeline.WorkloadReleaseComplete || driver.stopWrites != 1 {
				t.Fatalf("terminal receipt=%q stop writes=%d, want complete and exactly one", driver.receipt.Phase, driver.stopWrites)
			}
			for step := range driver.intent.RouteSteps {
				if driver.routeWrites[step] != 1 {
					t.Errorf("route step %d writes=%d, want exactly one", step, driver.routeWrites[step])
				}
			}
		})
	}
}
