package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/model"
)

func functionInvocationProjection(operationID, app, process string, exitCode int, duration int64) FuncExecution {
	return FuncExecution{
		ID: operationID, App: app, Process: process, Status: "complete",
		ExitCode: &exitCode, DurationMs: &duration, StartedAt: time.Now().UTC().Add(-time.Second),
	}
}

func TestFinishClaimedFunctionInvocationAtomicallyPublishesProjectionAndReceipt(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	ctx := context.Background()
	op := insertOperationFixture(t, dbs[0], PrivateInvocationOperationKind, 3, map[string]interface{}{"process": "resize"})
	claimed, claim, err := dbs[0].ClaimNextOperation(ctx, "function-worker", time.Minute, []string{PrivateInvocationOperationKind})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claim operation=%+v claim=%+v err=%v", claimed, claim, err)
	}
	execution := functionInvocationProjection(op.ID, op.App, "resize", 0, 42)
	if err := dbs[0].FinishClaimedFunctionInvocation(ctx, claim, execution, model.OperationSucceeded, "function completed", map[string]interface{}{"allocationId": "alloc-1"}); err != nil {
		t.Fatal(err)
	}

	var projection FuncExecution
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT id, app, process, status, exit_code, started_at, finished_at, duration_ms FROM func_executions WHERE id=$1`, op.ID).Scan(
		&projection.ID, &projection.App, &projection.Process, &projection.Status, &projection.ExitCode, &projection.StartedAt, &projection.FinishedAt, &projection.DurationMs,
	); err != nil {
		t.Fatal(err)
	}
	if projection.ID != op.ID || projection.App != op.App || projection.Process != "resize" || projection.Status != "complete" || projection.ExitCode == nil || *projection.ExitCode != 0 || projection.DurationMs == nil || *projection.DurationMs != 42 || projection.FinishedAt == nil {
		t.Fatalf("projection=%+v", projection)
	}
	finished, err := dbs[0].GetOperation(ctx, op.ID)
	if err != nil || finished.Status != model.OperationSucceeded || finished.Message != "function completed" || finished.Metadata["allocationId"] != "alloc-1" || finished.FinishedAt == nil || finished.LockedBy != "" {
		t.Fatalf("receipt=%+v err=%v", finished, err)
	}
	var subjectKind, subjectID string
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT subject_kind, subject_id FROM evidence_archive_intents WHERE operation_id=$1`, op.ID).Scan(&subjectKind, &subjectID); err != nil || subjectKind != "operation" || subjectID != op.ID {
		t.Fatalf("archive subject=%q/%q err=%v", subjectKind, subjectID, err)
	}
}

func TestFunctionOperationEvidenceWaitsForTerminalProjectionAndLoadsOnlyPublicEvidence(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	keys, err := NewPrivateInvocationKeyRing("invocation-1", map[string][]byte{"invocation-1": bytes.Repeat([]byte{0x77}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	request := newAcceptance(t, stores[0], "function-archive", "operator", "demo", false)
	request.Identity.Kind, request.Operation.Kind = PrivateInvocationOperationKind, PrivateInvocationOperationKind
	request.Operation.Payload = map[string]interface{}{"process": "archive"}
	accepted, err := stores[0].AcceptPrivateInvocation(ctx, request, PrivateInvocationInput{Body: "private-function-canary", Method: "POST", Path: "/private-canary"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	var subjectKind, subjectID string
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT subject_kind, subject_id FROM evidence_archive_intents WHERE operation_id=$1`, accepted.Operation.ID).Scan(&subjectKind, &subjectID); err != nil || subjectKind != "operation" || subjectID != accepted.Operation.ID {
		t.Fatalf("acceptance archive subject=%q/%q err=%v", subjectKind, subjectID, err)
	}
	if _, err := dbs[0].ProcessPendingEvidenceIntent(ctx, 0, func(context.Context, EvidenceIntent, EvidenceSource) (EvidencePublication, error) {
		t.Fatal("queued function invocation was archivable")
		return EvidencePublication{}, nil
	}); !errors.Is(err, ErrNoEvidenceWork) {
		t.Fatalf("queued function archive error=%v", err)
	}
	claimed, claim, err := dbs[0].ClaimNextOperation(ctx, "function-archive-worker", time.Minute, []string{PrivateInvocationOperationKind})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim=%+v receipt=%+v err=%v", claim, claimed, err)
	}
	if err := dbs[0].FinishClaimedFunctionInvocation(ctx, claim, functionInvocationProjection(accepted.Operation.ID, accepted.Operation.App, "archive", 0, 4), model.OperationSucceeded, "completed", nil); err != nil {
		t.Fatal(err)
	}
	_, err = dbs[0].ProcessPendingEvidenceIntent(ctx, 0, func(_ context.Context, intent EvidenceIntent, source EvidenceSource) (EvidencePublication, error) {
		if intent.SubjectKind != "operation" || intent.SubjectID != accepted.Operation.ID || source.OperationKind != PrivateInvocationOperationKind || source.Acceptance == nil || len(source.FunctionExecutionJSON) == 0 || len(source.FunctionEffectAttemptsJSON) == 0 {
			t.Fatalf("function archive source intent=%+v source=%+v", intent, source)
		}
		for _, data := range [][]byte{source.OperationJSON, source.FunctionExecutionJSON, source.FunctionEffectAttemptsJSON, source.Acceptance.CanonicalBytes, source.Acceptance.RequestCanonicalBytes} {
			if bytes.Contains(data, []byte("private-function-canary")) || bytes.Contains(data, []byte("/private-canary")) {
				t.Fatal("private invocation material entered archive source")
			}
		}
		return EvidencePublication{ObjectKey: "evidence/function-operation", ObjectSHA256: strings.Repeat("a", 64), ObjectBytes: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFinishClaimedFunctionInvocationRejectsStaleClaimWithoutProjection(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 2)
	ctx := context.Background()
	op := insertOperationFixture(t, dbs[0], PrivateInvocationOperationKind, 3, map[string]interface{}{"process": "resize"})
	_, stale, err := dbs[0].ClaimNextOperation(ctx, "function-one", 100*time.Millisecond, []string{PrivateInvocationOperationKind})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(180 * time.Millisecond)
	if err := dbs[1].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	_, successor, err := dbs[1].ClaimNextOperation(ctx, "function-two", time.Minute, []string{PrivateInvocationOperationKind})
	if err != nil || successor.Generation() != stale.Generation()+1 {
		t.Fatalf("successor=%+v stale=%+v err=%v", successor, stale, err)
	}
	execution := functionInvocationProjection(op.ID, op.App, "resize", 0, 3)
	if err := dbs[0].FinishClaimedFunctionInvocation(ctx, stale, execution, model.OperationSucceeded, "stale", nil); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale finish=%v", err)
	}
	var projections int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT count(*) FROM func_executions WHERE id=$1`, op.ID).Scan(&projections); err != nil || projections != 0 {
		t.Fatalf("stale projection count=%d err=%v", projections, err)
	}
	stillRunning, err := dbs[0].GetOperation(ctx, op.ID)
	if err != nil || stillRunning.Status != model.OperationRunning || stillRunning.LockGeneration != successor.Generation() {
		t.Fatalf("operation after stale finish=%+v err=%v", stillRunning, err)
	}
}

func TestFinishClaimedFunctionInvocationDoesNotTerminalizeOnProjectionConflict(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	ctx := context.Background()
	op := insertOperationFixture(t, dbs[0], PrivateInvocationOperationKind, 3, map[string]interface{}{"process": "resize"})
	_, claim, err := dbs[0].ClaimNextOperation(ctx, "function-worker", time.Minute, []string{PrivateInvocationOperationKind})
	if err != nil {
		t.Fatal(err)
	}
	existing := functionInvocationProjection(op.ID, op.App, "resize", 0, 1)
	existing.Status = "running"
	if err := dbs[0].InsertFuncExecution(ctx, &existing); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].FinishClaimedFunctionInvocation(ctx, claim, functionInvocationProjection(op.ID, op.App, "resize", 0, 2), model.OperationSucceeded, "completed", nil); !errors.Is(err, ErrFunctionInvocationExecutionConflict) {
		t.Fatalf("finish conflicting projection=%v", err)
	}
	stillRunning, err := dbs[0].GetOperation(ctx, op.ID)
	if err != nil || stillRunning.Status != model.OperationRunning || stillRunning.LockGeneration != claim.Generation() {
		t.Fatalf("operation after projection conflict=%+v err=%v", stillRunning, err)
	}
}

func TestFinishClaimedFunctionInvocationRejectsWrongOperationKind(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	ctx := context.Background()
	op := insertOperationFixture(t, dbs[0], "app.restart", 3, map[string]interface{}{"process": "resize"})
	_, claim, err := dbs[0].ClaimNextOperation(ctx, "restart-worker", time.Minute, []string{"app.restart"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].FinishClaimedFunctionInvocation(ctx, claim, functionInvocationProjection(op.ID, op.App, "resize", 0, 2), model.OperationSucceeded, "completed", nil); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("wrong-kind finish=%v", err)
	}
	var projections int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT count(*) FROM func_executions WHERE id=$1`, op.ID).Scan(&projections); err != nil || projections != 0 {
		t.Fatalf("wrong-kind projection count=%d err=%v", projections, err)
	}
}

func TestFinishClaimedFunctionInvocationRejectsNonPublicReceiptMetadata(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	ctx := context.Background()
	op := insertOperationFixture(t, dbs[0], PrivateInvocationOperationKind, 3, map[string]interface{}{"process": "resize"})
	_, claim, err := dbs[0].ClaimNextOperation(ctx, "function-worker", time.Minute, []string{PrivateInvocationOperationKind})
	if err != nil {
		t.Fatal(err)
	}
	err = dbs[0].FinishClaimedFunctionInvocation(ctx, claim, functionInvocationProjection(op.ID, op.App, "resize", 0, 2), model.OperationSucceeded, "completed", map[string]interface{}{"requestBody": "private"})
	if err == nil {
		t.Fatal("private receipt metadata was accepted")
	}
	var projections int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT count(*) FROM func_executions WHERE id=$1`, op.ID).Scan(&projections); err != nil || projections != 0 {
		t.Fatalf("private-metadata projection count=%d err=%v", projections, err)
	}
	stillRunning, err := dbs[0].GetOperation(ctx, op.ID)
	if err != nil || stillRunning.Status != model.OperationRunning || stillRunning.LockGeneration != claim.Generation() {
		t.Fatalf("operation after private metadata=%+v err=%v", stillRunning, err)
	}
}
