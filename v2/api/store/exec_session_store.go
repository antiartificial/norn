package store

import (
	"context"
	"time"
)

// ExecSessionStore owns step-up challenges and interactive exec sessions.
// A running session is controlled only through its immutable owner token and
// lease. Credential revocation is deliberately absent from this interface: it
// belongs to AuthStore, where revocation and session cancellation commit as one
// aggregate mutation.
type ExecSessionStore interface {
	ActiveAccessDevice(ctx context.Context, id string) (*AccessDevice, error)

	CreateStepUpChallenge(ctx context.Context, challenge *StepUpChallenge) error
	GetStepUpChallenge(ctx context.Context, id string) (*StepUpChallenge, error)
	VerifyStepUpChallenge(ctx context.Context, id, deviceID string) error

	CreateExecSession(ctx context.Context, session *ExecSession) error
	GetExecSession(ctx context.Context, id string) (*ExecSession, error)
	ExpireExecSessions(ctx context.Context) error
	RecoverExpiredExecSessions(ctx context.Context) error
	ReconcileExecSessions(ctx context.Context) error
	ConnectExecSession(ctx context.Context, id string, claim ExecSessionClaim) (bool, error)
	RenewExecSession(ctx context.Context, id, ownerID, ownerToken string, leaseDuration time.Duration) (bool, error)
	FinishOwnedExecSession(ctx context.Context, id, ownerID, ownerToken, status string, exitCode *int, errorCode string) (bool, error)
	FinishExecSession(ctx context.Context, id, status string, exitCode *int, errorCode string) error
	ListExecSessions(ctx context.Context, deviceID string, all bool) ([]ExecSession, error)
}

var _ ExecSessionStore = (*DB)(nil)
