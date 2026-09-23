package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestReviewCatalogActivationRequiresCurrentClaim(t *testing.T) {
	f := newTargetFixture(t)
	ctx := context.Background()
	accepted, err := f.p.QueueCatalogActivation(ctx, f.catalog, 0, f.request)
	if err != nil {
		t.Fatal(err)
	}
	// No worker claimed the operation. An executor must not mutate routing
	// merely because it holds a copy of the accepted payload.
	_, _ = f.p.executeCatalogActivation(ctx, &accepted.Operation, store.OperationClaim{})
	if _, err := f.db.ActiveDatabaseCatalog(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("catalog activation without a current claim changed durable routing: %v", err)
	}
}

func TestReviewCatalogActivationCannotAdoptAnotherWritersRevision(t *testing.T) {
	f := newTargetFixture(t)
	ctx := context.Background()
	if _, err := f.p.QueueCatalogActivation(ctx, f.catalog, 0, f.request); err != nil {
		t.Fatal(err)
	}
	op, claim, err := f.db.ClaimNextOperation(ctx, "catalog-provenance-review", time.Minute, []string{CatalogActivationKind})
	if err != nil || op == nil {
		t.Fatalf("claim: %v, %v", op, err)
	}
	// Another authorized writer won the expected-revision CAS. Equal bytes
	// do not make that activation this operation's crash-recovery receipt.
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, f.catalog, "another-writer"); err != nil {
		t.Fatal(err)
	}
	result, err := f.p.executeCatalogActivation(ctx, op, claim)
	if err == nil && result != nil && result.Status == model.OperationSucceeded {
		t.Fatal("operation claimed success using another writer's catalog revision")
	}
}

func TestReviewCatalogActivationRejectsLeaseExpiredDuringLockWait(t *testing.T) {
	f := newTargetFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.p.QueueCatalogActivation(ctx, f.catalog, 0, f.request); err != nil {
		t.Fatal(err)
	}
	op, claim, err := f.db.ClaimNextOperation(ctx, "catalog-lock-review", time.Second, []string{CatalogActivationKind})
	if err != nil || op == nil {
		t.Fatalf("claim: %v, %v", op, err)
	}
	lockConn, err := pgx.ConnectConfig(ctx, f.db.Pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close(context.Background())
	holder, err := lockConn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('norn:database-catalog', 0))`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := f.p.executeCatalogActivation(ctx, op, claim); done <- err }()
	for {
		var waiting bool
		if err := holder.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pg_backend_pid() = ANY(pg_blocking_pids(pid)))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("activation did not wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	for {
		var expired bool
		if err := holder.QueryRow(ctx, `SELECT clock_timestamp() > locked_until FROM operations WHERE id=$1`, op.ID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := f.db.ActiveDatabaseCatalog(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("lease expired during catalog lock wait but durable routing changed: %v", err)
	}
}
