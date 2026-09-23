package store

import (
	"context"
	"time"
)

// IdentityStore is the control boundary for authentication identity and
// revocation: device enrollment, registered devices, bearer tokens with
// rotation and revocation, GitHub Actions OIDC single-use assertions (replay
// tombstones), and IP access grants. It is a domain seam for Norn v3 (roadmap
// M1 / P5, "authentication revocation, replay tombstones").
//
// COUPLING NOTE: RotateAccessToken, RevokeAccessToken and RevokeAccessDevice
// also cancel the active exec sessions bound to the revoked credential and
// return their ids. That cascade ties this boundary to the exec-session
// boundary, so a non-PostgreSQL adapter must be introduced together with an
// exec-session abstraction — the shared conformance suite pins the identity and
// revocation invariants a second backend must reproduce.
type IdentityStore interface {
	// Enrollment lifecycle.
	CreateAccessEnrollment(ctx context.Context, enrollment *AccessEnrollment) error
	GetAccessEnrollment(ctx context.Context, id string) (*AccessEnrollment, error)
	GetAccessEnrollmentByCodeHash(ctx context.Context, codeHash string) (*AccessEnrollment, error)
	ListAccessEnrollments(ctx context.Context, status string) ([]AccessEnrollment, error)
	ApproveAccessEnrollment(ctx context.Context, id, deviceID string, scopes []string) (*AccessEnrollment, error)
	ApproveAccessEnrollmentWithDevice(ctx context.Context, id string, device *AccessDevice, scopes []string) (*AccessEnrollment, error)
	ExchangeAccessEnrollment(ctx context.Context, id string) (*AccessEnrollment, error)
	ExchangeAccessEnrollmentWithToken(ctx context.Context, id string, token *AccessToken) error
	RecordAccessEnrollmentFailure(ctx context.Context, id string) error

	// Devices.
	CreateAccessDevice(ctx context.Context, device *AccessDevice) error
	// RevokeAccessDevice revokes the device and all its tokens, and cancels the
	// exec sessions they authorized (returned ids).
	RevokeAccessDevice(ctx context.Context, id string) ([]string, error)
	TouchAccessDevice(ctx context.Context, id string) error
	ListAccessDevices(ctx context.Context) ([]AccessDevice, error)

	// Tokens.
	RecordAccessToken(ctx context.Context, token *AccessToken) error
	// RotateAccessToken records the replacement and revokes the token that
	// authorized rotation, cancelling its exec sessions (returned ids).
	RotateAccessToken(ctx context.Context, previousJTI string, token *AccessToken) ([]string, error)
	AccessTokenActive(ctx context.Context, jti string) (bool, error)
	// RevokeAccessToken revokes the token and cancels its exec sessions.
	RevokeAccessToken(ctx context.Context, jti string) ([]string, error)

	// ConsumeGitHubActionsAssertion durably reserves a verified issuer+jti as a
	// single-use replay tombstone; a second use returns
	// ErrGitHubActionsAssertionConsumed.
	ConsumeGitHubActionsAssertion(ctx context.Context, issuer, jti string, expiresAt time.Time) error

	// IP access grants.
	ListAccessGrants(ctx context.Context) ([]AccessGrant, error)
	CreateAccessGrant(ctx context.Context, g *AccessGrant) error
	DeleteAccessGrant(ctx context.Context, id string) error
	MatchAccessGrant(ctx context.Context, ip string) (bool, error)
	CleanExpiredGrants(ctx context.Context) error
}

// Compile-time proof that the PostgreSQL adapter satisfies the identity
// boundary. A second adapter (with an exec-session abstraction) adds its own.
var _ IdentityStore = (*DB)(nil)
