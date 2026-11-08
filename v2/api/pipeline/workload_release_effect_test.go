package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/effect"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

type workloadReleaseEffectStore struct {
	record        effect.Record
	has           bool
	receiptRecord *store.WorkloadReleaseReceiptRecord
}

func (s *workloadReleaseEffectStore) LoadWorkloadReleaseReceipt(_ context.Context, intentID string) (*store.WorkloadReleaseReceiptRecord, error) {
	if s.receiptRecord == nil || s.receiptRecord.IntentID != intentID {
		return nil, nil
	}
	copy := *s.receiptRecord
	copy.Receipt = append(json.RawMessage(nil), s.receiptRecord.Receipt...)
	return &copy, nil
}

func (s *workloadReleaseEffectStore) CompareAndSwapWorkloadReleaseReceipt(_ context.Context, claim store.OperationClaim, intentID, expected string, receipt json.RawMessage) (store.WorkloadReleaseReceiptRecord, error) {
	if s.receiptRecord == nil {
		if expected != "" {
			return store.WorkloadReleaseReceiptRecord{}, store.ErrWorkloadReleaseReceiptConflict
		}
		s.receiptRecord = &store.WorkloadReleaseReceiptRecord{IntentID: intentID, OperationID: claim.OperationID(), ClaimGeneration: claim.Generation(), Revision: 1, Receipt: append(json.RawMessage(nil), receipt...), ReceiptDigest: workloadReceiptDigest(receipt)}
	} else {
		if s.receiptRecord.IntentID != intentID || s.receiptRecord.OperationID != claim.OperationID() || s.receiptRecord.ReceiptDigest != expected {
			return store.WorkloadReleaseReceiptRecord{}, store.ErrWorkloadReleaseReceiptConflict
		}
		s.receiptRecord.Receipt = append(json.RawMessage(nil), receipt...)
		s.receiptRecord.ReceiptDigest = workloadReceiptDigest(receipt)
		s.receiptRecord.ClaimGeneration = claim.Generation()
		s.receiptRecord.Revision++
	}
	return *s.receiptRecord, nil
}

func TestWorkloadReleaseStopRequiresConfiguredDurableBoundary(t *testing.T) {
	client := &nomad.Client{}
	if _, err := NewWorkloadReleaseStopEffectsWithStore(nil, nil, client); err == nil {
		t.Fatal("accepted workload release stop without an effect store")
	}
	var typedNil *store.PGEffectStore
	if _, err := NewWorkloadReleaseStopEffectsWithStore(typedNil, nil, client); err == nil {
		t.Fatal("accepted typed-nil workload release effect store")
	}
	if (&WorkloadReleaseStopEffects{}).Available() {
		t.Fatal("reported workload release stop available without durable executor")
	}
}

func TestWorkloadReleaseStopExecutionMarkerSurvivesClaimRenewal(t *testing.T) {
	intent := workloadReleaseFixture()
	payload, err := json.Marshal(workloadReleaseStopDescriptor{Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := store.NewOperationClaim("release-operation", "worker-a", 3)
	second, _ := store.NewOperationClaim("release-operation", "worker-b", 4)
	reservation := effect.Reservation{Authority: "authority-1", Resource: workloadReleaseStopResource(intent),
		Stage: workloadReleaseStopStage, Supervisor: "nomad-workload-release-stop", LaunchPayload: payload}
	reservation.OperationClaim = effect.OperationClaim{OperationID: first.OperationID(), OwnerID: first.OwnerID(), Generation: first.Generation()}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	one := workloadReleaseStopExecutionID(reservation)
	reservation.OperationClaim = effect.OperationClaim{OperationID: second.OperationID(), OwnerID: second.OwnerID(), Generation: second.Generation()}
	if got := workloadReleaseStopExecutionID(reservation); got != one {
		t.Fatalf("claim renewal changed same-intent Nomad stop marker: %s != %s", got, one)
	}
}

type workloadReleaseTargetObserverFunc func(context.Context, nomad.CASDeploymentJobRequest) (nomad.DeploymentJobHealthObservation, error)

func (f workloadReleaseTargetObserverFunc) ObserveDeploymentJobHealth(ctx context.Context, request nomad.CASDeploymentJobRequest) (nomad.DeploymentJobHealthObservation, error) {
	return f(ctx, request)
}

func TestWorkloadReleaseTargetHealthControllerPersistsExactReadback(t *testing.T) {
	intent := workloadReleaseFixture()
	receiptStore := &workloadReleaseEffectStore{}
	claim, err := store.NewOperationClaim("release-operation", "worker", 3)
	if err != nil {
		t.Fatal(err)
	}
	repository := &WorkloadReleaseReceiptRepository{store: receiptStore}
	if _, err := repository.Advance(context.Background(), claim, intent, "", WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleasePlanned}); err != nil {
		t.Fatal(err)
	}
	planned := receiptStore.receiptRecord
	if _, err := repository.Advance(context.Background(), claim, intent, planned.ReceiptDigest, WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseWarming}); err != nil {
		t.Fatal(err)
	}
	var seen nomad.CASDeploymentJobRequest
	observer := workloadReleaseTargetObserverFunc(func(_ context.Context, request nomad.CASDeploymentJobRequest) (nomad.DeploymentJobHealthObservation, error) {
		seen = request
		return nomad.DeploymentJobHealthObservation{State: nomad.DeploymentJobHealthPending, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
			Allocations: []nomad.DeploymentJobAllocationHealthObservation{{AllocationID: "target-a", NodeID: "node-a", JobID: intent.TargetJobID,
				Namespace: intent.Namespace, TaskGroup: intent.Process, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
				ImageDigest: intent.TargetImageDigest, Healthy: false}}}, nil
	})
	controller, err := NewWorkloadReleaseTargetHealthController(receiptStore, observer)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := controller.ObserveTargetHealth(context.Background(), claim, intent)
	if err != nil {
		t.Fatal(err)
	}
	if seen.EffectiveJobID() != intent.TargetJobID || seen.Region != intent.NomadRegion || seen.PlacementRegion != "" || seen.DeploymentID != intent.TargetDeploymentID || seen.SpecDigest != intent.TargetSpecDigest ||
		seen.OperationID != intent.TargetOperationID || seen.ExecutionID != intent.TargetExecutionID || seen.JobDigest != strings.TrimPrefix(intent.TargetJobDigest, "sha256:") || seen.ImageTag != intent.TargetImage {
		t.Fatalf("Nomad observer request was not pinned to target provenance: %+v", seen)
	}
	var persisted WorkloadReleaseReceipt
	if err := json.Unmarshal(updated.Receipt, &persisted); err != nil || persisted.Phase != WorkloadReleaseWarming || persisted.RouteGeneration != 0 || len(persisted.TargetAllocations) != 1 || persisted.TargetAllocations[0].Healthy {
		t.Fatalf("pending target evidence was not persisted as warming: %+v err=%v", persisted, err)
	}
}

func TestWorkloadReleaseTargetHealthRefreshesMixedRouteAndHoldsTotalLoss(t *testing.T) {
	intent := workloadReleaseFixture()
	claim, _ := store.NewOperationClaim("release-operation", "worker", 3)
	for _, loseAll := range []bool{false, true} {
		receiptStore := &workloadReleaseEffectStore{}
		repository := &WorkloadReleaseReceiptRepository{store: receiptStore}
		planned, err := repository.Advance(context.Background(), claim, intent, "", WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleasePlanned})
		if err != nil {
			t.Fatal(err)
		}
		warming, err := repository.Advance(context.Background(), claim, intent, planned.ReceiptDigest, WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseWarming})
		if err != nil {
			t.Fatal(err)
		}
		routing := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseRouting, TargetAllocations: targetAllocationObservations("target-a", "target-b"),
			RouteStep: 0, RouteOldWeight: 100, RouteGeneration: intent.FirstRouteGeneration, RouteSHA256: workloadReleaseRouteDigest(t, intent, 0)}
		routed, err := repository.Advance(context.Background(), claim, intent, warming.ReceiptDigest, routing)
		if err != nil {
			t.Fatal(err)
		}
		mixed := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseDraining, TargetAllocations: targetAllocationObservations("target-a", "target-b"),
			RouteStep: 1, RouteOldWeight: 50, RouteTargetWeight: 50, RouteGeneration: intent.FirstRouteGeneration + 1, RouteSHA256: workloadReleaseRouteDigest(t, intent, 1)}
		if _, err := repository.Advance(context.Background(), claim, intent, routed.ReceiptDigest, mixed); err != nil {
			t.Fatal(err)
		}
		allocations := make([]nomad.DeploymentJobAllocationHealthObservation, 0, 2)
		ids := []string{"target-a", "target-b"}
		for _, id := range ids {
			allocations = append(allocations, nomad.DeploymentJobAllocationHealthObservation{AllocationID: id, NodeID: "node-" + id,
				JobID: intent.TargetJobID, Namespace: intent.Namespace, TaskGroup: intent.Process,
				JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
				ImageDigest: intent.TargetImageDigest, Healthy: !loseAll})
		}
		observer := workloadReleaseTargetObserverFunc(func(context.Context, nomad.CASDeploymentJobRequest) (nomad.DeploymentJobHealthObservation, error) {
			state := nomad.DeploymentJobHealthReady
			allocationIDs := ids
			if loseAll {
				state, allocationIDs = nomad.DeploymentJobHealthPending, nil
			}
			return nomad.DeploymentJobHealthObservation{State: state, JobVersion: intent.TargetJobVersion,
				JobModifyIndex: intent.TargetJobModifyIndex, AllocationIDs: allocationIDs, Allocations: allocations}, nil
		})
		controller, err := NewWorkloadReleaseTargetHealthController(receiptStore, observer)
		if err != nil {
			t.Fatal(err)
		}
		updated, err := controller.ObserveTargetHealth(context.Background(), claim, intent)
		if err != nil {
			t.Fatalf("refresh health loseAll=%t: %v", loseAll, err)
		}
		var got WorkloadReleaseReceipt
		if err := json.Unmarshal(updated.Receipt, &got); err != nil {
			t.Fatal(err)
		}
		if loseAll && (got.Phase != WorkloadReleaseHeld || got.HeldReason == "") {
			t.Fatalf("total target loss did not durably hold the release: %+v", got)
		}
		if !loseAll && (got.Phase != WorkloadReleaseDraining || got.RouteGeneration != intent.FirstRouteGeneration+1 || len(got.TargetAllocations) != 2) {
			t.Fatalf("healthy mixed-route refresh regressed progress: %+v", got)
		}
	}
}

func TestWorkloadReleaseTargetHealthControllerRejectsWrongClaimPhaseAndReadback(t *testing.T) {
	intent := workloadReleaseFixture()
	claim, _ := store.NewOperationClaim("release-operation", "worker", 3)
	for _, phase := range []string{WorkloadReleasePlanned, WorkloadReleaseRouting} {
		receiptStore := &workloadReleaseEffectStore{}
		repository := &WorkloadReleaseReceiptRepository{store: receiptStore}
		if _, err := repository.Advance(context.Background(), claim, intent, "", WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleasePlanned}); err != nil {
			t.Fatal(err)
		}
		planned := receiptStore.receiptRecord
		if phase == WorkloadReleaseWarming {
			_, _ = repository.Advance(context.Background(), claim, intent, planned.ReceiptDigest, WorkloadReleaseReceipt{IntentID: intent.ID, Phase: phase})
		} else if phase == WorkloadReleaseRouting {
			routing := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: phase, TargetAllocations: targetAllocationObservations("target-a", "target-b"), RouteStep: 0,
				RouteOldWeight: 100, RouteGeneration: intent.FirstRouteGeneration, RouteSHA256: workloadReleaseRouteDigest(t, intent, 0)}
			_, _ = repository.Advance(context.Background(), claim, intent, planned.ReceiptDigest, routing)
		}
		controller, _ := NewWorkloadReleaseTargetHealthController(receiptStore, workloadReleaseTargetObserverFunc(func(context.Context, nomad.CASDeploymentJobRequest) (nomad.DeploymentJobHealthObservation, error) {
			return nomad.DeploymentJobHealthObservation{State: nomad.DeploymentJobHealthPending, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex + 1}, nil
		}))
		if _, err := controller.ObserveTargetHealth(context.Background(), claim, intent); err == nil {
			t.Fatalf("phase %q or mismatched target index was accepted", phase)
		}
	}
	wrongClaim, _ := store.NewOperationClaim("different-operation", "worker", 3)
	receiptStore := &workloadReleaseEffectStore{}
	repository := &WorkloadReleaseReceiptRepository{store: receiptStore}
	_, _ = repository.Advance(context.Background(), claim, intent, "", WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleasePlanned})
	planned := receiptStore.receiptRecord
	_, _ = repository.Advance(context.Background(), claim, intent, planned.ReceiptDigest, WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseWarming})
	controller, _ := NewWorkloadReleaseTargetHealthController(receiptStore, workloadReleaseTargetObserverFunc(func(context.Context, nomad.CASDeploymentJobRequest) (nomad.DeploymentJobHealthObservation, error) {
		return nomad.DeploymentJobHealthObservation{State: nomad.DeploymentJobHealthPending, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex}, nil
	}))
	if _, err := controller.ObserveTargetHealth(context.Background(), wrongClaim, intent); err == nil {
		t.Fatal("foreign operation claim was allowed to observe or persist progress")
	}
}

func (s *workloadReleaseEffectStore) Authority(context.Context) (string, error) {
	return "authority-1", nil
}

func (s *workloadReleaseEffectStore) Reserve(_ context.Context, reservation effect.Reservation) (effect.ReservationResult, error) {
	if s.has {
		if s.record.Reservation.Resource != reservation.Resource {
			return effect.ReservationResult{}, effect.ErrResourceBlocked
		}
		return effect.ReservationResult{Record: s.record}, nil
	}
	s.record = effect.Record{Token: effect.Token{EffectID: "effect-1", Generation: 1}, Reservation: reservation, Lifecycle: effect.LifecycleReserved}
	s.has = true
	return effect.ReservationResult{Record: s.record, Created: true}, nil
}

func (s *workloadReleaseEffectStore) MarkLaunched(_ context.Context, token effect.Token, identity effect.ExecutionIdentity) error {
	if !s.has || token != s.record.Token {
		return errors.New("stale effect token")
	}
	s.record.Lifecycle, s.record.Execution = effect.LifecycleLaunched, identity
	return nil
}

func (s *workloadReleaseEffectStore) Complete(_ context.Context, token effect.Token, completion effect.Completion) error {
	if !s.has || token != s.record.Token {
		return errors.New("stale effect token")
	}
	s.record.Lifecycle, s.record.Completion = effect.LifecycleCompleted, &completion
	return nil
}

func (s *workloadReleaseEffectStore) Resolve(_ context.Context, token effect.Token, resolution effect.Resolution) error {
	if !s.has || token != s.record.Token {
		return errors.New("stale effect token")
	}
	s.record.Lifecycle = effect.LifecycleResolved
	return nil
}

func (s *workloadReleaseEffectStore) UnresolvedForResource(_ context.Context, authority, resource string) (effect.Record, bool, error) {
	if s.has && s.record.Reservation.Authority == authority && s.record.Reservation.Resource == resource &&
		(s.record.Lifecycle == effect.LifecycleReserved || s.record.Lifecycle == effect.LifecycleLaunched) {
		return s.record, true, nil
	}
	return effect.Record{}, false, nil
}

func TestWorkloadReleaseStopRecoversLostNomadResponseWithoutSecondStop(t *testing.T) {
	intent := workloadReleaseFixture()
	prerequisite := workloadReleaseDrainPrerequisite(t, intent)
	jobID, region, namespace := intent.OldJobID, intent.NomadRegion, intent.Namespace
	name, specDigest, jobDigest := intent.OldDeploymentID, intent.OldSpecDigest, intent.OldJobDigest
	image := "registry.test/canary@" + intent.OldImageDigest
	version, modifyIndex, count, stopped := intent.OldJobVersion, intent.OldJobModifyIndex, len(intent.OldAllocationIDs), false
	group := intent.Process
	job := &nomadapi.Job{ID: &jobID, Region: &region, Namespace: &namespace, Version: &version, JobModifyIndex: &modifyIndex, Stop: &stopped,
		Meta:       map[string]string{nomad.DeploymentIDMeta: name, nomad.SpecDigestMeta: specDigest, nomad.DeploymentJobDigestMeta: jobDigest},
		TaskGroups: []*nomadapi.TaskGroup{{Name: &group, Count: &count, Tasks: []*nomadapi.Task{{Name: group, Driver: "docker", Config: map[string]interface{}{"image": image}}}}}}
	stubs := make([]*nomadapi.AllocationListStub, 0, len(intent.OldAllocationIDs))
	for _, id := range intent.OldAllocationIDs {
		stubs = append(stubs, &nomadapi.AllocationListStub{ID: id, JobID: jobID, JobVersion: version, TaskGroup: group,
			DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning})
	}
	writes := 0
	writeOperationMarker, writeIntentMarker := "", ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.11.5"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/"+jobID:
			_ = json.NewEncoder(w).Encode(job)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/"+jobID+"/allocations":
			_ = json.NewEncoder(w).Encode(stubs)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
			writes++
			var registration nomadapi.JobRegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
				t.Fatal(err)
			}
			if !registration.EnforceIndex || registration.JobModifyIndex != intent.OldJobModifyIndex || registration.Job == nil ||
				registration.Job.Meta[nomad.DeploymentStopIntentMeta] == "" || registration.Job.Meta[nomad.DeploymentStopOperationMeta] == "" {
				t.Fatalf("stop was not fenced by its revision and intent marker: %+v", registration)
			}
			writeOperationMarker = registration.Job.Meta[nomad.DeploymentStopOperationMeta]
			writeIntentMarker = registration.Job.Meta[nomad.DeploymentStopIntentMeta]
			job = registration.Job
			stopped = true
			job.Stop = &stopped
			version, modifyIndex = intent.OldJobVersion+1, intent.OldJobModifyIndex+2
			job.Version, job.JobModifyIndex = &version, &modifyIndex
			for _, allocation := range stubs {
				allocation.DesiredStatus = nomadapi.AllocDesiredStatusStop
				allocation.ClientStatus = nomadapi.AllocClientStatusComplete
			}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Fatal(err)
			}
			_ = connection.Close() // Nomad committed the stop; the response was lost.
		default:
			t.Errorf("unexpected Nomad request %s %s", r.Method, r.URL)
		}
	}))
	defer server.Close()
	client, err := nomad.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	effectStore := &workloadReleaseEffectStore{}
	effects, err := NewWorkloadReleaseStopEffectsWithStore(effectStore, effectStore, client)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.NewOperationClaim("operation-1", "worker-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := effects.AdvanceReceipt(context.Background(), claim, intent, "", WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleasePlanned}); err != nil {
		t.Fatalf("seed initial durable receipt: %v", err)
	}
	if _, err := effects.AdvanceReceipt(context.Background(), claim, intent, effectStore.receiptRecord.ReceiptDigest, WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseWarming}); err != nil {
		t.Fatalf("advance to warming: %v", err)
	}
	routing := WorkloadReleaseReceipt{IntentID: intent.ID, Phase: WorkloadReleaseRouting,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"),
		RouteStep:         0, RouteOldWeight: 100, RouteTargetWeight: 0,
		RouteGeneration: intent.FirstRouteGeneration, RouteSHA256: workloadReleaseRouteDigest(t, intent, 0)}
	if _, err := effects.AdvanceReceipt(context.Background(), claim, intent, effectStore.receiptRecord.ReceiptDigest, routing); err != nil {
		t.Fatalf("advance to initial route: %v", err)
	}
	if _, err := effects.AdvanceReceipt(context.Background(), claim, intent, effectStore.receiptRecord.ReceiptDigest, prerequisite); err != nil {
		t.Fatalf("persist stop prerequisite receipt: %v", err)
	}
	if _, err := effects.ExecuteStop(context.Background(), claim, intent); err == nil || !effect.IsDeferred(err) {
		t.Fatalf("lost Nomad response should leave a recoverable pending effect, got %v", err)
	}
	if writes != 1 || !effectStore.has || effectStore.record.Lifecycle != effect.LifecycleReserved {
		t.Fatalf("after ambiguous response writes=%d record=%+v", writes, effectStore.record)
	}
	wantIntentDigest, err := intent.CanonicalDigest()
	if err != nil || writeOperationMarker != effectStore.record.Reservation.SupervisorExecutionID || writeIntentMarker != wantIntentDigest {
		t.Fatalf("Nomad stop marker operation=%q intent=%q want operation=%q intent=%q err=%v", writeOperationMarker, writeIntentMarker, effectStore.record.Reservation.SupervisorExecutionID, wantIntentDigest, err)
	}
	result, err := effects.Recover(context.Background(), effectStore.record)
	if err != nil || result.Outcome != effect.OutcomeSucceeded || effectStore.record.Lifecycle != effect.LifecycleCompleted {
		t.Fatalf("same-intent recovery result=%+v record=%+v err=%v", result, effectStore.record, err)
	}
	if writes != 1 {
		t.Fatalf("recovery repeated the Nomad stop %d times", writes)
	}
	var recovered workloadReleaseStopResult
	if err := json.Unmarshal(result.Output, &recovered); err != nil || recovered.StoppedJobVersion != version || recovered.StoppedModifyIndex != modifyIndex ||
		!equalReleaseStrings(sortedReleaseIDs(recovered.DrainedAllocationIDs), sortedReleaseIDs(intent.OldAllocationIDs)) {
		t.Fatalf("recovered stop result=%+v err=%v", recovered, err)
	}
	drained, err := effects.RecordDrainReadback(context.Background(), claim, intent, result)
	if err != nil {
		t.Fatalf("record fresh post-stop Nomad readback: %v", err)
	}
	var drainReceipt WorkloadReleaseReceipt
	if err := json.Unmarshal(drained.Receipt, &drainReceipt); err != nil || !equalReleaseStrings(sortedReleaseIDs(drainReceipt.DrainedOldIDs), sortedReleaseIDs(intent.OldAllocationIDs)) {
		t.Fatalf("exact old allocation drain evidence was not persisted: %+v err=%v", drainReceipt, err)
	}
	finalRoute := drainReceipt
	finalRoute.RouteStep = len(intent.RouteSteps) - 1
	finalRoute.RouteOldWeight = intent.RouteSteps[finalRoute.RouteStep].OldWeight
	finalRoute.RouteTargetWeight = intent.RouteSteps[finalRoute.RouteStep].TargetWeight
	finalRoute.RouteGeneration = intent.FirstRouteGeneration + uint64(finalRoute.RouteStep)
	finalRoute.RouteSHA256 = workloadReleaseRouteDigest(t, intent, finalRoute.RouteStep)
	advanced, err := effects.AdvanceReceipt(context.Background(), claim, intent, drained.ReceiptDigest, finalRoute)
	if err != nil {
		t.Fatalf("persist final verified route receipt: %v", err)
	}
	completed, err := effects.MarkComplete(context.Background(), claim, intent)
	if err != nil {
		t.Fatalf("complete verified workload release: %v", err)
	}
	var terminal WorkloadReleaseReceipt
	if err := json.Unmarshal(completed.Receipt, &terminal); err != nil || terminal.Phase != WorkloadReleaseComplete || advanced.ReceiptDigest == completed.ReceiptDigest {
		t.Fatalf("terminal workload receipt=%+v err=%v", terminal, err)
	}
}

func TestWorkloadReleaseStopRequiresHealthyMixedRouteReceipt(t *testing.T) {
	intent := workloadReleaseFixture()
	effectStore := &workloadReleaseEffectStore{}
	client, err := nomad.NewClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	effects, err := NewWorkloadReleaseStopEffectsWithStore(effectStore, effectStore, client)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.NewOperationClaim("operation-1", "worker-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	for name, receipt := range map[string]WorkloadReleaseReceipt{
		"planned": {IntentID: intent.ID, Phase: WorkloadReleasePlanned, RouteStep: 0},
		"mixed route without healthy targets": {
			IntentID: intent.ID, Phase: WorkloadReleaseDraining, RouteStep: 1, RouteOldWeight: 50, RouteTargetWeight: 50,
			RouteGeneration: intent.FirstRouteGeneration + 1, RouteSHA256: workloadReleaseRouteDigest(t, intent, 1),
		},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, _ := json.Marshal(receipt)
			effectStore.receiptRecord = &store.WorkloadReleaseReceiptRecord{IntentID: intent.ID, OperationID: claim.OperationID(), Receipt: encoded, ReceiptDigest: workloadReceiptDigest(encoded)}
			if _, err := effects.ExecuteStop(context.Background(), claim, intent); err == nil {
				t.Fatal("old deployment stop accepted without full healthy mixed-route proof")
			}
			if effectStore.has {
				t.Fatal("unsafe stop prerequisite reserved a durable effect")
			}
		})
	}
}

func workloadReleaseDrainPrerequisite(t *testing.T, intent WorkloadReleaseIntent) WorkloadReleaseReceipt {
	t.Helper()
	receipt := WorkloadReleaseReceipt{
		IntentID: intent.ID, Phase: WorkloadReleaseDraining,
		TargetAllocations: targetAllocationObservations("target-a", "target-b"),
		RouteStep:         1, RouteOldWeight: 50, RouteTargetWeight: 50,
		RouteGeneration: intent.FirstRouteGeneration + 1, RouteSHA256: workloadReleaseRouteDigest(t, intent, 1),
	}
	if err := validateWorkloadReleaseStopPrerequisite(intent, receipt); err != nil {
		t.Fatalf("test stop prerequisite invalid: %v", err)
	}
	return receipt
}
