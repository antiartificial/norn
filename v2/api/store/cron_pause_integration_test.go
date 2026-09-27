package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestFinishCronPauseClaimedOperationIsClaimFencedAndAtomic(t *testing.T) {
	pools := schemaMigrationTestPools(t, 2)
	db := &DB{Pool: pools[0]}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	op := insertOperationFixture(t, db, "app.cron-pause", 1, map[string]interface{}{})
	claimed, claim, err := db.ClaimNextOperation(context.Background(), "cron-worker", 75*time.Millisecond, []string{"app.cron-pause"})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claim = %#v %v", claimed, err)
	}
	locker, err := pools[0].Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback(context.Background())
	if _, err = locker.Exec(context.Background(), `SELECT id FROM operations WHERE id=$1 FOR UPDATE`, op.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- (&DB{Pool: pools[1]}).FinishCronPauseClaimedOperation(context.Background(), claim, op.App, "nightly", "0 2 * * *", "paused", map[string]interface{}{"effectId": "effect"})
	}()
	time.Sleep(125 * time.Millisecond)
	if err = locker.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("finish after lock wait=%v, want ownership lost", err)
	}
	var states int
	if err = pools[0].QueryRow(context.Background(), `SELECT count(*) FROM cron_states WHERE app=$1`, op.App).Scan(&states); err != nil {
		t.Fatal(err)
	}
	if states != 0 {
		t.Fatalf("stale claimant wrote %d cron states", states)
	}
	current, err := db.GetOperation(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == model.OperationSucceeded {
		t.Fatal("stale claimant terminalized operation")
	}
}

func TestExpiredCronPauseRequeuesForEffectReconciliation(t *testing.T) {
	pools := schemaMigrationTestPools(t, 2)
	db := &DB{Pool: pools[0]}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	op := insertOperationFixture(t, db, "app.cron-pause", 2, map[string]interface{}{"process": "nightly"})
	claimed, oldClaim, err := db.ClaimNextOperation(context.Background(), "old-cron-worker", time.Minute, []string{"app.cron-pause"})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("old claim = %#v, %v", claimed, err)
	}
	if _, err := db.Pool.Exec(context.Background(), `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := (&DB{Pool: pools[1]}).RecoverExpiredOperations(context.Background()); err != nil {
		t.Fatal(err)
	}
	queued, err := db.GetOperation(context.Background(), op.ID)
	if err != nil || queued.Status != model.OperationQueued || queued.Metadata["manualRecoveryRequired"] == true {
		t.Fatalf("recovered cron pause = %+v, %v", queued, err)
	}
	claimed, currentClaim, err := (&DB{Pool: pools[1]}).ClaimNextOperation(context.Background(), "new-cron-worker", time.Minute, []string{"app.cron-pause"})
	if err != nil || claimed == nil || currentClaim.Generation() <= oldClaim.Generation() {
		t.Fatalf("new claim = %#v generation=%d, old=%d, err=%v", claimed, currentClaim.Generation(), oldClaim.Generation(), err)
	}
	if err := db.FinishCronPauseClaimedOperation(context.Background(), oldClaim, op.App, "nightly", "0 2 * * *", "paused", nil); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale worker finish = %v", err)
	}
	if err := (&DB{Pool: pools[1]}).FinishCronPauseClaimedOperation(context.Background(), currentClaim, op.App, "nightly", "0 2 * * *", "paused", nil); err != nil {
		t.Fatalf("successor finish = %v", err)
	}
}

func TestCronPauseDeferredEffectConsumesRetryBudgetAndPreservesEvidence(t *testing.T) {
	pools := schemaMigrationTestPools(t, 2)
	db := &DB{Pool: pools[0]}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	op := insertOperationFixture(t, db, "app.cron-pause", 3, map[string]interface{}{"process": "nightly"})

	var authority string
	if err := db.Pool.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity WHERE singleton`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO operation_effects (id,generation,authority,resource,operation_id,claim_owner,claim_generation,stage,input_digest,launch_payload,supervisor,supervisor_execution_id,lifecycle)
		VALUES ('cron-pause-evidence-' || $1,1,$2::uuid,$3,$1,'worker',1,'app.cron-pause.nomad','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','{}'::jsonb,'nomad-cron-pause','evidence','reserved')
	`, op.ID, authority, "app/"+op.App+"/cron/nightly"); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		claimed, claim, err := (&DB{Pool: pools[attempt%2]}).ClaimNextOperation(ctx, "cron-worker", time.Minute, []string{"app.cron-pause"})
		if err != nil || claimed == nil || claimed.ID != op.ID || claimed.Attempts != attempt {
			t.Fatalf("attempt %d claim=%+v err=%v", attempt, claimed, err)
		}
		terminal, err := db.DeferOrFailCronPauseClaimedOperation(ctx, claim, nil, "Nomad response remains ambiguous", time.Now().Add(-time.Second), map[string]interface{}{"effectId": "effect-1", "effectResource": "app/" + op.App + "/cron/nightly", "externalEffectRecoveryPending": true})
		if err != nil || terminal != (attempt == 3) {
			t.Fatalf("attempt %d terminal=%v err=%v", attempt, terminal, err)
		}
		current, err := db.GetOperation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			if current.Status != model.OperationQueued || current.Attempts != attempt || current.Metadata["effectId"] != "effect-1" {
				t.Fatalf("attempt %d requeue=%+v", attempt, current)
			}
			continue
		}
		if current.Status != model.OperationFailed || current.Attempts != 3 || current.Metadata["manualRecoveryRequired"] != true || current.Metadata["retryBudgetExhausted"] != true || current.Metadata["effectId"] != "effect-1" {
			t.Fatalf("budget exhaustion operation=%+v", current)
		}
	}
	var effects, intents int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_effects WHERE operation_id=$1`, op.ID).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM evidence_archive_intents WHERE operation_id=$1 AND state='pending'`, op.ID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if effects != 1 || intents != 1 {
		t.Fatalf("effect evidence=%d archive intents=%d, want 1/1", effects, intents)
	}
}

func TestCronPauseDeferredEffectRejectsStaleClaim(t *testing.T) {
	pools := schemaMigrationTestPools(t, 2)
	db := &DB{Pool: pools[0]}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	op := insertOperationFixture(t, db, "app.cron-pause", 2, map[string]interface{}{})
	_, stale, err := db.ClaimNextOperation(context.Background(), "old-cron-worker", 50*time.Millisecond, []string{"app.cron-pause"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if err := (&DB{Pool: pools[1]}).RecoverExpiredOperations(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, currentClaim, err := (&DB{Pool: pools[1]}).ClaimNextOperation(context.Background(), "new-cron-worker", time.Minute, []string{"app.cron-pause"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DeferOrFailCronPauseClaimedOperation(context.Background(), stale, nil, "stale", time.Now(), nil); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale defer error=%v", err)
	}
	current, err := db.GetOperation(context.Background(), op.ID)
	if err != nil || current.Status != model.OperationRunning || current.LockGeneration != currentClaim.Generation() {
		t.Fatalf("stale defer changed current operation=%+v err=%v", current, err)
	}
}
