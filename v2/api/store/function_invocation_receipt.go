package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"norn/v2/api/model"
)

// FunctionInvocationReceiptStore is the terminal persistence boundary for a
// claimed function invocation. Implementations must publish the function
// projection and the operation receipt under the same claim fence.
type FunctionInvocationReceiptStore interface {
	FinishClaimedFunctionInvocation(context.Context, OperationClaim, FuncExecution, model.OperationStatus, string, map[string]interface{}) error
}

// FunctionInvocationReceiptWithAppLockStore is required when the execution
// backend uses lease-fenced app locks. The lock proof joins the claim and
// immutable projection in the same terminal transaction.
type FunctionInvocationReceiptWithAppLockStore interface {
	FinishClaimedFunctionInvocationWithAppLock(context.Context, OperationClaim, AppOperationLock, FuncExecution, model.OperationStatus, string, map[string]interface{}) error
}

var ErrFunctionInvocationExecutionConflict = errors.New("function invocation execution projection conflicts with durable record")

// FinishClaimedFunctionInvocation atomically publishes a terminal function
// execution projection and its operation receipt. The execution ID is the
// accepted operation ID, so a result cannot be associated with a different
// invocation. A lost or expired claim writes neither record.
func (db *DB) FinishClaimedFunctionInvocation(ctx context.Context, claim OperationClaim, execution FuncExecution, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	if db == nil || db.Pool == nil {
		return fmt.Errorf("function invocation receipt store is unavailable")
	}
	if err := validateOperationClaim(claim); err != nil {
		return err
	}
	if status != model.OperationSucceeded && status != model.OperationFailed {
		return fmt.Errorf("function invocation completion status %q is invalid", status)
	}
	if execution.ID != claim.OperationID() || strings.TrimSpace(execution.App) == "" || strings.TrimSpace(execution.Process) == "" ||
		(execution.Status != "complete" && execution.Status != "failed") || execution.ExitCode == nil || execution.DurationMs == nil || *execution.DurationMs < 0 || execution.StartedAt.IsZero() {
		return fmt.Errorf("function invocation execution projection is invalid")
	}
	if (status == model.OperationSucceeded) != (execution.Status == "complete") {
		return fmt.Errorf("function invocation execution status does not match operation receipt")
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if err := validateFunctionInvocationReceiptMetadata(execution, metadata); err != nil {
		return err
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode function invocation receipt metadata: %w", err)
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := checkOperationClaimLocked(ctx, tx, claim); err != nil {
		return err
	}

	var kind, app, process string
	if err := tx.QueryRow(ctx, `SELECT kind, app, payload->>'process' FROM operations WHERE id=$1`, claim.OperationID()).Scan(&kind, &app, &process); err != nil {
		return err
	}
	if kind != PrivateInvocationOperationKind || app != execution.App || process != execution.Process {
		return fmt.Errorf("function invocation execution projection does not match accepted operation")
	}

	inserted, err := tx.Exec(ctx, `
		INSERT INTO func_executions (id, app, process, status, exit_code, started_at, finished_at, duration_ms)
		VALUES ($1,$2,$3,$4,$5,$6,clock_timestamp(),$7)
		ON CONFLICT (id) DO NOTHING
	`, execution.ID, execution.App, execution.Process, execution.Status, *execution.ExitCode, execution.StartedAt, *execution.DurationMs)
	if err != nil {
		return err
	}
	if inserted.RowsAffected() != 1 {
		return ErrFunctionInvocationExecutionConflict
	}

	var operationApp string
	if err := tx.QueryRow(ctx, `
		UPDATE operations
		SET status=$1, message=$2, metadata=metadata || $3::jsonb,
			locked_by='', locked_until=NULL, updated_at=clock_timestamp(), finished_at=clock_timestamp()
		WHERE id=$4
		RETURNING app
	`, status, message, data, claim.OperationID()).Scan(&operationApp); err != nil {
		return err
	}
	// Acceptance creates this intent for current writers. Keep the terminal
	// insert for writers that accepted before that contract, but bind it to the
	// operation so its terminal func_executions projection can be archived.
	if _, err := tx.Exec(ctx, `
		INSERT INTO evidence_archive_intents (id, subject_kind, subject_id, app, operation_id, sequence, state)
		VALUES ('ei-' || gen_random_uuid()::text, 'operation', $1, $2, $1, 1, 'pending')
		ON CONFLICT (subject_kind, subject_id, sequence) DO NOTHING
	`, claim.OperationID(), operationApp); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Function invocation receipts are visible through generic operation history.
// Keep this projection limited to identifiers and values already represented
// by the terminal execution record; request and database material remain
// private to the invocation envelope and allocation variable.
func validateFunctionInvocationReceiptMetadata(execution FuncExecution, metadata map[string]interface{}) error {
	for key, value := range metadata {
		switch key {
		case "jobId", "allocationId":
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" || len(text) > 512 || strings.ContainsAny(text, "\r\n\x00") {
				return fmt.Errorf("function invocation receipt metadata %q is invalid", key)
			}
		case "exitCode":
			exitCode, ok := value.(int)
			if !ok || exitCode != *execution.ExitCode {
				return fmt.Errorf("function invocation receipt exit code differs from execution projection")
			}
		case "durationMs":
			duration, ok := value.(int64)
			if !ok || duration != *execution.DurationMs {
				return fmt.Errorf("function invocation receipt duration differs from execution projection")
			}
		default:
			return fmt.Errorf("function invocation receipt metadata %q is not public", key)
		}
	}
	return nil
}
