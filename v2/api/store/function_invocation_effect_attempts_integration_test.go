package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

const functionInvocationAttemptDigestTest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func claimFunctionInvocationAttemptOperation(t *testing.T, db *DB, worker string, lease time.Duration) (*OperationClaim, string) {
	t.Helper()
	op := insertOperationFixture(t, db, PrivateInvocationOperationKind, 3, map[string]interface{}{"process": "worker"})
	claimed, claim, err := db.ClaimNextOperation(context.Background(), worker, lease, []string{PrivateInvocationOperationKind})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claimed function operation=%+v claim=%+v err=%v", claimed, claim, err)
	}
	return &claim, op.ID
}

func TestFunctionInvocationEffectAttemptRecordsThenFencesVariableWrite(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	ctx := context.Background()
	claim, operationID := claimFunctionInvocationAttemptOperation(t, dbs[0], "function-worker", time.Minute)
	target := "norn/function-invocation/0123456789abcdef"

	recorded, err := dbs[0].RecordFunctionInvocationEffectStage(ctx, *claim, FunctionInvocationVariableAttempt, target, functionInvocationAttemptDigestTest)
	if err != nil || recorded.Attempted || recorded.OperationID != operationID || recorded.Target != target || recorded.InputDigest != functionInvocationAttemptDigestTest {
		t.Fatalf("recorded=%+v err=%v", recorded, err)
	}

	attempted, err := dbs[0].MarkFunctionInvocationEffectAttempt(ctx, *claim, FunctionInvocationVariableAttempt, target, functionInvocationAttemptDigestTest)
	if err != nil || !attempted.Attempted || !attempted.MarkedNow || attempted.AttemptedAt == nil || attempted.ClaimGeneration != claim.Generation() {
		t.Fatalf("attempted=%+v err=%v", attempted, err)
	}

	// A retry reads the original public binding and cannot turn the stage back
	// into a fresh pre-call authorization.
	replayed, err := dbs[0].MarkFunctionInvocationEffectAttempt(ctx, *claim, FunctionInvocationVariableAttempt, target, functionInvocationAttemptDigestTest)
	if err != nil || !replayed.Attempted || replayed.MarkedNow || replayed.AttemptedAt == nil || !replayed.AttemptedAt.Equal(*attempted.AttemptedAt) {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	loaded, err := dbs[0].LoadFunctionInvocationEffectAttempt(ctx, operationID, FunctionInvocationVariableAttempt)
	if err != nil || loaded == nil || !loaded.Attempted || loaded.Target != target {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestFunctionInvocationEffectAttemptHasOneConcurrentRemoteCaller(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 2)
	ctx := context.Background()
	claim, _ := claimFunctionInvocationAttemptOperation(t, dbs[0], "function-worker", time.Minute)
	target := "norn/function-invocation/concurrent"
	if _, err := dbs[0].RecordFunctionInvocationEffectStage(ctx, *claim, FunctionInvocationVariableAttempt, target, functionInvocationAttemptDigestTest); err != nil {
		t.Fatal(err)
	}
	type result struct {
		attempt FunctionInvocationEffectAttempt
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for _, db := range dbs {
		wait.Add(1)
		go func(db *DB) {
			defer wait.Done()
			<-start
			attempt, err := db.MarkFunctionInvocationEffectAttempt(context.Background(), *claim, FunctionInvocationVariableAttempt, target, functionInvocationAttemptDigestTest)
			results <- result{attempt: attempt, err: err}
		}(db)
	}
	close(start)
	wait.Wait()
	close(results)
	markedNow := 0
	for result := range results {
		if result.err != nil || !result.attempt.Attempted {
			t.Fatalf("attempt=%+v err=%v", result.attempt, result.err)
		}
		if result.attempt.MarkedNow {
			markedNow++
		}
	}
	if markedNow != 1 {
		t.Fatalf("remote callers=%d, want 1", markedNow)
	}
}

func TestFunctionInvocationEffectAttemptRejectsChangedBindingAndRequiresRecord(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	ctx := context.Background()
	claim, _ := claimFunctionInvocationAttemptOperation(t, dbs[0], "function-worker", time.Minute)
	target := "norn/function-invocation/abcdef"

	if _, err := dbs[0].MarkFunctionInvocationEffectAttempt(ctx, *claim, FunctionInvocationJobAttempt, "norn-fn-abcdef", functionInvocationAttemptDigestTest); !errors.Is(err, ErrFunctionInvocationEffectMissing) {
		t.Fatalf("missing record error=%v", err)
	}
	if _, err := dbs[0].RecordFunctionInvocationEffectStage(ctx, *claim, FunctionInvocationJobAttempt, target, functionInvocationAttemptDigestTest); err != nil {
		t.Fatal(err)
	}
	changedDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	stored, err := dbs[0].RecordFunctionInvocationEffectStage(ctx, *claim, FunctionInvocationJobAttempt, target, changedDigest)
	if !errors.Is(err, ErrFunctionInvocationEffectConflict) || stored.InputDigest != functionInvocationAttemptDigestTest {
		t.Fatalf("changed record stored=%+v err=%v", stored, err)
	}
	stored, err = dbs[0].MarkFunctionInvocationEffectAttempt(ctx, *claim, FunctionInvocationJobAttempt, "norn-fn-changed", functionInvocationAttemptDigestTest)
	if !errors.Is(err, ErrFunctionInvocationEffectConflict) || stored.Target != target || stored.Attempted {
		t.Fatalf("changed attempt stored=%+v err=%v", stored, err)
	}
}

func TestFunctionInvocationEffectAttemptFencesStaleClaimAndLetsSuccessorAdvanceRecordedStage(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 2)
	ctx := context.Background()
	op := insertOperationFixture(t, dbs[0], PrivateInvocationOperationKind, 3, map[string]interface{}{"process": "worker"})
	_, first, err := dbs[0].ClaimNextOperation(ctx, "function-one", 100*time.Millisecond, []string{PrivateInvocationOperationKind})
	if err != nil {
		t.Fatal(err)
	}
	target := "norn/function-invocation/successor"
	if _, err := dbs[0].RecordFunctionInvocationEffectStage(ctx, first, FunctionInvocationVariableAttempt, target, functionInvocationAttemptDigestTest); err != nil {
		t.Fatal(err)
	}
	time.Sleep(180 * time.Millisecond)
	if err := dbs[1].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	_, successor, err := dbs[1].ClaimNextOperation(ctx, "function-two", time.Minute, []string{PrivateInvocationOperationKind})
	if err != nil || successor.Generation() != first.Generation()+1 {
		t.Fatalf("successor=%+v first=%+v err=%v", successor, first, err)
	}
	if _, err := dbs[0].MarkFunctionInvocationEffectAttempt(ctx, first, FunctionInvocationVariableAttempt, target, functionInvocationAttemptDigestTest); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale attempt error=%v", err)
	}
	attempted, err := dbs[1].MarkFunctionInvocationEffectAttempt(ctx, successor, FunctionInvocationVariableAttempt, target, functionInvocationAttemptDigestTest)
	if err != nil || !attempted.Attempted || attempted.ClaimGeneration != successor.Generation() || attempted.OperationID != op.ID {
		t.Fatalf("successor attempted=%+v err=%v", attempted, err)
	}
}

func TestFunctionInvocationEffectAttemptMigrationContractAndNoPrivateColumns(t *testing.T) {
	migration := functionInvocationEffectAttemptsMigration()
	if migration.Version != 19 || migration.Name != "function-invocation-effect-attempts" || migration.MinimumReaderVersion != OperationAcceptanceRetirementReaderVersion || migration.MinimumWriterVersion != FunctionInvocationEffectAttemptWriterVersion {
		t.Fatalf("migration=%+v", migration)
	}
	dbs, _, _ := setupEffectStores(t, 1)
	rows, err := dbs[0].Pool.Query(context.Background(), `SELECT column_name FROM information_schema.columns WHERE table_name='function_invocation_effect_attempts' ORDER BY column_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		if column == "body" || column == "envelope" || column == "private_content" || column == "private_bytes" {
			t.Fatalf("private function material column %q is durable", column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
