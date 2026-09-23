package storetest

import (
	"context"
	"testing"
	"time"

	"norn/v2/api/store"
)

// RunAccessPatternStoreConformance is the backend-neutral behavioral contract
// for store.AccessPatternStore — hour-bucket accumulation with status splitting,
// replace-overwrites, re-grouping by hour-of-day/weekday, the since filter, and
// pruning by bucket age.
func RunAccessPatternStoreConformance(t *testing.T, newStore func(t *testing.T) store.AccessPatternStore) {
	ctx := context.Background()
	// A fixed reference time (a Sunday 14:30 UTC) so hour/weekday assertions are
	// deterministic. 2026-09-20 is a Sunday.
	base := time.Date(2026, 9, 20, 14, 30, 0, 0, time.UTC)

	obs := func(status int, count int64, at time.Time) store.AccessObservation {
		return store.AccessObservation{App: "web", Process: "http", Endpoint: "/api", Source: "external", ObservedAt: at, Count: count, Status: status}
	}

	t.Run("RecordAccumulatesWithStatusSplit", func(t *testing.T) {
		s := newStore(t)
		if err := s.RecordAccessObservation(ctx, obs(200, 3, base)); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordAccessObservation(ctx, obs(404, 1, base.Add(5*time.Minute))); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordAccessObservation(ctx, obs(500, 2, base.Add(10*time.Minute))); err != nil {
			t.Fatal(err)
		}
		rows, err := s.ListAccessPatternRows(ctx, base.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("same hour bucket should yield 1 row, got %d", len(rows))
		}
		r := rows[0]
		if r.Requests != 6 || r.Successes != 3 || r.ClientErrors != 1 || r.ServerErrors != 2 {
			t.Fatalf("accumulation wrong: %+v", r)
		}
		if r.Hour != 14 || r.Weekday != 0 {
			t.Fatalf("hour/weekday wrong: hour=%d weekday=%d (want 14/0)", r.Hour, r.Weekday)
		}
	})

	t.Run("ReplaceOverwrites", func(t *testing.T) {
		s := newStore(t)
		if err := s.RecordAccessObservation(ctx, obs(200, 5, base)); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceAccessObservation(ctx, obs(200, 1, base)); err != nil {
			t.Fatal(err)
		}
		rows, err := s.ListAccessPatternRows(ctx, base.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Requests != 1 {
			t.Fatalf("replace did not overwrite: %+v", rows)
		}
	})

	t.Run("GroupsSameHourWeekdayAcrossDays", func(t *testing.T) {
		s := newStore(t)
		// Same hour-of-day (14) and weekday (Sunday) one week apart -> one row.
		if err := s.RecordAccessObservation(ctx, obs(200, 2, base)); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordAccessObservation(ctx, obs(200, 3, base.AddDate(0, 0, 7))); err != nil {
			t.Fatal(err)
		}
		rows, err := s.ListAccessPatternRows(ctx, base.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Requests != 5 {
			t.Fatalf("cross-day same hour/weekday not grouped: %+v", rows)
		}
	})

	t.Run("SinceFilterAndPrune", func(t *testing.T) {
		s := newStore(t)
		old := base.AddDate(0, 0, -30)
		if err := s.RecordAccessObservation(ctx, obs(200, 1, old)); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordAccessObservation(ctx, obs(200, 1, base)); err != nil {
			t.Fatal(err)
		}
		// since excludes the old bucket.
		rows, err := s.ListAccessPatternRows(ctx, base.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("since filter failed: got %d rows", len(rows))
		}
		// Prune removes the old bucket; a wide since then still sees only the recent one.
		if err := s.PruneAccessObservations(ctx, base.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		rows, err = s.ListAccessPatternRows(ctx, old.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("prune did not remove the old bucket: got %d rows", len(rows))
		}
	})
}
