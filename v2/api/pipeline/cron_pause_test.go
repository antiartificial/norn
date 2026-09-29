package pipeline

import (
	"context"
	"errors"
	"testing"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

func TestCronPauseRequestRejectsUnboundJobOrSchedule(t *testing.T) {
	valid := &model.Operation{App: "widget", Payload: map[string]interface{}{"process": "nightly", "schedule": "0 2 * * *", "timezone": "America/Chicago", "jobId": "widget-nightly", "version": uint64(3), "modifyIndex": uint64(7)}}
	if got, err := cronPauseRequestFromOperation(valid); err != nil || got.JobID != "widget-nightly" {
		t.Fatalf("valid descriptor = %#v, %v", got, err)
	}
	for _, payload := range []map[string]interface{}{
		{"process": "nightly", "schedule": "", "timezone": "America/Chicago", "jobId": "widget-nightly", "version": uint64(3), "modifyIndex": uint64(7)},
		{"process": "nightly", "schedule": "0 2 * * *", "timezone": "America/Chicago", "jobId": "other-nightly", "version": uint64(3), "modifyIndex": uint64(7)},
		{"process": "nightly", "schedule": "0 2 * * *", "timezone": "America/Chicago", "jobId": "widget-nightly", "modifyIndex": uint64(7)},
	} {
		if _, err := cronPauseRequestFromOperation(&model.Operation{App: "widget", Payload: payload}); err == nil {
			t.Fatalf("accepted invalid descriptor %#v", payload)
		}
	}
}

func TestCronPauseRequestPreservesLargeNomadRevisions(t *testing.T) {
	const large = "9007199254740993" // one more than the largest exact float64 integer
	r, err := cronPauseRequestFromOperation(&model.Operation{App: "widget", Payload: map[string]interface{}{"process": "nightly", "schedule": "0 2 * * *", "timezone": "America/Chicago", "jobId": "widget-nightly", "version": large, "modifyIndex": large}})
	if err != nil || r.Version != 9007199254740993 || r.ModifyIndex != 9007199254740993 {
		t.Fatalf("large revision binding = %#v, %v", r, err)
	}
}

func TestCronPauseIntentMatchesOnlyExactSignedTarget(t *testing.T) {
	r := cronPauseRequest{App: "widget", Process: "nightly", Schedule: "0 2 * * *", TimeZone: "America/Chicago", JobID: "widget-nightly", Version: 3, ModifyIndex: 7}
	matching := &nomad.PeriodicJobInfo{JobID: r.JobID, Schedule: r.Schedule, TimeZone: r.TimeZone, Version: r.Version, ModifyIndex: r.ModifyIndex}
	if !cronPauseIntentMatches(r, matching, true) {
		t.Fatal("exact signed target did not match")
	}
	for _, mutate := range []func(*nomad.PeriodicJobInfo){
		func(s *nomad.PeriodicJobInfo) { s.JobID = "replacement-nightly" },
		func(s *nomad.PeriodicJobInfo) { s.Schedule = "15 2 * * *" },
		func(s *nomad.PeriodicJobInfo) { s.TimeZone = "UTC" },
		func(s *nomad.PeriodicJobInfo) { s.Version++ },
		func(s *nomad.PeriodicJobInfo) { s.ModifyIndex++ },
	} {
		state := *matching
		mutate(&state)
		if cronPauseIntentMatches(r, &state, true) {
			t.Fatalf("accepted changed target: %#v", state)
		}
	}
	// StopJob advances ModifyIndex, so post-effect evidence binds the stable
	// identity fields while Launch alone requires the signed current index.
	postStop := *matching
	postStop.Version++
	postStop.ModifyIndex++
	if !cronPauseIntentMatches(r, &postStop, false) {
		t.Fatal("post-stop state should retain the signed periodic identity")
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
	r := cronPauseRequest{App: "widget", Process: "nightly", Schedule: "0 2 * * *", TimeZone: "America/Chicago", JobID: "widget-nightly", Version: 3, ModifyIndex: 7}
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
