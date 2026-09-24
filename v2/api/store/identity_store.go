package store

import (
	"context"
	"errors"
	"time"
)

// ErrIdentityNotFound is returned by backend-neutral credential operations
// when a token is absent or is no longer active.
var ErrIdentityNotFound = errors.New("identity not found")

// IdentityStore is the control boundary for enrollment, managed devices,
// bearer-token lifecycle, replay tombstones, and IP access grants.
//
// Token and device revocation are intentionally part of AuthStore rather than
// an independently composable identity backend: their result must include the
// cancellation of every active exec session authorized by that credential.
type IdentityStore interface {
	CreateAccessEnrollment(ctx context.Context, enrollment *AccessEnrollment) error
	GetAccessEnrollment(ctx context.Context, id string) (*AccessEnrollment, error)
	GetAccessEnrollmentByCodeHash(ctx context.Context, codeHash string) (*AccessEnrollment, error)
	ListAccessEnrollments(ctx context.Context, status string) ([]AccessEnrollment, error)
	ApproveAccessEnrollment(ctx context.Context, id, deviceID string, scopes []string) (*AccessEnrollment, error)
	ApproveAccessEnrollmentWithDevice(ctx context.Context, id string, device *AccessDevice, scopes []string) (*AccessEnrollment, error)
	ExchangeAccessEnrollment(ctx context.Context, id string) (*AccessEnrollment, error)
	ExchangeAccessEnrollmentWithToken(ctx context.Context, id string, token *AccessToken) error
	RecordAccessEnrollmentFailure(ctx context.Context, id string) error

	CreateAccessDevice(ctx context.Context, device *AccessDevice) error
	RevokeAccessDevice(ctx context.Context, id string) ([]string, error)
	TouchAccessDevice(ctx context.Context, id string) error
	ListAccessDevices(ctx context.Context) ([]AccessDevice, error)

	RecordAccessToken(ctx context.Context, token *AccessToken) error
	RotateAccessToken(ctx context.Context, previousJTI string, token *AccessToken) ([]string, error)
	AccessTokenActive(ctx context.Context, jti string) (bool, error)
	RevokeAccessToken(ctx context.Context, jti string) ([]string, error)

	ConsumeGitHubActionsAssertion(ctx context.Context, issuer, jti string, expiresAt time.Time) error

	ListAccessGrants(ctx context.Context) ([]AccessGrant, error)
	CreateAccessGrant(ctx context.Context, g *AccessGrant) error
	DeleteAccessGrant(ctx context.Context, id string) error
	MatchAccessGrant(ctx context.Context, ip string) (bool, error)
	CleanExpiredGrants(ctx context.Context) error
}

var _ IdentityStore = (*DB)(nil)
