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

func TestCronScheduleRequestRequiresExactNonsecretDescriptor(t *testing.T) {
	valid := &model.Operation{App: "widget", Payload: map[string]interface{}{"process": "nightly", "previousSchedule": "0 2 * * *", "schedule": "15 2 * * *", "timezone": "America/Chicago", "jobId": "widget-nightly", "imageTag": "registry/widget:v2", "specDigest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "version": "3", "modifyIndex": "9007199254740993", "paused": false}}
	q, err := cronScheduleRequestFromOperation(valid)
	if err != nil || q.ModifyIndex != 9007199254740993 || q.Paused {
		t.Fatalf("valid descriptor = %#v, %v", q, err)
	}
	for _, key := range []string{"process", "previousSchedule", "schedule", "jobId", "imageTag", "specDigest", "version", "modifyIndex", "paused"} {
		bad := *valid
		bad.Payload = map[string]interface{}{}
		for k, v := range valid.Payload {
			bad.Payload[k] = v
		}
		delete(bad.Payload, key)
		if _, err := cronScheduleRequestFromOperation(&bad); err == nil {
			t.Fatalf("accepted missing %s", key)
		}
	}
}

func TestCronScheduleIntentRequiresExactOldRevisionAndPreservesPause(t *testing.T) {
	q := cronScheduleRequest{App: "widget", Process: "nightly", PreviousSchedule: "0 2 * * *", Schedule: "15 2 * * *", TimeZone: "America/Chicago", JobID: "widget-nightly", ImageTag: "registry/widget:v2", Version: 3, ModifyIndex: 7, Paused: true}
	state := &nomad.PeriodicJobInfo{JobID: q.JobID, Schedule: q.PreviousSchedule, TimeZone: q.TimeZone, Version: q.Version, ModifyIndex: q.ModifyIndex, Paused: true}
	if !cronScheduleIntentMatches(q, state, true) {
		t.Fatal("exact signed parent refused")
	}
	state.Paused = false
	if cronScheduleIntentMatches(q, state, true) {
		t.Fatal("paused-state drift accepted")
	}
	state.Paused = true
	state.ModifyIndex++
	if cronScheduleIntentMatches(q, state, true) {
		t.Fatal("stale CAS revision accepted")
	}
	if !cronScheduleIntentMatches(q, state, false) {
		t.Fatal("post-CAS stable identity refused")
	}
	state.Schedule = q.Schedule
	if cronScheduleIntentMatches(q, state, false) {
		t.Fatal("already-updated schedule credited before effect evidence")
	}
}

func TestCronScheduleStateFailureRemainsRecoverable(t *testing.T) {
	claim, err := store.NewOperationClaim("operation", "worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := &Pipeline{FinishCronScheduleIntent: func(context.Context, store.OperationClaim, string, string, bool, string, string, map[string]interface{}) error {
		calls++
		if calls == 1 {
			return errors.New("temporary database outage")
		}
		return nil
	}}
	q := cronScheduleRequest{App: "widget", Process: "nightly", PreviousSchedule: "0 2 * * *", Schedule: "15 2 * * *", TimeZone: "America/Chicago", JobID: "widget-nightly", ImageTag: "registry/widget:v2", Version: 3, ModifyIndex: 7}
	first := p.completeCronSchedule(context.Background(), claim, q, effect.ExecuteResult{EffectID: "effect-1", Outcome: effect.OutcomeSucceeded})
	if first == nil || !effect.IsDeferred(first.deferred) {
		t.Fatalf("write failure was not deferred: %#v", first)
	}
	second := p.completeCronSchedule(context.Background(), claim, q, effect.ExecuteResult{EffectID: "effect-1", Outcome: effect.OutcomeSucceeded})
	if second == nil || second.Status != model.OperationSucceeded || calls != 2 {
		t.Fatalf("retry = %#v calls=%d", second, calls)
	}
}
