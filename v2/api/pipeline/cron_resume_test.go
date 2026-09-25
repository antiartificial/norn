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

func TestCronResumeRequestRequiresExactNonsecretDescriptor(t *testing.T) {
	valid := &model.Operation{App: "widget", Payload: map[string]interface{}{"process": "nightly", "schedule": "0 2 * * *", "timezone": "America/Chicago", "jobId": "widget-nightly", "imageTag": "registry/widget:v2", "specDigest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "version": "3", "modifyIndex": "9007199254740993"}}
	q, err := cronResumeRequestFromOperation(valid)
	if err != nil || q.ModifyIndex != 9007199254740993 {
		t.Fatalf("valid descriptor = %#v, %v", q, err)
	}
	for _, key := range []string{"process", "schedule", "jobId", "imageTag", "specDigest", "version", "modifyIndex"} {
		bad := *valid
		bad.Payload = make(map[string]interface{}, len(valid.Payload))
		for k, v := range valid.Payload {
			bad.Payload[k] = v
		}
		delete(bad.Payload, key)
		if _, err := cronResumeRequestFromOperation(&bad); err == nil {
			t.Fatalf("accepted missing %s", key)
		}
	}
	bad := *valid
	bad.Payload = map[string]interface{}{"process": "nightly", "schedule": "0 2 * * *", "timezone": "America/Chicago", "jobId": "another-nightly", "imageTag": "registry/widget:v2", "specDigest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "version": "3", "modifyIndex": "7"}
	if _, err := cronResumeRequestFromOperation(&bad); err == nil {
		t.Fatal("accepted another job")
	}
}

func TestCronResumeIntentRequiresStoppedExactRevisionBeforeLaunch(t *testing.T) {
	q := cronResumeRequest{App: "widget", Process: "nightly", Schedule: "0 2 * * *", TimeZone: "America/Chicago", JobID: "widget-nightly", ImageTag: "registry/widget:v2", Version: 3, ModifyIndex: 7}
	state := &nomad.PeriodicJobInfo{JobID: q.JobID, Schedule: q.Schedule, TimeZone: q.TimeZone, Version: q.Version, ModifyIndex: q.ModifyIndex, Paused: true}
	if !cronResumeIntentMatches(q, state, true) {
		t.Fatal("exact signed parent refused")
	}
	state.ModifyIndex++
	if cronResumeIntentMatches(q, state, true) {
		t.Fatal("stale CAS revision accepted")
	}
	if !cronResumeIntentMatches(q, state, false) {
		t.Fatal("post-CAS stable identity refused")
	}
	state.Schedule = "15 2 * * *"
	if cronResumeIntentMatches(q, state, false) {
		t.Fatal("different schedule credited to signed resume")
	}
}

func TestCronResumeStateFailureRemainsRecoverable(t *testing.T) {
	claim, err := store.NewOperationClaim("operation", "worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := &Pipeline{FinishCronResumeIntent: func(context.Context, store.OperationClaim, string, string, string, string, map[string]interface{}) error {
		calls++
		if calls == 1 {
			return errors.New("temporary database outage")
		}
		return nil
	}}
	q := cronResumeRequest{App: "widget", Process: "nightly", Schedule: "0 2 * * *", TimeZone: "America/Chicago", JobID: "widget-nightly", ImageTag: "registry/widget:v2", Version: 3, ModifyIndex: 7}
	result := effect.ExecuteResult{EffectID: "effect-1", Outcome: effect.OutcomeSucceeded}
	first := p.completeCronResume(context.Background(), claim, q, result)
	if first == nil || !effect.IsDeferred(first.deferred) {
		t.Fatalf("write failure was not deferred: %#v", first)
	}
	second := p.completeCronResume(context.Background(), claim, q, result)
	if second == nil || second.Status != model.OperationSucceeded || calls != 2 {
		t.Fatalf("retry = %#v calls=%d", second, calls)
	}
}

func TestCronResumeExecutionIdentityChangesWithClaimGeneration(t *testing.T) {
	r := effect.Reservation{Authority: "authority", InputDigest: "sha256:input", OperationClaim: effect.OperationClaim{OperationID: "operation", Generation: 1}}
	first := cronResumeExecutionID(r)
	r.OperationClaim.Generation = 2
	if first == cronResumeExecutionID(r) {
		t.Fatal("successor reused predecessor execution identity")
	}
}

func TestCronBlockingEffectRejectsUnknownStage(t *testing.T) {
	_, err := (&Pipeline{}).recoverCronBlockingEffect(context.Background(), effect.Record{Reservation: effect.Reservation{Stage: "app.other.nomad"}})
	if err == nil {
		t.Fatal("unknown effect stage was treated as reconciled")
	}
}
