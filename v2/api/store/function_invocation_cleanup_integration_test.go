package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const functionInvocationCleanupPath = "nomad/jobs/norn-fn-0123456789abcdef0123456789abcdef01234567/invoke"

func terminalFunctionInvocationForCleanup(t *testing.T, db *DB) string {
	t.Helper()
	ctx := context.Background()
	op := insertOperationFixture(t, db, PrivateInvocationOperationKind, 1, map[string]interface{}{"process": "cleanup"})
	claimed, claim, err := db.ClaimNextOperation(ctx, "function-cleanup-worker", time.Minute, []string{PrivateInvocationOperationKind})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claim=%+v receipt=%+v err=%v", claim, claimed, err)
	}
	if _, err := db.RecordFunctionInvocationEffectStage(ctx, claim, FunctionInvocationVariableAttempt, functionInvocationCleanupPath, functionInvocationAttemptDigestTest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkFunctionInvocationEffectAttempt(ctx, claim, FunctionInvocationVariableAttempt, functionInvocationCleanupPath, functionInvocationAttemptDigestTest); err != nil {
		t.Fatal(err)
	}
	execution := functionInvocationProjection(op.ID, op.App, "cleanup", 0, 1)
	if err := db.FinishClaimedFunctionInvocation(ctx, claim, execution, "succeeded", "completed", nil); err != nil {
		t.Fatal(err)
	}
	return op.ID
}

func verifyFunctionInvocationCleanupEvidence(t *testing.T, db *DB, operationID string) {
	t.Helper()
	_, err := db.Pool.Exec(context.Background(), `
		UPDATE evidence_archive_intents
		SET state='verified', object_key='evidence/function-cleanup',
			object_sha256=$2, object_bytes=1, verified_at=clock_timestamp()
		WHERE operation_id=$1 AND subject_kind='saga' AND state='pending'
	`, operationID, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
}

func TestFunctionInvocationCleanupClaimsOnlyAfterTerminalReceiptAndVerifiedArchive(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	operationID := terminalFunctionInvocationForCleanup(t, dbs[0])
	intent, err := dbs[0].ClaimFunctionInvocationCleanup(context.Background())
	if err != nil || intent != nil {
		t.Fatalf("unarchived cleanup=%+v err=%v", intent, err)
	}
	verifyFunctionInvocationCleanupEvidence(t, dbs[0], operationID)
	intent, err = dbs[0].ClaimFunctionInvocationCleanup(context.Background())
	if err != nil || intent == nil {
		t.Fatalf("archived cleanup=%+v err=%v", intent, err)
	}
	if intent.OperationID != operationID || intent.Variable.Path != functionInvocationCleanupPath || intent.Variable.OwnerMarker != operationID || intent.Token == "" {
		t.Fatalf("cleanup intent=%+v", intent)
	}
	if err := dbs[0].CompleteFunctionInvocationVariableCleanup(context.Background(), *intent); err != nil {
		t.Fatal(err)
	}
	if again, err := dbs[0].ClaimFunctionInvocationCleanup(context.Background()); err != nil || again != nil {
		t.Fatalf("completed cleanup=%+v err=%v", again, err)
	}
}

func TestFunctionInvocationCleanupAcknowledgementIsTokenAndIdentityFenced(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 2)
	operationID := terminalFunctionInvocationForCleanup(t, dbs[0])
	verifyFunctionInvocationCleanupEvidence(t, dbs[0], operationID)
	first, err := dbs[0].ClaimFunctionInvocationCleanup(context.Background())
	if err != nil || first == nil {
		t.Fatalf("first cleanup=%+v err=%v", first, err)
	}
	if _, err := dbs[0].Pool.Exec(context.Background(), `UPDATE function_invocation_cleanup_intents SET lease_until=clock_timestamp() - interval '1 second' WHERE operation_id=$1`, operationID); err != nil {
		t.Fatal(err)
	}
	second, err := dbs[1].ClaimFunctionInvocationCleanup(context.Background())
	if err != nil || second == nil || second.Token == first.Token {
		t.Fatalf("second cleanup=%+v first=%+v err=%v", second, first, err)
	}
	if err := dbs[0].CompleteFunctionInvocationVariableCleanup(context.Background(), *first); !errors.Is(err, ErrFunctionInvocationCleanupOwnershipLost) {
		t.Fatalf("stale completion=%v", err)
	}
	wrongPath := *second
	wrongPath.Variable.Path = functionInvocationCleanupPath + "/other"
	if err := dbs[1].CompleteFunctionInvocationVariableCleanup(context.Background(), wrongPath); !errors.Is(err, ErrFunctionInvocationCleanupOwnershipLost) {
		t.Fatalf("wrong-path completion=%v", err)
	}
	if err := dbs[1].CompleteFunctionInvocationVariableCleanup(context.Background(), *second); err != nil {
		t.Fatal(err)
	}
}

func TestFunctionInvocationCleanupMigrationHasNoPrivateMaterialColumns(t *testing.T) {
	migration := functionInvocationCleanupMigration()
	if migration.Version != 20 || migration.Name != "function-invocation-variable-cleanup" || migration.MinimumReaderVersion != OperationAcceptanceRetirementReaderVersion || migration.MinimumWriterVersion != FunctionInvocationCleanupWriterVersion {
		t.Fatalf("migration=%+v", migration)
	}
	dbs, _, _ := setupEffectStores(t, 1)
	rows, err := dbs[0].Pool.Query(context.Background(), `SELECT column_name FROM information_schema.columns WHERE table_name='function_invocation_cleanup_intents' ORDER BY column_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(column, "body") || strings.Contains(column, "envelope") || strings.Contains(column, "private") || strings.Contains(column, "cipher") {
			t.Fatalf("private function material column %q is durable", column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
