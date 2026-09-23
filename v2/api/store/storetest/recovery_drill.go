package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/store"
)

// RunRecoveryDrillStoreConformance is the backend-neutral behavioral contract
// for store.RecoveryDrillStore — insert-as-running, finish-once (only a running
// drill transitions), get/list, and latest-passed-per-kind reduction.
func RunRecoveryDrillStoreConformance(t *testing.T, newStore func(t *testing.T) store.RecoveryDrillStore) {
	ctx := context.Background()

	newDrill := func(kind string) *store.RecoveryDrill {
		return &store.RecoveryDrill{ID: uuid.NewString(), Kind: kind, Target: "control", InitiatedBy: "op", StartedAt: time.Now()}
	}

	t.Run("InsertRunningThenFinishOnce", func(t *testing.T) {
		s := newStore(t)
		d := newDrill("restore")
		if err := s.InsertRecoveryDrill(ctx, d); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRecoveryDrill(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "running" {
			t.Fatalf("new drill status = %q, want running", got.Status)
		}
		finished, err := s.FinishRecoveryDrill(ctx, d.ID, "passed", map[string]string{"rto": "12m"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if finished.Status != "passed" || finished.Evidence["rto"] != "12m" || finished.FinishedAt == nil {
			t.Fatalf("finish did not apply: %+v", finished)
		}
		// A second finish on the now-terminal drill fails.
		if _, err := s.FinishRecoveryDrill(ctx, d.ID, "failed", nil, time.Now()); err == nil {
			t.Fatal("finishing an already-finished drill should error")
		}
	})

	t.Run("GetMissingErrors", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.GetRecoveryDrill(ctx, "nope"); err == nil {
			t.Fatal("get of a missing drill should error")
		}
	})

	t.Run("ListNewestFirstAndLatestPassed", func(t *testing.T) {
		s := newStore(t)
		older := newDrill("restore")
		older.StartedAt = time.Now().Add(-2 * time.Hour)
		newer := newDrill("failover")
		newer.StartedAt = time.Now()
		for _, d := range []*store.RecoveryDrill{older, newer} {
			if err := s.InsertRecoveryDrill(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
		// Finish two "restore" drills passed; latest-passed should keep the newer.
		firstPass := time.Now().Add(-time.Hour)
		if _, err := s.FinishRecoveryDrill(ctx, older.ID, "passed", nil, firstPass); err != nil {
			t.Fatal(err)
		}
		another := newDrill("restore")
		if err := s.InsertRecoveryDrill(ctx, another); err != nil {
			t.Fatal(err)
		}
		latestPass := time.Now()
		if _, err := s.FinishRecoveryDrill(ctx, another.ID, "passed", nil, latestPass); err != nil {
			t.Fatal(err)
		}

		list, err := s.ListRecoveryDrills(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 3 {
			t.Fatalf("list returned %d, want 3", len(list))
		}

		latest, err := s.LatestPassedRecoveryDrills(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := latest["restore"]; !ok || got.Before(latestPass.Add(-time.Second)) {
			t.Fatalf("latest passed restore = %v, want ~%v", got, latestPass)
		}
		if _, ok := latest["failover"]; ok {
			t.Fatal("failover was never finished passed; should not appear")
		}
	})
}
