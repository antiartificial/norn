package pipeline

import (
	"testing"

	"norn/v2/api/effect"
	"norn/v2/api/model"
)

func TestScaleRequestFromOperationRejectsAmbiguousCounts(t *testing.T) {
	valid := &model.Operation{App: "widget", Payload: map[string]interface{}{"group": "web", "count": 2.0}}
	got, err := scaleRequestFromOperation(valid)
	if err != nil || got != (scaleRequest{App: "widget", Group: "web", Count: 2}) {
		t.Fatalf("valid request = %#v, %v", got, err)
	}
	for _, count := range []interface{}{-1, 1.5, "2"} {
		_, err := scaleRequestFromOperation(&model.Operation{App: "widget", Payload: map[string]interface{}{"group": "web", "count": count}})
		if err == nil {
			t.Fatalf("count %#v was accepted", count)
		}
	}
}

func TestScaleExecutionIdentityIncludesClaimGeneration(t *testing.T) {
	base := effect.Reservation{Authority: "authority", InputDigest: "sha256:input", OperationClaim: effect.OperationClaim{OperationID: "operation", Generation: 1}}
	first := scaleExecutionID(base)
	base.OperationClaim.Generation = 2
	if second := scaleExecutionID(base); first == second {
		t.Fatal("successor claim reused predecessor external execution identity")
	}
}
