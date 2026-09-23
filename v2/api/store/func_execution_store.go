package store

import "context"

// FuncExecutionStore is the control boundary for one-off function invocation
// records, keyed by id and queried by app. It is a cleanly isolated single-table
// concern — a backend-neutral seam for Norn v3 (roadmap M3 / P5). Diagnostics and
// duration statistics may follow a shorter retention than proof of accepted work.
type FuncExecutionStore interface {
	InsertFuncExecution(ctx context.Context, fe *FuncExecution) error
	UpdateFuncExecution(ctx context.Context, id, status string, exitCode int, durationMs int64) error
	ListFuncExecutions(ctx context.Context, app string, limit int) ([]FuncExecution, error)
}

// Compile-time proof that the PostgreSQL adapter satisfies the boundary.
var _ FuncExecutionStore = (*DB)(nil)
