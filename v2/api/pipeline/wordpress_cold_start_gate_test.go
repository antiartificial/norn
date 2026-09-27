package pipeline

import (
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/store"
)

func TestActiveWordPressAllocationsTreatsOnlyTerminalStatesAsInactive(t *testing.T) {
	allocations := []*nomadapi.AllocationListStub{
		{ID: "running", ClientStatus: "running"},
		{ID: "pending", ClientStatus: "pending"},
		{ID: "done", ClientStatus: "complete"},
		{ID: "failed", ClientStatus: "failed"},
	}
	active := activeWordPressAllocations(allocations)
	if len(active) != 2 || active[0].ID != "running" || active[1].ID != "pending" {
		t.Fatalf("active allocations = %#v", active)
	}
	if len(activeWordPressAllocations([]*nomadapi.AllocationListStub{nil})) != 1 {
		t.Fatal("malformed Nomad allocation was treated as an empty cold start")
	}
}

func TestWordPressColdStartAllocationMatchesSubmittedEvaluation(t *testing.T) {
	allocation := &nomadapi.AllocationListStub{ID: "alloc-1", JobID: "wordpress", EvalID: "eval-1"}
	if !matchesWordPressColdStartAllocation(allocation, "wordpress", "eval-1") {
		t.Fatal("submitted evaluation allocation was rejected")
	}
	for _, other := range []*nomadapi.AllocationListStub{
		{ID: "alloc-1", JobID: "wordpress", EvalID: "eval-other"},
		{ID: "alloc-1", JobID: "other", EvalID: "eval-1"},
		{JobID: "wordpress", EvalID: "eval-1"},
	} {
		if matchesWordPressColdStartAllocation(other, "wordpress", "eval-1") {
			t.Fatalf("unrelated allocation was accepted: %#v", other)
		}
	}
}

func TestWordPressColdStartReservationIDBindsClaimDeploymentAndSpec(t *testing.T) {
	claim, err := store.NewOperationClaim("op-1", "worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	otherClaim, err := store.NewOperationClaim("op-2", "worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	base := &state{claim: claim, deploymentID: "deployment-1", operationPayload: map[string]interface{}{"specDigest": "spec-a"}}
	first := wordpressColdStartReservationID(base)
	if first == wordpressColdStartReservationID(&state{claim: otherClaim, deploymentID: "deployment-1", operationPayload: map[string]interface{}{"specDigest": "spec-a"}}) {
		t.Fatal("operation ID was not bound into reservation ID")
	}
	if first == wordpressColdStartReservationID(&state{claim: base.claim, deploymentID: "deployment-2", operationPayload: map[string]interface{}{"specDigest": "spec-a"}}) {
		t.Fatal("deployment ID was not bound into reservation ID")
	}
	if first == wordpressColdStartReservationID(&state{claim: base.claim, deploymentID: base.deploymentID, operationPayload: map[string]interface{}{"specDigest": "spec-b"}}) {
		t.Fatal("spec digest was not bound into reservation ID")
	}
}

func TestWordPressColdStartGateDefaultsOff(t *testing.T) {
	p := &Pipeline{}
	if p.WPColdStartGate {
		t.Fatal("cold-start gate unexpectedly defaults on")
	}
}
