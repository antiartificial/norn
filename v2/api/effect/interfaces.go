package effect

import "context"

// Store is the durable fencing boundary. Reserve must verify the live
// operation claim and acquire the unresolved resource gate atomically.
// Complete and Resolve must compare both effect ID and token generation.
type Store interface {
	Reserve(context.Context, Reservation) (ReservationResult, error)
	MarkLaunched(context.Context, Token, ExecutionIdentity) error
	Complete(context.Context, Token, Completion) error
	Resolve(context.Context, Token, Resolution) error
}

// RecoveryStore locates the unresolved effect currently holding a resource so
// a successor can drive recovery of that exact original execution rather than
// waiting on an owner that may never return.
type RecoveryStore interface {
	UnresolvedForResource(ctx context.Context, authority, resource string) (Record, bool, error)
}

// Supervisor owns a durable execution namespace. Launch must be idempotent by
// SupervisorExecutionID; Query must not translate lost history into not-found
// proof without evidence that the verifier can authenticate.
type Supervisor interface {
	// Prepare durably registers the execution namespace before the database
	// effect reservation is attempted. Launch must refuse an unregistered ID.
	Prepare(context.Context, Reservation) error
	Launch(context.Context, Reservation, LaunchMaterial) (ExecutionIdentity, error)
	Query(context.Context, Reservation, ExecutionIdentity) (Observation, error)
	// Revoke durably tombstones SupervisorExecutionID before a never-launched
	// or stopped effect may release its resource. A later paused Launch using
	// that ID must fail rather than cross the released fence.
	Revoke(context.Context, Reservation, ExecutionIdentity) (Observation, error)
	RetrieveResult(context.Context, Reservation, ExecutionIdentity, string) ([]byte, error)
}

// EvidenceVerifier is the trust boundary for supervisor/downstream evidence.
// It binds an observation to the immutable reservation and derives whether a
// failed or terminated execution is actually safe to repeat.
type EvidenceVerifier interface {
	Verify(context.Context, Record, Observation) (Verification, error)
}
