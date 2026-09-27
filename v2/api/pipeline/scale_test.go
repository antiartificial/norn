package pipeline

import (
	"context"
	"errors"
	"testing"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestScaleRequestFromOperationRejectsAmbiguousCounts(t *testing.T) {
	valid := &model.Operation{App: "widget", Payload: map[string]interface{}{"group": "web", "region": "us-central", "nomadRegion": "global", "count": 2.0}}
	got, err := scaleRequestFromOperation(valid)
	if err != nil || got != (scaleRequest{App: "widget", Group: "web", Region: "us-central", NomadRegion: "global", Count: 2}) {
		t.Fatalf("valid request = %#v, %v", got, err)
	}
	for _, count := range []interface{}{-1, 1.5, "2"} {
		_, err := scaleRequestFromOperation(&model.Operation{App: "widget", Payload: map[string]interface{}{"group": "web", "region": "us-central", "nomadRegion": "global", "count": count}})
		if err == nil {
			t.Fatalf("count %#v was accepted", count)
		}
	}
}

func TestScaleIntentWriteFailureDefersForCompletedEffectRecovery(t *testing.T) {
	claim, err := store.NewOperationClaim("operation", "worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := &Pipeline{FinishScaleIntent: func(context.Context, store.OperationClaim, string, string, string, int, string, map[string]interface{}) error {
		calls++
		if calls == 1 {
			return errors.New("temporary database outage")
		}
		return nil
	}}
	err = p.finishScaleIntent(context.Background(), claim, "widget", "web", "us-central", 3, "scaled", nil)
	result := deferredResult(claim, &effect.PendingError{EffectID: "completed-effect", Resource: "app/widget/scale/web", Reason: "persist desired scale intent", Cause: err})
	if result.deferred == nil || !effect.IsDeferred(result.deferred) || result.Status != "" {
		t.Fatalf("intent persistence failure terminalized completed effect: %#v", result)
	}
	if err := p.finishScaleIntent(context.Background(), claim, "widget", "web", "us-central", 3, "scaled", nil); err != nil || calls != 2 {
		t.Fatalf("reclaimed completed effect did not retry only intent write: calls=%d err=%v", calls, err)
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
