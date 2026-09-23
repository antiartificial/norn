package store

import "context"

// ExecSessionStore is the control boundary for interactive execution sessions
// and the step-up (MFA) challenges that authorize them: challenge issuance and
// verification, session creation gated on a verified challenge with a bounded
// number of active sessions per device, connect/finish/expire lifecycle, and
// cancellation of the sessions bound to a revoked credential. It is a domain
// seam for Norn v3 (roadmap M1 / P5, "execution ownership and fencing").
//
// COUPLING NOTE: this boundary reads access_devices (ConnectExecSession refuses
// a revoked device; ActiveAccessDevice) and is in turn driven by identity
// revocation, which cancels a credential's active sessions atomically. A
// non-PostgreSQL adapter must be co-designed with IdentityStore and a
// cross-boundary atomic-revocation strategy.
type ExecSessionStore interface {
	// ActiveAccessDevice returns the device only if it exists and is not revoked.
	ActiveAccessDevice(ctx context.Context, id string) (*AccessDevice, error)

	CreateStepUpChallenge(ctx context.Context, challenge *StepUpChallenge) error
	GetStepUpChallenge(ctx context.Context, id string) (*StepUpChallenge, error)
	VerifyStepUpChallenge(ctx context.Context, id, deviceID string) error

	// CreateExecSession consumes a matching verified step-up challenge and
	// enforces the per-device active-session cap.
	CreateExecSession(ctx context.Context, session *ExecSession) error
	GetExecSession(ctx context.Context, id string) (*ExecSession, error)
	ExpireExecSessions(ctx context.Context) error
	// ConnectExecSession transitions a pending session to running, stamping the
	// owning instance (ownerID), and refusing a revoked device.
	ConnectExecSession(ctx context.Context, id, ownerID string) error
	// RecoverExecSessions fails the running sessions a dead instance owned
	// (ownerID), any owner-less legacy sessions, and any past their deadline,
	// without touching another live instance's healthy sessions.
	RecoverExecSessions(ctx context.Context, ownerID string) error
	FinishExecSession(ctx context.Context, id, status string, exitCode *int, errorCode string) error
	ListExecSessions(ctx context.Context, deviceID string, all bool) ([]ExecSession, error)

	// ExecSessionAuthorized re-checks, at use time, that a session is still
	// active and its bound device is still live — an execution-boundary fence
	// (ADR 0007) applied independently of the revoke-plus-cancel cascade, so a
	// revoked device is refused at the session's next fenced check even if the
	// cancel had not propagated. Returns false (no error) for a missing session.
	ExecSessionAuthorized(ctx context.Context, sessionID string) (bool, error)

	// CancelActiveExecSessions cancels the pending/running sessions bound to a
	// credential (column "token_jti" or "device_id") and returns their ids.
	CancelActiveExecSessions(ctx context.Context, column, value, errorCode string) ([]string, error)
}

// Compile-time proof that the PostgreSQL adapter satisfies the exec-session
// boundary.
var _ ExecSessionStore = (*DB)(nil)
