package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

func TestCronPauseReplayResolvesBeforeLiveNomadState(t *testing.T) {
	accepted := store.AcceptedOperation{Operation: model.Operation{ID: "pause-1", Kind: "app.cron-pause", App: "widgets", Ref: "nightly", Payload: map[string]interface{}{"process": "nightly", "schedule": "0 2 * * *", "modifyIndex": "9007199254740993"}}}
	p := &pipeline.Pipeline{}
	p.SetOperationStore(canaryReplayStore{accepted: accepted})
	h := &Handler{pipeline: p}
	request := pipeline.EnqueueRequest{Authority: "authority", Actor: store.OperationActor{Issuer: "issuer", Subject: "subject"}, Key: "same-key"}
	got, replayed, err := h.resolveCronPauseReplay(context.Background(), request, "widgets", "nightly")
	if err != nil || !replayed || got.Operation.ID != "pause-1" {
		t.Fatalf("cron pause replay = %+v, replayed=%v, err=%v", got, replayed, err)
	}
	if _, _, err := h.resolveCronPauseReplay(context.Background(), request, "widgets", "different"); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("changed process replay = %v", err)
	}
}

func TestCronPauseEffectiveScheduleRejectsStateReadFailure(t *testing.T) {
	if _, err := cronPauseEffectiveSchedule("0 2 * * *", nil, errors.New("database unavailable")); err == nil {
		t.Fatal("database read failure fell back to declared schedule")
	}
	if got, err := cronPauseEffectiveSchedule("0 2 * * *", nil, pgx.ErrNoRows); err != nil || got != "0 2 * * *" {
		t.Fatalf("missing state = %q, %v", got, err)
	}
	if got, err := cronPauseEffectiveSchedule("0 2 * * *", &store.CronState{Schedule: "15 3 * * *"}, nil); err != nil || got != "15 3 * * *" {
		t.Fatalf("override = %q, %v", got, err)
	}
}
