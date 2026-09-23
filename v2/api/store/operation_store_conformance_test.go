package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
)

// TestOperationStoreConformance_Postgres runs the shared operation-store
// conformance suite against the PostgreSQL adapter. It is opt-in so the normal
// unit suite needs no local PostgreSQL: set NORN_TEST_DATABASE_URL to a
// disposable test database to exercise it.
//
// The suite is written against the OperationStore interface, not *DB. When an
// etcd adapter lands (roadmap M3/P5) it gets a sibling test that calls
// runOperationStoreConformance with its own factory, and must pass the exact
// same invariants — claim exclusivity, lease fencing, idempotency, terminal
// transitions and interrupted-operation recovery.
func TestOperationStoreConformance_Postgres(t *testing.T) {
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
	runOperationStoreConformance(t, func(t *testing.T) OperationStore {
		resetOperationTables(t, db)
		return db
	})
}

// resetOperationTables clears the operation queue and the deployment-aggregate
// tables its recovery query references, so each conformance subtest starts from
// an empty queue regardless of table-wide operations like recovery and metrics.
// Children are deleted before parents to respect foreign keys.
func resetOperationTables(t *testing.T, db *DB) {
	t.Helper()
	for _, table := range []string{"deployment_steps", "deployment_regions", "deployments", "operations"} {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
}

// runOperationStoreConformance is the backend-neutral behavioral contract for
// OperationStore. newStore must return a store backed by an empty operation
// queue on each call.
func runOperationStoreConformance(t *testing.T, newStore func(t *testing.T) OperationStore) {
	ctx := context.Background()

	newQueuedOp := func(kind string) *model.Operation {
		// Backdate the schedule so eligibility never depends on host/DB clock
		// agreement: ClaimNextOperation requires next_attempt_at <= now().
		past := time.Now().Add(-time.Minute)
		return &model.Operation{
			ID:            uuid.NewString(),
			Kind:          kind,
			App:           "conformance-app",
			Status:        model.OperationQueued,
			MaxAttempts:   3,
			StartedAt:     past,
			NextAttemptAt: past,
		}
	}

	t.Run("InsertAndGetNormalizesDefaults", func(t *testing.T) {
		s := newStore(t)
		op := &model.Operation{ID: uuid.NewString(), Kind: "conf.insert"}
		if err := s.InsertOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOperation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.OperationQueued {
			t.Fatalf("status = %q, want queued", got.Status)
		}
		if got.MaxAttempts != 1 {
			t.Fatalf("max attempts = %d, want normalized 1", got.MaxAttempts)
		}
		if got.Payload == nil || got.Metadata == nil {
			t.Fatalf("payload/metadata not initialized: %+v", got)
		}
	})

	t.Run("IdempotencyLookup", func(t *testing.T) {
		s := newStore(t)
		key := "idem-" + uuid.NewString()
		op := newQueuedOp("conf.idem")
		op.Metadata = map[string]interface{}{"idempotencyKey": key}
		if err := s.InsertOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOperationByIdempotencyKey(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != op.ID {
			t.Fatalf("idempotency lookup returned %s, want %s", got.ID, op.ID)
		}
		if _, err := s.GetOperationByIdempotencyKey(ctx, "missing-"+uuid.NewString()); err == nil {
			t.Fatal("expected error for unknown idempotency key")
		}
	})

	t.Run("ClaimIsExclusiveAndAdvancesState", func(t *testing.T) {
		s := newStore(t)
		op := newQueuedOp("conf.claim")
		if err := s.InsertOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		claimed, err := s.ClaimNextOperation(ctx, "worker-a", time.Minute, []string{"conf.claim"})
		if err != nil {
			t.Fatal(err)
		}
		if claimed == nil {
			t.Fatal("first claim returned nil for an eligible queued op")
		}
		if claimed.ID != op.ID {
			t.Fatalf("first claim did not return the queued op: %+v", claimed)
		}
		if claimed.Status != model.OperationRunning || claimed.Attempts != 1 || claimed.LockedBy != "worker-a" {
			t.Fatalf("claim did not advance state: status=%q attempts=%d lockedBy=%q", claimed.Status, claimed.Attempts, claimed.LockedBy)
		}
		// A running operation must not be re-claimed.
		again, err := s.ClaimNextOperation(ctx, "worker-b", time.Minute, []string{"conf.claim"})
		if err != nil {
			t.Fatal(err)
		}
		if again != nil {
			t.Fatalf("second claim returned a running op: %+v", again)
		}
	})

	t.Run("ClaimRespectsScheduleAndKind", func(t *testing.T) {
		s := newStore(t)
		future := newQueuedOp("conf.sched")
		future.NextAttemptAt = time.Now().Add(time.Hour)
		if err := s.InsertOperation(ctx, future); err != nil {
			t.Fatal(err)
		}
		if got, err := s.ClaimNextOperation(ctx, "worker", time.Minute, []string{"conf.sched"}); err != nil {
			t.Fatal(err)
		} else if got != nil {
			t.Fatalf("claimed an operation scheduled for the future: %+v", got)
		}
		// Wrong-kind filter must not match.
		ready := newQueuedOp("conf.kindA")
		if err := s.InsertOperation(ctx, ready); err != nil {
			t.Fatal(err)
		}
		if got, err := s.ClaimNextOperation(ctx, "worker", time.Minute, []string{"conf.kindB"}); err != nil {
			t.Fatal(err)
		} else if got != nil {
			t.Fatalf("kind filter matched the wrong kind: %+v", got)
		}
	})

	t.Run("LeaseRenewalRequiresOwnership", func(t *testing.T) {
		s := newStore(t)
		op := newQueuedOp("conf.lease")
		if err := s.InsertOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimNextOperation(ctx, "owner", time.Minute, []string{"conf.lease"}); err != nil {
			t.Fatal(err)
		}
		// A non-owner cannot move the lease.
		stranger := time.Now().Add(-time.Hour)
		if err := s.RenewOperationLease(ctx, op.ID, "stranger", stranger); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOperation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.LockedUntil == nil || !got.LockedUntil.After(time.Now()) {
			t.Fatalf("non-owner renewal changed the lease: %v", got.LockedUntil)
		}
		// The owner can.
		owned := time.Now().Add(5 * time.Minute)
		if err := s.RenewOperationLease(ctx, op.ID, "owner", owned); err != nil {
			t.Fatal(err)
		}
		got, err = s.GetOperation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.LockedUntil == nil || got.LockedUntil.Before(time.Now().Add(4*time.Minute)) {
			t.Fatalf("owner renewal did not extend the lease: %v", got.LockedUntil)
		}
	})

	t.Run("FinishRecordsTerminalAndClearsLease", func(t *testing.T) {
		s := newStore(t)
		op := newQueuedOp("conf.finish")
		if err := s.InsertOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimNextOperation(ctx, "owner", time.Minute, []string{"conf.finish"}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishOperation(ctx, op.ID, model.OperationSucceeded, "done", map[string]interface{}{"receipt": "r1"}); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOperation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.OperationSucceeded || got.FinishedAt == nil || got.LockedBy != "" {
			t.Fatalf("finish did not record a clean terminal: status=%q finished=%v lockedBy=%q", got.Status, got.FinishedAt, got.LockedBy)
		}
		if got.Metadata["receipt"] != "r1" {
			t.Fatalf("finish did not merge metadata: %+v", got.Metadata)
		}
	})

	t.Run("RetryRequeuesWithBackoff", func(t *testing.T) {
		s := newStore(t)
		op := newQueuedOp("conf.retry")
		if err := s.InsertOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimNextOperation(ctx, "owner", time.Minute, []string{"conf.retry"}); err != nil {
			t.Fatal(err)
		}
		next := time.Now().Add(30 * time.Second)
		if err := s.RetryOperation(ctx, op.ID, "transient", "boom", next, nil); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOperation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.OperationQueued || got.LastError != "boom" || got.LockedBy != "" {
			t.Fatalf("retry did not requeue cleanly: status=%q lastError=%q lockedBy=%q", got.Status, got.LastError, got.LockedBy)
		}
	})

	t.Run("DeferDoesNotConsumeAttempt", func(t *testing.T) {
		s := newStore(t)
		op := newQueuedOp("conf.defer")
		if err := s.InsertOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		claimed, err := s.ClaimNextOperation(ctx, "owner", time.Minute, []string{"conf.defer"})
		if err != nil {
			t.Fatal(err)
		}
		if claimed == nil {
			t.Fatal("claim returned nil for an eligible queued op")
		}
		if claimed.Attempts != 1 {
			t.Fatalf("claim attempts = %d, want 1", claimed.Attempts)
		}
		if err := s.DeferClaimedOperation(ctx, op.ID, "another replica holds the lock", time.Now().Add(time.Second), nil); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOperation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.OperationQueued || got.Attempts != 0 || got.LockedBy != "" {
			t.Fatalf("defer did not return the attempt: status=%q attempts=%d lockedBy=%q", got.Status, got.Attempts, got.LockedBy)
		}
	})

	t.Run("CancelOnlyAffectsQueued", func(t *testing.T) {
		s := newStore(t)
		op := newQueuedOp("conf.cancel")
		if err := s.InsertOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		got, canceled, err := s.CancelQueuedOperation(ctx, op.ID, "operator")
		if err != nil {
			t.Fatal(err)
		}
		if !canceled || got.Status != model.OperationCanceled {
			t.Fatalf("queued cancel failed: canceled=%v status=%q", canceled, got.Status)
		}
		// Cancelling a non-queued operation is a no-op.
		_, canceledAgain, err := s.CancelQueuedOperation(ctx, op.ID, "operator")
		if err != nil {
			t.Fatal(err)
		}
		if canceledAgain {
			t.Fatal("cancel reported success on an already-terminal operation")
		}
	})

	t.Run("InsertCompletedRejectsNonTerminal", func(t *testing.T) {
		s := newStore(t)
		if err := s.InsertCompletedOperation(ctx, newQueuedOp("conf.completed")); err == nil {
			t.Fatal("expected non-terminal completed operation to be rejected")
		}
		done := newQueuedOp("conf.completed")
		done.Status = model.OperationSucceeded
		if err := s.InsertCompletedOperation(ctx, done); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOperation(ctx, done.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.FinishedAt == nil {
			t.Fatal("completed operation was persisted without finished_at")
		}
	})

	t.Run("RecoverInFlightRequeuesRetryableAndFailsExhausted", func(t *testing.T) {
		s := newStore(t)
		// Retryable: an interrupted app operation with attempts remaining is
		// safe to requeue for another try.
		retryable := newQueuedOp("app.deploy") // MaxAttempts 3
		if err := s.InsertOperation(ctx, retryable); err != nil {
			t.Fatal(err)
		}
		// Exhausted: an interrupted app operation with no attempts left must
		// fail closed for manual review rather than loop.
		exhausted := newQueuedOp("app.migrate")
		exhausted.MaxAttempts = 1
		if err := s.InsertOperation(ctx, exhausted); err != nil {
			t.Fatal(err)
		}
		// Put both into a running state with an expired lease using only the
		// interface: claim (consumes an attempt), then renew the lease past.
		expired := time.Now().Add(-time.Hour)
		for _, op := range []*model.Operation{retryable, exhausted} {
			if _, err := s.ClaimNextOperation(ctx, "owner", time.Minute, []string{op.Kind}); err != nil {
				t.Fatal(err)
			}
			if err := s.RenewOperationLease(ctx, op.ID, "owner", expired); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.RecoverInFlightOperations(ctx); err != nil {
			t.Fatal(err)
		}
		gotRetryable, err := s.GetOperation(ctx, retryable.ID)
		if err != nil {
			t.Fatal(err)
		}
		if gotRetryable.Status != model.OperationQueued {
			t.Fatalf("retryable app op not requeued: status=%q", gotRetryable.Status)
		}
		gotExhausted, err := s.GetOperation(ctx, exhausted.ID)
		if err != nil {
			t.Fatal(err)
		}
		if gotExhausted.Status != model.OperationFailed {
			t.Fatalf("attempts-exhausted app op not failed closed: status=%q", gotExhausted.Status)
		}
	})

	t.Run("RecoverMaintenanceFailsExpiredPlatformOps", func(t *testing.T) {
		s := newStore(t)
		op := newQueuedOp("platform.upgrade")
		if err := s.InsertOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimNextOperation(ctx, "owner", time.Minute, []string{"platform.upgrade"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RenewOperationLease(ctx, op.ID, "owner", time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := s.RecoverMaintenanceOperations(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetOperation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.OperationFailed {
			t.Fatalf("expired platform op not failed: status=%q", got.Status)
		}
	})

	t.Run("MetricsAggregateByKindAndStatus", func(t *testing.T) {
		s := newStore(t)
		for i := 0; i < 2; i++ {
			done := newQueuedOp("conf.metrics")
			done.Status = model.OperationSucceeded
			if err := s.InsertCompletedOperation(ctx, done); err != nil {
				t.Fatal(err)
			}
		}
		metrics, err := s.OperationMetrics(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var count int64
		for _, m := range metrics {
			if m.Kind == "conf.metrics" && m.Status == model.OperationSucceeded {
				count = m.Count
			}
		}
		if count != 2 {
			t.Fatalf("metrics count for conf.metrics/succeeded = %d, want 2", count)
		}
	})
}
