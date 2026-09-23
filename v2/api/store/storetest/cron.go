package storetest

import (
	"context"
	"testing"

	"norn/v2/api/store"
)

// RunCronStoreConformance is the backend-neutral behavioral contract for
// store.CronStore — upsert-then-get, pause toggling, per-app listing ordered by
// process, and an error for a missing state.
func RunCronStoreConformance(t *testing.T, newStore func(t *testing.T) store.CronStore) {
	ctx := context.Background()

	t.Run("UpsertGetToggle", func(t *testing.T) {
		s := newStore(t)
		if err := s.UpsertCronState(ctx, "web", "beat", false, "* * * * *"); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetCronState(ctx, "web", "beat")
		if err != nil {
			t.Fatal(err)
		}
		if got.Paused || got.Schedule != "* * * * *" {
			t.Fatalf("unexpected state: paused=%v schedule=%q", got.Paused, got.Schedule)
		}
		if err := s.UpsertCronState(ctx, "web", "beat", true, "0 * * * *"); err != nil {
			t.Fatal(err)
		}
		got, err = s.GetCronState(ctx, "web", "beat")
		if err != nil {
			t.Fatal(err)
		}
		if !got.Paused || got.Schedule != "0 * * * *" {
			t.Fatalf("upsert did not update: paused=%v schedule=%q", got.Paused, got.Schedule)
		}
		if _, err := s.GetCronState(ctx, "web", "missing"); err == nil {
			t.Fatal("get of a missing state should error")
		}
	})

	t.Run("ListPerAppOrderedByProcess", func(t *testing.T) {
		s := newStore(t)
		if err := s.UpsertCronState(ctx, "web", "zeta", false, "@daily"); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertCronState(ctx, "web", "alpha", false, "@hourly"); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertCronState(ctx, "other", "beta", false, "@weekly"); err != nil {
			t.Fatal(err)
		}
		states, err := s.GetCronStates(ctx, "web")
		if err != nil {
			t.Fatal(err)
		}
		if len(states) != 2 {
			t.Fatalf("per-app list returned %d, want 2", len(states))
		}
		if states[0].Process != "alpha" || states[1].Process != "zeta" {
			t.Fatalf("not ordered by process: %q, %q", states[0].Process, states[1].Process)
		}
	})
}
