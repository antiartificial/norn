package pipeline

import (
	"context"
	"errors"
	"testing"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestCronPauseRequestRejectsUnboundJobOrSchedule(t *testing.T) {
	valid := &model.Operation{App: "widget", Payload: map[string]interface{}{"process": "nightly", "schedule": "0 2 * * *", "jobId": "widget-nightly"}}
	if got, err := cronPauseRequestFromOperation(valid); err != nil || got.JobID != "widget-nightly" {
		t.Fatalf("valid descriptor = %#v, %v", got, err)
	}
	for _, payload := range []map[string]interface{}{
		{"process": "nightly", "schedule": "", "jobId": "widget-nightly"},
		{"process": "nightly", "schedule": "0 2 * * *", "jobId": "other-nightly"},
	} {
		if _, err := cronPauseRequestFromOperation(&model.Operation{App: "widget", Payload: payload}); err == nil {
			t.Fatalf("accepted invalid descriptor %#v", payload)
		}
	}
}

func TestCronPauseStateWriteFailureCanBeDeferredAndRetried(t *testing.T) {
	claim, err := store.NewOperationClaim("operation", "worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := &Pipeline{FinishCronPauseIntent: func(context.Context, store.OperationClaim, string, string, string, string, map[string]interface{}) error {
		calls++
		if calls == 1 {
			return errors.New("temporary database outage")
		}
		return nil
	}}
	r := cronPauseRequest{App: "widget", Process: "nightly", Schedule: "0 2 * * *", JobID: "widget-nightly"}
	err = p.finishCronPauseIntent(context.Background(), claim, r, "paused", nil)
	if !effect.IsDeferred(&effect.PendingError{Reason: "persist cron pause state", Cause: err}) {
		t.Fatal("write failure was not recoverable")
	}
	if err = p.finishCronPauseIntent(context.Background(), claim, r, "paused", nil); err != nil || calls != 2 {
		t.Fatalf("retry = %v calls=%d", err, calls)
	}
}

func TestCronPauseExecutionIdentityIncludesClaimGeneration(t *testing.T) {
	r := effect.Reservation{Authority: "authority", InputDigest: "sha256:input", OperationClaim: effect.OperationClaim{OperationID: "operation", Generation: 1}}
	first := cronPauseExecutionID(r)
	r.OperationClaim.Generation = 2
	if first == cronPauseExecutionID(r) {
		t.Fatal("successor reused predecessor execution identity")
	}
}
