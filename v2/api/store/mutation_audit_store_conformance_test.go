package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestMutationAuditStoreConformance_Postgres runs the shared evidence-store
// conformance suite against the PostgreSQL adapter. Opt-in via
// NORN_TEST_DATABASE_URL. A future etcd/object adapter gets a sibling test that
// calls runMutationAuditStoreConformance with its own factory and must pass the
// same reserve/finish and retention invariants.
func TestMutationAuditStoreConformance_Postgres(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	runMutationAuditStoreConformance(t, func(t *testing.T) MutationAuditStore {
		for _, table := range []string{"mutation_audit_incidents", "mutation_audit_events"} {
			if _, err := db.Pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
				t.Fatalf("reset %s: %v", table, err)
			}
		}
		return db
	})
}

// runMutationAuditStoreConformance is the backend-neutral behavioral contract
// for MutationAuditStore. newStore must return a store backed by empty evidence
// tables on each call.
func runMutationAuditStoreConformance(t *testing.T, newStore func(t *testing.T) MutationAuditStore) {
	ctx := context.Background()

	reserve := func(t *testing.T, s MutationAuditStore, startedAt time.Time) *MutationAuditEvent {
		t.Helper()
		e := &MutationAuditEvent{
			ID:               uuid.NewString(),
			PrincipalSubject: "operator@example.com",
			Method:           "POST",
			Path:             "/api/v1/apps/web/deploy",
			Scopes:           []string{"api:write"},
			StartedAt:        startedAt,
		}
		if err := s.ReserveMutationAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
		return e
	}

	t.Run("ReserveThenFinishRecordsOutcome", func(t *testing.T) {
		s := newStore(t)
		e := reserve(t, s, time.Now().Add(-time.Minute))
		started, err := s.GetMutationAudit(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if started.Outcome != "started" || started.FinishedAt != nil {
			t.Fatalf("reservation not in started state: %+v", started)
		}
		if len(started.Scopes) != 1 || started.Scopes[0] != "api:write" {
			t.Fatalf("scopes not preserved: %+v", started.Scopes)
		}
		if err := s.FinishMutationAudit(ctx, e.ID, e.Path, 200, "allowed", time.Now(), 42, "sha256:digest"); err != nil {
			t.Fatal(err)
		}
		done, err := s.GetMutationAudit(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if done.Outcome != "allowed" || done.Status != 200 || done.FinishedAt == nil || done.RecordDigest != "sha256:digest" {
			t.Fatalf("finish did not record outcome: %+v", done)
		}
	})

	t.Run("FinishHappensOnlyOnce", func(t *testing.T) {
		s := newStore(t)
		e := reserve(t, s, time.Now())
		if err := s.FinishMutationAudit(ctx, e.ID, e.Path, 200, "allowed", time.Now(), 1, "d1"); err != nil {
			t.Fatal(err)
		}
		// A second finish must not silently rewrite a sealed record.
		if err := s.FinishMutationAudit(ctx, e.ID, e.Path, 500, "denied", time.Now(), 1, "d2"); err == nil {
			t.Fatal("second finish on a sealed record was accepted")
		}
	})

	t.Run("ListOrdersByStartedDesc", func(t *testing.T) {
		s := newStore(t)
		older := reserve(t, s, time.Now().Add(-2*time.Hour))
		newer := reserve(t, s, time.Now().Add(-1*time.Hour))
		list, err := s.ListMutationAudits(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 || list[0].ID != newer.ID || list[1].ID != older.ID {
			t.Fatalf("list not ordered newest-first: %+v", list)
		}
	})

	t.Run("IncidentsAttachToEvents", func(t *testing.T) {
		s := newStore(t)
		e := reserve(t, s, time.Now())
		incident := &MutationAuditIncident{
			ID:             uuid.NewString(),
			AuditEventID:   e.ID,
			ReasonCode:     "unsigned",
			Explanation:    "missing signature",
			AcknowledgedBy: "operator",
			AcknowledgedAt: time.Now(),
		}
		if err := s.InsertMutationAuditIncident(ctx, incident); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListMutationAuditIncidents(ctx, []string{e.ID})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got[e.ID]; !ok || got[e.ID].ReasonCode != "unsigned" {
			t.Fatalf("incident not attached: %+v", got)
		}
		empty, err := s.ListMutationAuditIncidents(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(empty) != 0 {
			t.Fatalf("empty query returned incidents: %+v", empty)
		}
	})

	t.Run("StaleCountsUnfinishedAndPruneKeepsThem", func(t *testing.T) {
		s := newStore(t)
		// One old, still-started reservation and one old, finished record.
		unfinished := reserve(t, s, time.Now().Add(-3*time.Hour))
		finished := reserve(t, s, time.Now().Add(-3*time.Hour))
		if err := s.FinishMutationAudit(ctx, finished.ID, finished.Path, 200, "allowed", time.Now().Add(-2*time.Hour), 1, "d"); err != nil {
			t.Fatal(err)
		}
		cutoff := time.Now().Add(-time.Hour)
		stale, err := s.CountStaleMutationAudits(ctx, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		if stale != 1 {
			t.Fatalf("stale count = %d, want 1 (only the unfinished reservation)", stale)
		}
		// Prune removes finished records older than the cutoff, never the
		// unfinished reservation whose outcome was never recorded.
		pruned, err := s.PruneMutationAudits(ctx, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		if pruned != 1 {
			t.Fatalf("pruned = %d, want 1", pruned)
		}
		if _, err := s.GetMutationAudit(ctx, unfinished.ID); err != nil {
			t.Fatalf("prune removed an unfinished reservation: %v", err)
		}
		if _, err := s.GetMutationAudit(ctx, finished.ID); err == nil {
			t.Fatal("finished record was not pruned")
		}
	})
}
