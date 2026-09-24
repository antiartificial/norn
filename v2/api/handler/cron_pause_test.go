package handler

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/store"
)

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
