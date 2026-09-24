package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
)

func operationTestStores(t *testing.T, count int) []*DB {
	t.Helper()
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	stores := make([]*DB, 0, count)
	for range count {
		db, err := Connect(databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, db)
		t.Cleanup(db.Close)
	}
	if err := Migrate(stores[0]); err != nil {
		t.Fatal(err)
	}
	return stores
}

func insertOperationFixture(t *testing.T, db *DB, kind string, maxAttempts int, payload map[string]interface{}) *model.Operation {
	t.Helper()
	// PostgreSQL in a disposable container can lag the host clock slightly.
	// Keep queued fixture admission independent of that clock skew.
	now := time.Now().UTC().Add(-time.Second)
	op := &model.Operation{ID: "operation-fence-" + uuid.NewString(), Kind: kind, App: "app-" + uuid.NewString(), SagaID: "saga-" + uuid.NewString(), Status: model.OperationQueued, MaxAttempts: maxAttempts, StartedAt: now, NextAttemptAt: now, Payload: payload, Metadata: map[string]interface{}{}}
	if err := db.InsertOperation(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(context.Background(), `DELETE FROM operations WHERE id=$1`, op.ID) })
	return op
}

func insertDeploymentOperationFixture(t *testing.T, db *DB, maxAttempts int) (*model.Deployment, *model.Operation) {
	t.Helper()
	now := time.Now().UTC().Add(-time.Second)
	deployment := &model.Deployment{ID: "deployment-fence-" + uuid.NewString(), App: "app-" + uuid.NewString(), SagaID: "saga-" + uuid.NewString(), Status: model.StatusQueued, StartedAt: now}
	op := &model.Operation{ID: "operation-fence-" + uuid.NewString(), Kind: "app.deploy", App: deployment.App, SagaID: deployment.SagaID, Status: model.OperationQueued, MaxAttempts: maxAttempts, StartedAt: now, NextAttemptAt: now, Payload: map[string]interface{}{"deploymentId": deployment.ID}, Metadata: map[string]interface{}{"deploymentId": deployment.ID}}
	regions := []model.ResolvedRegion{{Name: "test", NomadRegion: "global", TrafficWeight: 100}}
	if err := db.InsertDeploymentOperation(context.Background(), deployment, regions, op); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM operations WHERE id=$1`, op.ID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM deployments WHERE id=$1`, deployment.ID)
	})
	return deployment, op
}

func TestOperationClaimsFenceStaleGenerationAndCancellation(t *testing.T) {
	stores := operationTestStores(t, 2)
	ctx := context.Background()
	op := insertOperationFixture(t, stores[0], "app.preflight", 4, map[string]interface{}{})
	claimedA, claimA, err := stores[0].ClaimNextOperation(ctx, "worker-a", 100*time.Millisecond, []string{"app.preflight"})
	if err != nil || claimedA == nil || claimA.Generation() != 1 {
		t.Fatalf("first claim op=%v claim=%+v err=%v", claimedA, claimA, err)
	}
	time.Sleep(180 * time.Millisecond)
	if err := stores[1].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	claimedB, claimB, err := stores[1].ClaimNextOperation(ctx, "worker-b", time.Minute, []string{"app.preflight"})
	if err != nil || claimedB == nil || claimB.Generation() != claimA.Generation()+1 {
		t.Fatalf("second claim op=%v claim=%+v err=%v", claimedB, claimB, err)
	}
	staleCalls := []func() error{
		func() error { return stores[0].RenewOperationClaim(ctx, claimA, time.Minute) },
		func() error {
			return stores[0].DeferClaimedOperation(ctx, claimA, "stale defer", time.Now(), map[string]interface{}{"staleDefer": true})
		},
		func() error {
			return stores[0].RetryClaimedOperation(ctx, claimA, "stale retry", "stale", time.Now(), map[string]interface{}{"staleRetry": true})
		},
		func() error {
			return stores[0].FinishClaimedOperation(ctx, claimA, model.OperationSucceeded, "stale finish", map[string]interface{}{"staleFinish": true})
		},
	}
	for i, call := range staleCalls {
		if err := call(); !errors.Is(err, ErrOperationOwnershipLost) {
			t.Fatalf("stale call %d error=%v, want ownership lost", i, err)
		}
	}
	current, err := stores[1].GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != model.OperationRunning || current.LockedBy != claimB.OwnerID() || current.LockGeneration != claimB.Generation() {
		t.Fatalf("stale write changed current owner: %+v", current)
	}
	for _, key := range []string{"staleDefer", "staleRetry", "staleFinish"} {
		if _, exists := current.Metadata[key]; exists {
			t.Fatalf("stale metadata %q was written", key)
		}
	}
	if err := stores[1].FinishClaimedOperation(ctx, claimB, model.OperationSucceeded, "current success", nil); err != nil {
		t.Fatal(err)
	}

	cancelOp := insertOperationFixture(t, stores[0], "app.preflight", 3, map[string]interface{}{})
	_, cancelClaim, err := stores[0].ClaimNextOperation(ctx, "worker-a", 80*time.Millisecond, []string{"app.preflight"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := stores[1].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if _, canceled, err := stores[1].CancelQueuedOperation(ctx, cancelOp.ID, "test"); err != nil || !canceled {
		t.Fatalf("cancel canceled=%v err=%v", canceled, err)
	}
	if err := stores[0].FinishClaimedOperation(ctx, cancelClaim, model.OperationSucceeded, "stale success", nil); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale finish after cancellation=%v", err)
	}
	canceled, err := stores[1].GetOperation(ctx, cancelOp.ID)
	if err != nil || canceled.Status != model.OperationCanceled {
		t.Fatalf("cancellation lost: op=%+v err=%v", canceled, err)
	}
}

func TestOperationClaimHasSingleConcurrentWinner(t *testing.T) {
	stores := operationTestStores(t, 2)
	op := insertOperationFixture(t, stores[0], "app.preflight", 3, map[string]interface{}{})
	type result struct {
		op    *model.Operation
		claim OperationClaim
		err   error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i, db := range stores {
		wg.Add(1)
		go func(i int, db *DB) {
			defer wg.Done()
			got, claim, err := db.ClaimNextOperation(context.Background(), "concurrent-worker-"+string(rune('a'+i)), time.Minute, []string{"app.preflight"})
			results <- result{got, claim, err}
		}(i, db)
	}
	wg.Wait()
	close(results)
	winners := 0
	for got := range results {
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.op != nil {
			winners++
			if got.op.ID != op.ID || got.claim.Generation() != 1 {
				t.Fatalf("unexpected winner op=%+v claim=%+v", got.op, got.claim)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d, want 1", winners)
	}
}

func TestRetryClaimRequiresDurablePreMutationProof(t *testing.T) {
	stores := operationTestStores(t, 1)
	ctx := context.Background()
	unsafeDeployment, unsafeOp := insertDeploymentOperationFixture(t, stores[0], 3)
	_, unsafeClaim, err := stores[0].ClaimNextOperation(ctx, "panic-worker", time.Minute, []string{"app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Pool.Exec(ctx, `INSERT INTO deployment_steps(deployment_id,app,saga_id,step,kind,status,attempt) VALUES($1,$2,$3,'future-effect','readonly','failed',1)`, unsafeDeployment.ID, unsafeDeployment.App, unsafeDeployment.SagaID); err != nil {
		t.Fatal(err)
	}
	if err := stores[0].RetryClaimedOperation(ctx, unsafeClaim, "panic", "panic", time.Now(), nil); !errors.Is(err, ErrOperationRetryUnsafe) {
		t.Fatalf("unsafe retry error=%v", err)
	}
	stillOwned, err := stores[0].GetOperation(ctx, unsafeOp.ID)
	if err != nil || stillOwned.Status != model.OperationRunning || stillOwned.LockGeneration != unsafeClaim.Generation() {
		t.Fatalf("unsafe retry changed claim: %+v err=%v", stillOwned, err)
	}
	if err := stores[0].FinishClaimedOperation(ctx, unsafeClaim, model.OperationFailed, "manual review", map[string]interface{}{"manualRecoveryRequired": true}); err != nil {
		t.Fatal(err)
	}

	safeDeployment, safeOp := insertDeploymentOperationFixture(t, stores[0], 3)
	_, safeClaim, err := stores[0].ClaimNextOperation(ctx, "safe-worker", time.Minute, []string{"app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := stores[0].RetryClaimedOperation(ctx, safeClaim, "retry", "pre-mutation", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	requeued, err := stores[0].GetOperation(ctx, safeOp.ID)
	if err != nil || requeued.Status != model.OperationQueued {
		t.Fatalf("safe retry=%+v err=%v", requeued, err)
	}
	deployment, err := stores[0].GetDeployment(ctx, safeDeployment.ID)
	if err != nil || deployment.Status != model.StatusQueued {
		t.Fatalf("safe deployment=%+v err=%v", deployment, err)
	}
}

func TestRetryClaimCASRejectsOwnerChangeWhileUpdateWaitsOnRowLock(t *testing.T) {
	stores := operationTestStores(t, 2)
	ctx := context.Background()
	op := insertOperationFixture(t, stores[0], "app.preflight", 3, map[string]interface{}{})
	_, staleClaim, err := stores[0].ClaimNextOperation(ctx, "stale-worker", time.Minute, []string{"app.preflight"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := stores[1].Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM operations WHERE id=$1 FOR UPDATE`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE operations SET locked_by='replacement-worker', lock_generation=lock_generation+1, locked_until=now()+interval '1 minute' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	retryDone := make(chan error, 1)
	go func() {
		retryDone <- stores[0].RetryClaimedOperation(context.Background(), staleClaim, "stale retry", "stale", time.Now(), map[string]interface{}{"staleRace": true})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waiting bool
		if err := stores[1].Pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND query LIKE '%operation-retry-cas%'
			  AND wait_event_type='Lock'
		)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry did not reach the controlled row-lock wait")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-retryDone; !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("racing stale retry error=%v", err)
	}
	current, err := stores[1].GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != model.OperationRunning || current.LockedBy != "replacement-worker" || current.LockGeneration != staleClaim.Generation()+1 {
		t.Fatalf("stale retry overwrote replacement: %+v", current)
	}
	if _, ok := current.Metadata["staleRace"]; ok {
		t.Fatal("stale racing retry wrote metadata")
	}
}

func TestExpiredOperationRecoveryPreservesLiveAndClassifiesSafeWork(t *testing.T) {
	stores := operationTestStores(t, 2)
	ctx := context.Background()
	liveDeployment, liveOp := insertDeploymentOperationFixture(t, stores[0], 3)
	_, _, err := stores[0].ClaimNextOperation(ctx, "live-owner", time.Minute, []string{"app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := stores[1].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	assertOperationDeploymentStatus(t, stores[1], liveOp.ID, liveDeployment.ID, model.OperationRunning, model.StatusQueued)

	safeDeployment, safeOp := insertDeploymentOperationFixture(t, stores[0], 3)
	unsafeDeployment, unsafeOp := insertDeploymentOperationFixture(t, stores[0], 3)
	if _, _, err := stores[0].ClaimNextOperation(ctx, "safe-owner", 100*time.Millisecond, []string{"app.deploy"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stores[0].ClaimNextOperation(ctx, "unsafe-owner", 100*time.Millisecond, []string{"app.deploy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Pool.Exec(ctx, `INSERT INTO deployment_steps(deployment_id,app,saga_id,step,kind,status,attempt) VALUES($1,$2,$3,'submit','mutation','running',1)`, unsafeDeployment.ID, unsafeDeployment.App, unsafeDeployment.SagaID); err != nil {
		t.Fatal(err)
	}
	unsafeData := insertOperationFixture(t, stores[0], "app.migrate", 5, map[string]interface{}{})
	maintenance := insertOperationFixture(t, stores[0], "platform.upgrade", 5, map[string]interface{}{})
	if _, _, err := stores[0].ClaimNextOperation(ctx, "data-owner", 100*time.Millisecond, []string{"app.migrate"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stores[0].ClaimNextOperation(ctx, "maintenance-owner", 100*time.Millisecond, []string{"platform.upgrade"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(180 * time.Millisecond)
	if err := stores[1].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	assertOperationDeploymentStatus(t, stores[1], safeOp.ID, safeDeployment.ID, model.OperationQueued, model.StatusQueued)
	assertOperationDeploymentStatus(t, stores[1], unsafeOp.ID, unsafeDeployment.ID, model.OperationFailed, model.StatusFailed)
	for _, id := range []string{unsafeData.ID, maintenance.ID} {
		got, err := stores[1].GetOperation(ctx, id)
		if err != nil || got.Status != model.OperationFailed || got.Metadata["manualRecoveryRequired"] != true {
			t.Fatalf("manual recovery operation %s = %+v err=%v", id, got, err)
		}
	}
}

func TestExpiredRestartRequeuesForDurableEffectReconciliation(t *testing.T) {
	stores := operationTestStores(t, 2)
	ctx := context.Background()
	op := insertOperationFixture(t, stores[0], "app.restart", 1, map[string]interface{}{"app": "demo"})
	if _, _, err := stores[0].ClaimNextOperation(ctx, "restart-owner", 100*time.Millisecond, []string{"app.restart"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(180 * time.Millisecond)
	if err := stores[1].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := stores[1].GetOperation(ctx, op.ID)
	if err != nil || got.Status != model.OperationQueued || got.Metadata["recoveredAfterRestart"] != true || got.Metadata["manualRecoveryRequired"] != nil {
		t.Fatalf("recovered restart = %+v err=%v", got, err)
	}
}

func TestExpiredCanaryPromotionRequeuesForDurableEffectReconciliation(t *testing.T) {
	stores := operationTestStores(t, 2)
	ctx := context.Background()
	op := insertOperationFixture(t, stores[0], "app.canary-promote", 1, map[string]interface{}{"app": "demo"})
	_, claim, err := stores[0].ClaimNextOperation(ctx, "canary-owner", 100*time.Millisecond, []string{"app.canary-promote"})
	if err != nil {
		t.Fatal(err)
	}
	effectStore, err := NewPGEffectStore(stores[0])
	if err != nil {
		t.Fatal(err)
	}
	authority, err := effectStore.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effectReservation(t, authority, "app/"+op.App+"/canary-promote/us", "canary-crash-"+op.ID, claim)
	if _, err := effectStore.Reserve(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = stores[0].Pool.Exec(context.Background(), `DELETE FROM operation_effects WHERE operation_id=$1`, op.ID)
	})
	time.Sleep(180 * time.Millisecond)
	if err := stores[1].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := stores[1].GetOperation(ctx, op.ID)
	if err != nil || got.Status != model.OperationQueued || got.Metadata["recoveredAfterRestart"] != true || got.Metadata["manualRecoveryRequired"] != nil {
		t.Fatalf("recovered canary = %+v err=%v", got, err)
	}
}

func TestDeploymentRecoveryIgnoresForeignTerminalReferencesAndPreservesOrphans(t *testing.T) {
	stores := operationTestStores(t, 2)
	ctx := context.Background()
	liveDeployment, liveOp := insertDeploymentOperationFixture(t, stores[0], 3)
	if _, _, err := stores[0].ClaimNextOperation(ctx, "live-owner", time.Minute, []string{"app.deploy"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	foreign := &model.Operation{ID: "foreign-" + uuid.NewString(), Kind: "release.qualification", SagaID: "foreign-saga", Status: model.OperationFailed, MaxAttempts: 1, StartedAt: now, FinishedAt: &now, Payload: map[string]interface{}{"deploymentId": liveDeployment.ID}}
	if err := stores[0].InsertCompletedOperation(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = stores[0].Pool.Exec(context.Background(), `DELETE FROM operations WHERE id=$1`, foreign.ID)
	})
	orphan := &model.Deployment{ID: "orphan-" + uuid.NewString(), App: "orphan-app", SagaID: "orphan-saga", Status: model.StatusQueued, StartedAt: now}
	if err := stores[0].InsertDeployment(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = stores[0].Pool.Exec(context.Background(), `DELETE FROM deployments WHERE id=$1`, orphan.ID)
	})
	if err := stores[1].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	assertOperationDeploymentStatus(t, stores[1], liveOp.ID, liveDeployment.ID, model.OperationRunning, model.StatusQueued)
	gotOrphan, err := stores[1].GetDeployment(ctx, orphan.ID)
	if err != nil || gotOrphan.Status != model.StatusQueued {
		t.Fatalf("orphan deployment was mutated: %+v err=%v", gotOrphan, err)
	}
}

func assertOperationDeploymentStatus(t *testing.T, db *DB, operationID, deploymentID string, operationStatus model.OperationStatus, deploymentStatus model.DeployStatus) {
	t.Helper()
	op, err := db.GetOperation(context.Background(), operationID)
	if err != nil || op.Status != operationStatus {
		t.Fatalf("operation %s status=%v err=%v, want %s", operationID, op, err, operationStatus)
	}
	deployment, err := db.GetDeployment(context.Background(), deploymentID)
	if err != nil || deployment.Status != deploymentStatus {
		t.Fatalf("deployment %s status=%v err=%v, want %s", deploymentID, deployment, err, deploymentStatus)
	}
}
