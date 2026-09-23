package store

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"norn/v2/api/fleet"
)

// TestFleetAttemptStoreConformance_Postgres runs the shared Fleet-attempt
// conformance suite against the PostgreSQL adapter. Opt-in via
// NORN_TEST_DATABASE_URL. A future etcd adapter gets a sibling test that calls
// runFleetAttemptStoreConformance with its own factory and must pass the same
// revision-CAS and lease-fencing invariants.
func TestFleetAttemptStoreConformance_Postgres(t *testing.T) {
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
	runFleetAttemptStoreConformance(t, func(t *testing.T) FleetAttemptStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM fleet_runner_attempts"); err != nil {
			t.Fatalf("reset fleet_runner_attempts: %v", err)
		}
		return db
	})
}

// runFleetAttemptStoreConformance is the backend-neutral behavioral contract for
// FleetAttemptStore. newStore must return a store backed by empty attempt tables
// on each call.
func runFleetAttemptStoreConformance(t *testing.T, newStore func(t *testing.T) FleetAttemptStore) {
	ctx := context.Background()

	newAttempt := func(planID string) fleet.RunnerAttempt {
		return fleet.RunnerAttempt{
			ID:                      uuid.NewString(),
			PlanID:                  planID,
			RunnerAttemptID:         uuid.NewString(),
			CommitSHA:               "abc123",
			PlanSHA256:              "deadbeef",
			WorkflowURL:             "https://github.com/o/r/actions/runs/1",
			HeartbeatTimeoutSeconds: 3600,
		}
	}

	t.Run("CreateAssignsAttemptRevisionAndRoot", func(t *testing.T) {
		s := newStore(t)
		created, err := s.CreateFleetRunnerAttempt(ctx, newAttempt(uuid.NewString()))
		if err != nil {
			t.Fatal(err)
		}
		if created.Attempt != 1 || created.Revision != 1 || created.Status != "queued" {
			t.Fatalf("create did not seed attempt/revision/status: %+v", created)
		}
		if created.RootAttemptID != created.ID {
			t.Fatalf("first attempt is not its own root: id=%s root=%s", created.ID, created.RootAttemptID)
		}
		if created.CurrentPhase != "provider_applying" {
			t.Fatalf("default phase = %q", created.CurrentPhase)
		}
	})

	t.Run("GetReturnsCreated", func(t *testing.T) {
		s := newStore(t)
		plan := uuid.NewString()
		created, err := s.CreateFleetRunnerAttempt(ctx, newAttempt(plan))
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.GetFleetRunnerAttempt(ctx, plan, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != created.ID || got.Attempt != 1 {
			t.Fatalf("get mismatch: %+v", got)
		}
	})

	t.Run("SecondAttemptContinuesRootLineage", func(t *testing.T) {
		s := newStore(t)
		plan := uuid.NewString()
		first, err := s.CreateFleetRunnerAttempt(ctx, newAttempt(plan))
		if err != nil {
			t.Fatal(err)
		}
		// A plan may only have one live attempt, so retire the first.
		if _, err := s.UpdateFleetRunnerAttempt(ctx, plan, first.ID, first.Revision, "cancel", "superseded"); err != nil {
			t.Fatal(err)
		}
		second, err := s.CreateFleetRunnerAttempt(ctx, newAttempt(plan))
		if err != nil {
			t.Fatal(err)
		}
		if second.Attempt != 2 {
			t.Fatalf("second attempt number = %d, want 2", second.Attempt)
		}
		if second.RootAttemptID != first.ID {
			t.Fatalf("root lineage broken: second.root=%s first.id=%s", second.RootAttemptID, first.ID)
		}
	})

	t.Run("UpdateRequiresMatchingRevision", func(t *testing.T) {
		s := newStore(t)
		plan := uuid.NewString()
		created, err := s.CreateFleetRunnerAttempt(ctx, newAttempt(plan))
		if err != nil {
			t.Fatal(err)
		}
		// Correct revision advances and bumps the revision.
		beat, err := s.UpdateFleetRunnerAttempt(ctx, plan, created.ID, created.Revision, "heartbeat", int64(1), "alive")
		if err != nil {
			t.Fatal(err)
		}
		if beat.Status != "running" || beat.Revision != created.Revision+1 {
			t.Fatalf("heartbeat did not advance: %+v", beat)
		}
		// A stale revision matches no row and is rejected.
		if _, err := s.UpdateFleetRunnerAttempt(ctx, plan, created.ID, created.Revision, "heartbeat", int64(2), "stale"); err == nil {
			t.Fatal("stale-revision update was accepted")
		}
		// The fresh revision still works.
		if _, err := s.UpdateFleetRunnerAttempt(ctx, plan, created.ID, beat.Revision, "heartbeat", int64(3), "alive"); err != nil {
			t.Fatalf("fresh-revision update rejected: %v", err)
		}
	})

	t.Run("AdvanceToCompleteFinishes", func(t *testing.T) {
		s := newStore(t)
		plan := uuid.NewString()
		created, err := s.CreateFleetRunnerAttempt(ctx, newAttempt(plan))
		if err != nil {
			t.Fatal(err)
		}
		done, err := s.UpdateFleetRunnerAttempt(ctx, plan, created.ID, created.Revision, "advance", "complete")
		if err != nil {
			t.Fatal(err)
		}
		if done.Status != "succeeded" || done.CurrentPhase != "complete" || done.FinishedAt == nil {
			t.Fatalf("advance to complete did not finish: %+v", done)
		}
	})

	t.Run("CancelFinishes", func(t *testing.T) {
		s := newStore(t)
		plan := uuid.NewString()
		created, err := s.CreateFleetRunnerAttempt(ctx, newAttempt(plan))
		if err != nil {
			t.Fatal(err)
		}
		canceled, err := s.UpdateFleetRunnerAttempt(ctx, plan, created.ID, created.Revision, "cancel", "operator stop")
		if err != nil {
			t.Fatal(err)
		}
		if canceled.Status != "canceled" || canceled.FinishedAt == nil {
			t.Fatalf("cancel did not finish: %+v", canceled)
		}
	})

	t.Run("ExpiredHeartbeatIsAbandonedOnRead", func(t *testing.T) {
		s := newStore(t)
		plan := uuid.NewString()
		attempt := newAttempt(plan)
		// A negative timeout puts the lease unambiguously in the past regardless
		// of host/DB clock skew, so the abandon sweep must fire on read.
		attempt.HeartbeatTimeoutSeconds = -3600
		created, err := s.CreateFleetRunnerAttempt(ctx, attempt)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.GetFleetRunnerAttempt(ctx, plan, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "abandoned" || got.FinishedAt == nil {
			t.Fatalf("expired attempt not abandoned on read: %+v", got)
		}
	})
}
