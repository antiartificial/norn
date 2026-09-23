package store

import (
	"context"
	"time"

	"norn/v2/api/model"
)

// OperationStore is the backend-neutral control boundary for the durable
// operation lifecycle: acceptance, idempotency lookup, worker claiming with a
// fencing lease, terminal recording and crash recovery. It is the first domain
// seam extracted for Norn v3 (roadmap M1 / P5): callers depend on this
// interface rather than a concrete *DB, so an etcd-backed adapter can be
// introduced later without touching the workers and handlers above it.
//
// The PostgreSQL adapter (*DB) is the current sole implementation; the
// compile-time assertion below keeps the two in lockstep. Any second
// implementation MUST pass the shared conformance suite in
// operation_store_conformance_test.go — that suite, not this signature list, is
// the behavioral contract (claim exclusivity, lease fencing, idempotency,
// terminal transitions and interrupted-operation recovery).
//
// Scope is deliberately the operations table only. The composite
// InsertDeploymentOperation / InsertRollbackOperation writers span the
// deployment aggregate and belong to a separate boundary; they are not part of
// this seam.
type OperationStore interface {
	// AcquireAppOperationLock serializes mutable app operations across API
	// replicas. The returned release func must always be called.
	AcquireAppOperationLock(ctx context.Context, app string) (func(), bool, error)

	// InsertOperation persists a new operation, normalizing defaults.
	InsertOperation(ctx context.Context, op *model.Operation) error
	// InsertCompletedOperation persists a terminal, planning-only operation in
	// a single statement (no queued->finished window).
	InsertCompletedOperation(ctx context.Context, op *model.Operation) error

	GetOperation(ctx context.Context, id string) (*model.Operation, error)
	GetOperationByIdempotencyKey(ctx context.Context, key string) (*model.Operation, error)
	GetOperationByPromotionQualificationID(ctx context.Context, qualificationID string) (*model.Operation, error)
	GetReleaseOperationByDeploymentID(ctx context.Context, deploymentID string) (*model.Operation, error)
	GetPromotionOperationByDeploymentID(ctx context.Context, deploymentID string) (*model.Operation, error)
	ListOperations(ctx context.Context, filter OperationFilter) ([]model.Operation, error)

	// ClaimNextOperation atomically claims the oldest eligible queued operation
	// for workerID, taking a lease until now+lease. Exactly one caller can win a
	// given operation; returns (nil, nil) when nothing is eligible.
	ClaimNextOperation(ctx context.Context, workerID string, lease time.Duration, kinds []string) (*model.Operation, error)
	// RenewOperationLease extends the fencing lease only while workerID still
	// owns a running operation.
	RenewOperationLease(ctx context.Context, id, workerID string, until time.Time) error

	FinishOperation(ctx context.Context, id string, status model.OperationStatus, message string, metadata map[string]interface{}) error
	FinishOperationBySaga(ctx context.Context, sagaID string, status model.OperationStatus, message string, metadata map[string]interface{}) error
	RetryOperation(ctx context.Context, id, message, lastError string, nextAttemptAt time.Time, metadata map[string]interface{}) error
	// DeferClaimedOperation returns a claimed operation to the queue without
	// consuming an execution attempt.
	DeferClaimedOperation(ctx context.Context, id, message string, nextAttemptAt time.Time, metadata map[string]interface{}) error
	CancelQueuedOperation(ctx context.Context, id, requestedBy string) (*model.Operation, bool, error)

	// RecoverInFlightOperations re-queues safely-interrupted app operations and
	// fails ones interrupted past a mutable stage, on executor restart.
	RecoverInFlightOperations(ctx context.Context) error
	// RecoverMaintenanceOperations fails platform/host operations whose executor
	// lease expired without recording completion.
	RecoverMaintenanceOperations(ctx context.Context) error

	OperationMetrics(ctx context.Context) ([]OperationMetric, error)
}

// Compile-time proof that the PostgreSQL adapter satisfies the operations
// boundary. A future etcd adapter adds its own assertion here.
var _ OperationStore = (*DB)(nil)
