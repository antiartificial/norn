package pipeline

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"norn/v2/api/ingress"
	"norn/v2/api/nomad"
)

func TestWorkloadReleaseIntentDigestIsStableAcrossAllocationOrder(t *testing.T) {
	first := workloadReleaseFixture()
	second := workloadReleaseFixture()
	second.OldAllocationIDs = []string{"old-b", "old-a"}
	firstDigest, err := first.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := second.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("same allocation set produced different intent digests: %s != %s", firstDigest, secondDigest)
	}
}

func TestWorkloadReleaseRouteStepsUseProductionWeightedRenderer(t *testing.T) {
	intent := workloadReleaseFixture()
	for index, wantBackends := range []int{1, 2, 1} {
		route, err := intent.RenderRouteStep(index)
		if err != nil {
			t.Fatalf("render route step %d: %v", index, err)
		}
		if len(route.BackendNames) != wantBackends || route.SHA256 == "" {
			t.Fatalf("route step %d has %d backends and digest %q", index, len(route.BackendNames), route.SHA256)
		}
		if err := ingress.RequireTLSRenderedRoute(route); err != nil {
			t.Fatalf("route step %d is not TLS qualified: %v", index, err)
		}
	}
	if _, err := intent.RenderRouteStep(3); err == nil {
		t.Fatal("route step outside immutable intent rendered")
	}
}

func TestWorkloadReleaseConsumesPinnedNomadAllocationReadback(t *testing.T) {
	intent := workloadReleaseFixture()
	readback := nomad.DeploymentJobHealthObservation{
		State: nomad.DeploymentJobHealthPending, JobVersion: intent.TargetJobVersion,
		JobModifyIndex: intent.TargetJobModifyIndex,
		Allocations: []nomad.DeploymentJobAllocationHealthObservation{{
			AllocationID: "target-a", NodeID: "node-a", JobID: intent.TargetJobID, Namespace: intent.Namespace,
			TaskGroup: intent.Process, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
			ImageDigest: intent.TargetImageDigest, Healthy: false,
		}},
	}
	allocations, err := intent.TargetAllocationObservations(readback)
	if err != nil || len(allocations) != 1 || allocations[0].Healthy || allocations[0].JobModifyIndex != intent.TargetJobModifyIndex {
		t.Fatalf("pending Nomad target observation=%+v err=%v", allocations, err)
	}
	readback.Allocations[0].Healthy = true
	if _, err := intent.TargetAllocationObservations(readback); err == nil {
		t.Fatal("aggregate and per-allocation health disagreement accepted")
	}
	readback.AllocationIDs = []string{"target-a"}
	readback.State = nomad.DeploymentJobHealthReady
	if _, err := intent.TargetAllocationObservations(readback); err == nil {
		t.Fatal("ready aggregate with fewer than desired target allocations accepted")
	}
	readback.State = nomad.DeploymentJobHealthPending
	readback.AllocationIDs = nil
	readback.Allocations[0].Healthy = false
	readback.JobModifyIndex++
	if _, err := intent.TargetAllocationObservations(readback); err == nil {
		t.Fatal("allocation readback from a different job modify index accepted")
	}
	readback.JobModifyIndex = intent.TargetJobModifyIndex
	readback.Allocations[0].ImageDigest = intent.OldImageDigest
	if _, err := intent.TargetAllocationObservations(readback); err == nil {
		t.Fatal("allocation readback with old image accepted as target")
	}
	readback.Allocations[0].ImageDigest = intent.TargetImageDigest
	readback.State = nomad.DeploymentJobHealthIndeterminate
	if _, err := intent.TargetAllocationObservations(readback); err == nil {
		t.Fatal("indeterminate Nomad provenance accepted")
	}
}

func TestWorkloadReleaseReceiptBindsOnlyValidatedNomadReadback(t *testing.T) {
	intent := workloadReleaseFixture()
	receipt := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseWarming}
	readback := nomad.DeploymentJobHealthObservation{
		State: nomad.DeploymentJobHealthPending, JobVersion: intent.TargetJobVersion,
		JobModifyIndex: intent.TargetJobModifyIndex,
		Allocations: []nomad.DeploymentJobAllocationHealthObservation{{
			AllocationID: "target-a", NodeID: "node-a", JobID: intent.TargetJobID, Namespace: intent.Namespace,
			TaskGroup: intent.Process, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
			ImageDigest: intent.TargetImageDigest,
		}},
	}

	bound, err := receipt.WithTargetAllocationReadback(intent, readback)
	if err != nil || len(bound.TargetAllocations) != 1 || bound.TargetAllocations[0].Healthy {
		t.Fatalf("pending partial placement was not retained as validated evidence: receipt=%+v err=%v", bound, err)
	}
	if err := bound.ValidateSuccessor(intent, receipt); err != nil {
		t.Fatalf("valid partial placement could not advance warming evidence: %v", err)
	}

	readback.Allocations = append(readback.Allocations, readback.Allocations[0])
	if _, err := receipt.WithTargetAllocationReadback(intent, readback); err == nil {
		t.Fatal("duplicate target allocation identity escaped receipt binding")
	}
	readback.Allocations = readback.Allocations[:1]
	readback.Allocations[0].AllocationID = ""
	if _, err := receipt.WithTargetAllocationReadback(intent, readback); err == nil {
		t.Fatal("empty target allocation identity escaped receipt binding")
	}
}

func TestWorkloadReleaseBindsOldDeploymentStopAndDrainReadback(t *testing.T) {
	intent := workloadReleaseFixture()
	request, err := intent.OldDeploymentStopRequest("stop-effect-1")
	if err != nil || request.JobID != intent.OldJobID || request.Region != intent.NomadRegion || request.JobDigest != intent.OldJobDigest || request.StopOperationID != "stop-effect-1" ||
		request.OriginalJobVersion != intent.OldJobVersion || request.OriginalJobModifyIndex != intent.OldJobModifyIndex || len(request.AllocationIDs) != len(intent.OldAllocationIDs) {
		t.Fatalf("old stop observer request=%+v err=%v", request, err)
	}
	base := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseDraining,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"), RouteStep: len(intent.RouteSteps) - 1,
		RouteOldWeight: 0, RouteTargetWeight: 100, RouteGeneration: intent.FirstRouteGeneration + uint64(len(intent.RouteSteps)-1),
		RouteSHA256: workloadReleaseRouteDigest(t, intent, len(intent.RouteSteps)-1)}
	readback := nomad.StoppedDeploymentObservation{JobID: intent.OldJobID, DeploymentID: intent.OldDeploymentID,
		JobVersion: intent.OldJobVersion + 1, JobModifyIndex: intent.OldJobModifyIndex + 2,
		DrainedAllocationIDs: []string{"old-b", "old-a"}}
	stoppedVersion, stoppedModifyIndex := intent.OldJobVersion+1, intent.OldJobModifyIndex+2
	bound, err := base.WithOldDeploymentDrainReadback(intent, stoppedVersion, stoppedModifyIndex, readback)
	if err != nil || len(bound.DrainedOldIDs) != 2 || bound.OldJobStoppedVersion != intent.OldJobVersion+1 {
		t.Fatalf("verified old drain readback failed to bind: receipt=%+v err=%v", bound, err)
	}
	wrongIdentity := readback
	wrongIdentity.JobID = intent.TargetJobID
	if _, err := base.WithOldDeploymentDrainReadback(intent, stoppedVersion, stoppedModifyIndex, wrongIdentity); err == nil {
		t.Fatal("drain readback from the target Nomad job was accepted")
	}
	wrongIdentity = readback
	wrongIdentity.JobModifyIndex++
	if _, err := base.WithOldDeploymentDrainReadback(intent, stoppedVersion, stoppedModifyIndex, wrongIdentity); err == nil {
		t.Fatal("drain readback from a different post-stop revision was accepted")
	}
	if _, err := base.WithOldDeploymentDrainReadback(intent, stoppedVersion, stoppedModifyIndex+1, readback); err == nil {
		t.Fatal("drain readback not matching stop-effect receipt was accepted")
	}
	readback.DrainedAllocationIDs = []string{"old-a"}
	if _, err := base.WithOldDeploymentDrainReadback(intent, stoppedVersion, stoppedModifyIndex, readback); err == nil {
		t.Fatal("partial old allocation terminal readback was bound as a complete drain")
	}
}

func TestWorkloadReleaseReceiptRecoveryRejectsUnsafeProgress(t *testing.T) {
	intent := workloadReleaseFixture()
	initial := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleasePlanned}
	if err := initial.ValidateAgainst(intent); err != nil {
		t.Fatalf("initial receipt rejected: %v", err)
	}

	// A durable receipt survives process restart byte-for-byte and can be
	// validated against the same immutable intent before recovery continues.
	receipt := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseDraining,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"),
		RouteGeneration:   intent.FirstRouteGeneration + uint64(len(intent.RouteSteps)-1),
		RouteStep:         len(intent.RouteSteps) - 1, RouteOldWeight: 0, RouteTargetWeight: 100,
		RouteSHA256: workloadReleaseRouteDigest(t, intent, len(intent.RouteSteps)-1), DrainedOldIDs: []string{"old-a"}}
	withOldJobStopEvidence(&receipt, intent)
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var recovered WorkloadReleaseReceipt
	if err := json.Unmarshal(encoded, &recovered); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(receipt, recovered) {
		t.Fatalf("receipt changed over recovery round trip: got %+v, want %+v", recovered, receipt)
	}
	if err := recovered.ValidateAgainst(intent); err != nil {
		t.Fatalf("same-intent recovery receipt rejected: %v", err)
	}

	unsafe := recovered
	unsafe.RouteGeneration = 0
	unsafe.RouteSHA256 = ""
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("drain progress without route readback accepted")
	}
	unsafe = recovered
	unsafe.DrainedOldIDs = []string{"unlisted-allocation"}
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("drain acknowledgement for an allocation outside the intent accepted")
	}
	unsafe = recovered
	unsafe.DrainedOldIDs = []string{"old-a", "old-a"}
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("duplicate drain acknowledgement accepted")
	}
	unsafe = recovered
	unsafe.IntentID = "different-intent"
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("receipt from a different intent accepted")
	}
	unsafe = recovered
	unsafe.TargetAllocations = targetAllocationObservations("old-a", "target-b")
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("target generation reused an old allocation identity")
	}
	unsafe = recovered
	unsafe.RouteTargetWeight = 70
	unsafe.RouteOldWeight = 30
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("route readback weights not admitted by the intent accepted")
	}
	unsafe = recovered
	unsafe.TargetAllocations[0].Healthy = false
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("old allocation drain before all targets healthy accepted")
	}
	unsafe = recovered
	unsafe.TargetAllocations = targetAllocationObservations("target-a", "target-b", "target-c", "target-d")
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("target allocation count above desired plus surge accepted")
	}
	unsafe = recovered
	unsafe.TargetAllocations[0].JobVersion++
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("allocation from another Nomad job version accepted")
	}
	unsafe = recovered
	unsafe.TargetAllocations[0].ImageDigest = intent.OldImageDigest
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("allocation with the old image digest accepted as target")
	}
	unsafe = recovered
	unsafe.TargetAllocations[0].JobID = "other-job"
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("allocation from another Nomad job accepted")
	}
	unsafe = recovered
	unsafe.TargetAllocations[0].JobID = intent.App
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("app name substituted for deployment-specific Nomad job ID")
	}
	unsafe = recovered
	unsafe.TargetAllocations[1].NodeID = unsafe.TargetAllocations[0].NodeID
	if err := unsafe.ValidateAgainst(intent); err == nil {
		t.Fatal("duplicate target node placement accepted")
	}
}

func TestWorkloadReleasePhaseTransitionsRequireEvidence(t *testing.T) {
	intent := workloadReleaseFixture()
	planned := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleasePlanned}
	warming := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseWarming}
	if err := warming.ValidateSuccessor(intent, planned); err != nil {
		t.Fatalf("empty warming checkpoint rejected: %v", err)
	}
	ready := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseWarming,
		TargetAllocations: targetAllocationObservations("target-a", "target-b")}
	firstRoute := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseRouting,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"),
		RouteStep:         0, RouteOldWeight: 100, RouteTargetWeight: 0, RouteGeneration: intent.FirstRouteGeneration,
		RouteSHA256: workloadReleaseRouteDigest(t, intent, 0)}
	if err := firstRoute.ValidateSuccessor(intent, ready); err != nil {
		t.Fatalf("healthy target could not enter route phase: %v", err)
	}
	partialReady := ready
	partialReady.TargetAllocations = targetAllocationObservations("target-a")
	partialFirstRoute := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseRouting,
		TargetAllocations: targetAllocationObservations("target-a"),
		RouteStep:         0, RouteOldWeight: 100, RouteTargetWeight: 0, RouteGeneration: intent.FirstRouteGeneration,
		RouteSHA256: workloadReleaseRouteDigest(t, intent, 0)}
	if err := partialFirstRoute.ValidateSuccessor(intent, partialReady); err != nil {
		t.Fatalf("one healthy target could not retain the old-only route: %v", err)
	}
	partialMixed := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseRouting,
		TargetAllocations: targetAllocationObservations("target-a"),
		RouteStep:         1, RouteOldWeight: 50, RouteTargetWeight: 50, RouteGeneration: intent.FirstRouteGeneration + 1,
		RouteSHA256: workloadReleaseRouteDigest(t, intent, 1)}
	if err := partialMixed.ValidateSuccessor(intent, partialFirstRoute); err != nil {
		t.Fatalf("partial healthy target could not enter mixed canary route: %v", err)
	}
	partialDrain := partialMixed
	partialDrain.Phase = WorkloadReleaseDraining
	partialDrain.DrainedOldIDs = []string{"old-a"}
	if err := partialDrain.ValidateSuccessor(intent, partialMixed); err == nil {
		t.Fatal("old allocation drained before full target replica health")
	}
	mixed := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseRouting,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"),
		RouteStep:         1, RouteOldWeight: 50, RouteTargetWeight: 50, RouteGeneration: intent.FirstRouteGeneration + 1,
		RouteSHA256: workloadReleaseRouteDigest(t, intent, 1)}
	if err := mixed.ValidateSuccessor(intent, firstRoute); err != nil {
		t.Fatalf("forward mixed route step rejected: %v", err)
	}
	draining := mixed
	draining.Phase = WorkloadReleaseDraining
	draining.RouteStep = len(intent.RouteSteps) - 1
	draining.RouteOldWeight, draining.RouteTargetWeight = 0, 100
	draining.RouteGeneration = intent.FirstRouteGeneration + uint64(draining.RouteStep)
	draining.RouteSHA256 = workloadReleaseRouteDigest(t, intent, draining.RouteStep)
	if err := draining.ValidateSuccessor(intent, mixed); err != nil {
		t.Fatalf("target-only route could not enter draining: %v", err)
	}
	complete := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseComplete,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"), RouteStep: len(intent.RouteSteps) - 1,
		RouteOldWeight: 0, RouteTargetWeight: 100, RouteGeneration: intent.FirstRouteGeneration + uint64(len(intent.RouteSteps)-1),
		RouteSHA256: workloadReleaseRouteDigest(t, intent, len(intent.RouteSteps)-1), DrainedOldIDs: append([]string(nil), intent.OldAllocationIDs...)}
	withOldJobStopEvidence(&complete, intent)
	if err := complete.ValidateAgainst(intent); err != nil {
		t.Fatalf("terminal fixture is invalid: %v", err)
	}
	incomplete := complete
	incomplete.DrainedOldIDs = []string{"old-a"}
	if err := incomplete.ValidateSuccessor(intent, draining); err == nil {
		t.Fatal("draining receipt completed without all old allocation drain evidence")
	}
	finalRoute := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseDraining,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"),
		RouteStep:         len(intent.RouteSteps) - 1, RouteOldWeight: 0, RouteTargetWeight: 100,
		RouteGeneration: intent.FirstRouteGeneration + uint64(len(intent.RouteSteps)-1),
		RouteSHA256:     workloadReleaseRouteDigest(t, intent, len(intent.RouteSteps)-1),
		DrainedOldIDs:   append([]string(nil), intent.OldAllocationIDs...)}
	withOldJobStopEvidence(&finalRoute, intent)
	if err := complete.ValidateSuccessor(intent, finalRoute); err != nil {
		t.Fatalf("fully drained terminal route could not complete: %v", err)
	}
	if err := complete.ValidateSuccessor(intent, planned); err == nil {
		t.Fatal("planned receipt skipped directly to complete")
	}

	held := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseHeld, HeldReason: "synthetic controller interruption"}
	if err := held.ValidateSuccessor(intent, planned); err != nil {
		t.Fatalf("explicit hold from planned state rejected: %v", err)
	}
	if err := planned.ValidateSuccessor(intent, held); err == nil {
		t.Fatal("terminal held receipt resumed under the same intent")
	}
}

func TestWorkloadReleaseReceiptDigestIsOrderStableAndProgressSensitive(t *testing.T) {
	intent := workloadReleaseFixture()
	receipt := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseWarming,
		TargetAllocations: targetAllocationObservations("target-b", "target-a")}
	first, err := receipt.CanonicalDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	permuted := receipt
	permuted.TargetAllocations = targetAllocationObservations("target-a", "target-b")
	second, err := permuted.CanonicalDigest(intent)
	if err != nil || first != second {
		t.Fatalf("same receipt set produced different digest: %q, %q, err=%v", first, second, err)
	}
	permuted.Phase = WorkloadReleaseHeld
	permuted.HeldReason = "synthetic hold"
	heldDigest, err := permuted.CanonicalDigest(intent)
	if err != nil || first == heldDigest {
		t.Fatalf("receipt phase change failed to change digest: planned=%q held=%q err=%v", first, heldDigest, err)
	}
	progressed := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseRouting,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"),
		RouteStep:         1, RouteOldWeight: 50, RouteTargetWeight: 50, RouteGeneration: 12,
		RouteSHA256: workloadReleaseRouteDigest(t, intent, 1)}
	third, err := progressed.CanonicalDigest(intent)
	if err != nil || first == third {
		t.Fatalf("receipt change failed to change digest: old=%q new=%q err=%v", first, third, err)
	}
}

func TestWorkloadReleaseCompletionRequiresTerminalEvidence(t *testing.T) {
	intent := workloadReleaseFixture()
	receipt := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseComplete,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"), RouteStep: 2,
		RouteOldWeight: 0, RouteTargetWeight: 100, RouteGeneration: 13,
		RouteSHA256: workloadReleaseRouteDigest(t, intent, 2), DrainedOldIDs: []string{"old-a", "old-b"}}
	withOldJobStopEvidence(&receipt, intent)
	if err := receipt.ValidateAgainst(intent); err != nil {
		t.Fatalf("complete evidence rejected: %v", err)
	}
	receipt.DrainedOldIDs = []string{"old-a"}
	if err := receipt.ValidateAgainst(intent); err == nil {
		t.Fatal("terminal receipt with an old allocation not drained accepted")
	}
}

func TestWorkloadReleaseSuccessorRequiresMonotoneSingleStepProgress(t *testing.T) {
	intent := workloadReleaseFixture()
	current := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseWarming,
		TargetAllocations: targetAllocationObservations("target-a", "target-b")}
	firstRoute := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseRouting,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"),
		RouteStep:         0, RouteOldWeight: 100, RouteTargetWeight: 0, RouteGeneration: 11,
		RouteSHA256: workloadReleaseRouteDigest(t, intent, 0)}
	if err := firstRoute.ValidateSuccessor(intent, current); err != nil {
		t.Fatalf("first route publication rejected: %v", err)
	}
	mixed := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseRouting,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"),
		RouteStep:         1, RouteOldWeight: 50, RouteTargetWeight: 50, RouteGeneration: 12,
		RouteSHA256: workloadReleaseRouteDigest(t, intent, 1)}
	if err := mixed.ValidateSuccessor(intent, firstRoute); err != nil {
		t.Fatalf("single mixed route step rejected: %v", err)
	}
	unsafe := mixed
	unsafe.RouteStep = 2
	unsafe.RouteOldWeight = 0
	unsafe.RouteTargetWeight = 100
	unsafe.RouteGeneration = 13
	if err := unsafe.ValidateSuccessor(intent, firstRoute); err == nil {
		t.Fatal("skipped route step accepted")
	}
	unsafe = firstRoute
	unsafe.Phase = WorkloadReleaseWarming
	if err := unsafe.ValidateSuccessor(intent, mixed); err == nil {
		t.Fatal("backward route or phase progress accepted")
	}
	held := mixed
	held.Phase = WorkloadReleaseHeld
	held.HeldReason = "route readback unavailable"
	if err := held.ValidateSuccessor(intent, mixed); err != nil {
		t.Fatalf("safe hold from a valid receipt rejected: %v", err)
	}
	if err := mixed.ValidateSuccessor(intent, held); err == nil {
		t.Fatal("held terminal receipt resumed without a new intent")
	}
	healthLost := held
	healthLost.HeldReason = "target health lost after route publication"
	healthLost.TargetAllocations = targetAllocationObservations("target-a", "target-b")
	healthLost.TargetAllocations[0].Healthy = false
	healthLost.TargetAllocations[1].Healthy = false
	if err := healthLost.ValidateSuccessor(intent, mixed); err != nil {
		t.Fatalf("held receipt did not preserve unhealthy target readback: %v", err)
	}
	withDrain := mixed
	withDrain.Phase = WorkloadReleaseDraining
	withDrain.RouteStep = len(intent.RouteSteps) - 1
	withDrain.RouteOldWeight, withDrain.RouteTargetWeight = 0, 100
	withDrain.RouteGeneration = intent.FirstRouteGeneration + uint64(withDrain.RouteStep)
	withDrain.RouteSHA256 = workloadReleaseRouteDigest(t, intent, withDrain.RouteStep)
	withDrain.DrainedOldIDs = []string{"old-a"}
	withOldJobStopEvidence(&withDrain, intent)
	if err := withDrain.ValidateAgainst(intent); err != nil {
		t.Fatalf("valid drained progress rejected: %v", err)
	}
	unsafeHold := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseHeld, HeldReason: "lost local receipt"}
	if err := unsafeHold.ValidateSuccessor(intent, withDrain); err == nil {
		t.Fatal("hold that erased durable drain progress accepted")
	}
}

func TestWorkloadReleaseIntentRejectsUnsafeRoutePlans(t *testing.T) {
	tests := map[string]func(*WorkloadReleaseIntent){
		"missing old job identity": func(intent *WorkloadReleaseIntent) {
			intent.OldJobID = ""
		},
		"missing old job digest": func(intent *WorkloadReleaseIntent) {
			intent.OldJobDigest = ""
		},
		"missing old spec digest": func(intent *WorkloadReleaseIntent) {
			intent.OldSpecDigest = ""
		},
		"missing target job identity": func(intent *WorkloadReleaseIntent) {
			intent.TargetJobID = ""
		},
		"missing signed traffic probe": func(intent *WorkloadReleaseIntent) {
			intent.TrafficProbePath = ""
		},
		"traffic probe outside public route": func(intent *WorkloadReleaseIntent) {
			intent.TrafficProbePath = "/private"
		},
		"noncanonical traffic body digest": func(intent *WorkloadReleaseIntent) {
			intent.TrafficProbeBodySHA256 = strings.Repeat("A", 64)
		},
		"reused old job identity": func(intent *WorkloadReleaseIntent) {
			intent.TargetJobID = intent.OldJobID
		},
		"missing old job modify index": func(intent *WorkloadReleaseIntent) {
			intent.OldJobModifyIndex = 0
		},
		"no mixed route": func(intent *WorkloadReleaseIntent) {
			intent.RouteSteps = []WorkloadReleaseRouteStep{{OldWeight: 100}, {TargetWeight: 100}}
		},
		"traffic moves backward": func(intent *WorkloadReleaseIntent) {
			intent.RouteSteps = []WorkloadReleaseRouteStep{{OldWeight: 100}, {OldWeight: 30, TargetWeight: 70}, {OldWeight: 50, TargetWeight: 50}, {TargetWeight: 100}}
		},
		"duplicate old-only route step": func(intent *WorkloadReleaseIntent) {
			intent.RouteSteps = []WorkloadReleaseRouteStep{{OldWeight: 100}, {OldWeight: 100}, {OldWeight: 50, TargetWeight: 50}, {TargetWeight: 100}}
		},
		"weight total is ambiguous": func(intent *WorkloadReleaseIntent) {
			intent.RouteSteps[1] = WorkloadReleaseRouteStep{OldWeight: 40, TargetWeight: 40}
		},
		"duplicate old allocation": func(intent *WorkloadReleaseIntent) {
			intent.OldAllocationIDs = []string{"old-a", "old-a"}
		},
		"bad target digest": func(intent *WorkloadReleaseIntent) {
			intent.TargetImageDigest = "mutable-tag"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			intent := workloadReleaseFixture()
			mutate(&intent)
			if err := intent.Validate(); err == nil {
				t.Fatal("unsafe workload release intent accepted")
			}
		})
	}
}

func TestWorkloadReleaseAcceptsFirstNomadJobRevisionZero(t *testing.T) {
	intent := workloadReleaseFixture()
	intent.OldJobVersion = 0
	intent.TargetJobVersion = 0
	if err := intent.Validate(); err != nil {
		t.Fatalf("first Nomad job revision rejected: %v", err)
	}
	allocations := []nomad.DeploymentJobAllocationHealthObservation{
		{AllocationID: "target-a", NodeID: "node-a", JobID: intent.TargetJobID, Namespace: intent.Namespace, TaskGroup: intent.Process, JobVersion: 0, JobModifyIndex: intent.TargetJobModifyIndex, ImageDigest: intent.TargetImageDigest, Healthy: true},
		{AllocationID: "target-b", NodeID: "node-b", JobID: intent.TargetJobID, Namespace: intent.Namespace, TaskGroup: intent.Process, JobVersion: 0, JobModifyIndex: intent.TargetJobModifyIndex, ImageDigest: intent.TargetImageDigest, Healthy: true},
	}
	readback := nomad.DeploymentJobHealthObservation{State: nomad.DeploymentJobHealthReady,
		JobVersion: 0, JobModifyIndex: intent.TargetJobModifyIndex, AllocationIDs: []string{"target-a", "target-b"}, Allocations: allocations}
	if _, err := (WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseWarming}).WithTargetAllocationReadback(intent, readback); err != nil {
		t.Fatalf("first-revision allocation readback rejected: %v", err)
	}
}

func workloadReleaseFixture() WorkloadReleaseIntent {
	return WorkloadReleaseIntent{
		ID: "intent-1", App: "norn-fleet-canary", Region: "nyc3", NomadRegion: "global", Namespace: "default",
		ControlEnvironment: "staging", FleetCluster: "norn-staging", FleetEnvironment: "staging/nyc3", FleetDatacenters: []string{"nyc3"}, FleetTargetGeneration: 3,
		Process: "web", Endpoint: "https://canary.example.test", PublicPaths: []string{"/route-proof", "/version"},
		RetryGETPaths: []string{"/version"}, PrivateReadinessPath: "/readyz", TrafficProbePath: "/route-proof",
		TrafficProbeBodySHA256: strings.Repeat("a", 64),
		SpecDigest:             "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		SourceRevision:         "git:abc123", OldDeploymentID: "deployment-old",
		OldImageDigest: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		OldSpecDigest:  "sha256:4444444444444444444444444444444444444444444444444444444444444444",
		OldJobID:       "norn-fleet-canary-old", OldJobDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OldJobVersion: 7, OldJobModifyIndex: 41,
		TargetDeploymentID: "deployment-new",
		TargetImage:        "registry.example.test/norn@sha256:3333333333333333333333333333333333333333333333333333333333333333",
		TargetImageDigest:  "sha256:3333333333333333333333333333333333333333333333333333333333333333",
		TargetJobID:        "norn-fleet-canary-new",
		TargetJobDigest:    "sha256:" + strings.Repeat("4", 64),
		TargetSpecDigest:   "sha256:" + strings.Repeat("5", 64),
		TargetOperationID:  "operation-target",
		TargetExecutionID:  "nomad-deployment-target",
		OldAllocationIDs:   []string{"old-a", "old-b"}, TargetReplicas: 2, SurgeLimit: 1,
		TargetJobVersion:     8,
		TargetJobModifyIndex: 43,
		FirstRouteGeneration: 11,
		RouteSteps:           []WorkloadReleaseRouteStep{{OldWeight: 100}, {OldWeight: 50, TargetWeight: 50}, {TargetWeight: 100}},
	}
}

func targetAllocationObservations(ids ...string) []WorkloadReleaseAllocationObservation {
	observations := make([]WorkloadReleaseAllocationObservation, 0, len(ids))
	for _, id := range ids {
		observations = append(observations, WorkloadReleaseAllocationObservation{
			AllocationID: id, NodeID: "node-" + id, JobID: "norn-fleet-canary-new", Namespace: "default",
			TaskGroup: "web", JobVersion: 8, JobModifyIndex: 43,
			ImageDigest: "sha256:3333333333333333333333333333333333333333333333333333333333333333", Healthy: true,
		})
	}
	return observations
}

func withOldJobStopEvidence(receipt *WorkloadReleaseReceipt, intent WorkloadReleaseIntent) {
	receipt.OldJobStoppedVersion = intent.OldJobVersion + 1
	receipt.OldJobStoppedModifyIndex = intent.OldJobModifyIndex + 2
}

func workloadReleaseRouteDigest(t *testing.T, intent WorkloadReleaseIntent, step int) string {
	t.Helper()
	route, err := intent.RenderRouteStep(step)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("sha256:%s", route.SHA256)
}
