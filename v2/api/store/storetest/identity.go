package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/store"
)

// RunIdentityStoreConformance is the backend-neutral behavioral contract for
// store.IdentityStore — the authentication revocation, token rotation,
// OIDC-replay and IP-grant invariants. newStore must return a store backed by
// empty identity tables on each call.
func RunIdentityStoreConformance(t *testing.T, newStore func(t *testing.T) store.IdentityStore) {
	ctx := context.Background()
	future := func() time.Time { return time.Now().Add(time.Hour) }

	newToken := func(deviceID string) store.AccessToken {
		return store.AccessToken{
			JTI:       uuid.NewString(),
			DeviceID:  deviceID,
			Subject:   "device",
			Scopes:    []string{"api:read"},
			IssuedAt:  time.Now(),
			ExpiresAt: future(),
		}
	}
	newDevice := func() store.AccessDevice {
		return store.AccessDevice{ID: uuid.NewString(), Name: "laptop", CreatedAt: time.Now()}
	}
	newEnrollment := func() *store.AccessEnrollment {
		return &store.AccessEnrollment{
			ID:              uuid.NewString(),
			CodeHash:        uuid.NewString(),
			VerifierHash:    uuid.NewString(),
			DeviceName:      "laptop",
			RequestedScopes: []string{"api:read"},
			SourceHash:      uuid.NewString(),
			CreatedAt:       time.Now(),
			ExpiresAt:       future(),
		}
	}

	t.Run("TokenActiveThenRevoked", func(t *testing.T) {
		s := newStore(t)
		tok := newToken("")
		if err := s.RecordAccessToken(ctx, &tok); err != nil {
			t.Fatal(err)
		}
		if active, err := s.AccessTokenActive(ctx, tok.JTI); err != nil || !active {
			t.Fatalf("fresh token not active: active=%v err=%v", active, err)
		}
		if _, err := s.RevokeAccessToken(ctx, tok.JTI); err != nil {
			t.Fatal(err)
		}
		if active, err := s.AccessTokenActive(ctx, tok.JTI); err != nil || active {
			t.Fatalf("revoked token still active: active=%v err=%v", active, err)
		}
		if _, err := s.RevokeAccessToken(ctx, "unknown-jti"); err == nil {
			t.Fatal("revoking an unknown token should error")
		}
	})

	t.Run("RevokeDeviceCascadesToTokens", func(t *testing.T) {
		s := newStore(t)
		dev := newDevice()
		if err := s.CreateAccessDevice(ctx, &dev); err != nil {
			t.Fatal(err)
		}
		tok := newToken(dev.ID)
		if err := s.RecordAccessToken(ctx, &tok); err != nil {
			t.Fatal(err)
		}
		if active, _ := s.AccessTokenActive(ctx, tok.JTI); !active {
			t.Fatal("device token not active before revoke")
		}
		if _, err := s.RevokeAccessDevice(ctx, dev.ID); err != nil {
			t.Fatal(err)
		}
		if active, _ := s.AccessTokenActive(ctx, tok.JTI); active {
			t.Fatal("token still active after its device was revoked")
		}
	})

	t.Run("RotateRevokesPrevious", func(t *testing.T) {
		s := newStore(t)
		a := newToken("")
		if err := s.RecordAccessToken(ctx, &a); err != nil {
			t.Fatal(err)
		}
		b := newToken("")
		b.RotatedFrom = a.JTI
		if _, err := s.RotateAccessToken(ctx, a.JTI, &b); err != nil {
			t.Fatal(err)
		}
		if active, _ := s.AccessTokenActive(ctx, a.JTI); active {
			t.Fatal("previous token still active after rotation")
		}
		if active, _ := s.AccessTokenActive(ctx, b.JTI); !active {
			t.Fatal("rotated-in token not active")
		}
		// Rotating an already-revoked token is rejected.
		c := newToken("")
		if _, err := s.RotateAccessToken(ctx, a.JTI, &c); err == nil {
			t.Fatal("rotating an already-revoked token should error")
		}
	})

	t.Run("OIDCAssertionIsSingleUse", func(t *testing.T) {
		s := newStore(t)
		issuer := "https://token.actions.githubusercontent.com"
		jti := uuid.NewString()
		if err := s.ConsumeGitHubActionsAssertion(ctx, issuer, jti, future()); err != nil {
			t.Fatal(err)
		}
		err := s.ConsumeGitHubActionsAssertion(ctx, issuer, jti, future())
		if !errors.Is(err, store.ErrGitHubActionsAssertionConsumed) {
			t.Fatalf("replayed assertion accepted: err=%v", err)
		}
	})

	t.Run("GrantMatchesAndExpires", func(t *testing.T) {
		s := newStore(t)
		live := &store.AccessGrant{ID: uuid.NewString(), IP: "10.0.0.1", CreatedAt: time.Now(), ExpiresAt: future()}
		if err := s.CreateAccessGrant(ctx, live); err != nil {
			t.Fatal(err)
		}
		expired := &store.AccessGrant{ID: uuid.NewString(), IP: "10.0.0.9", CreatedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour)}
		if err := s.CreateAccessGrant(ctx, expired); err != nil {
			t.Fatal(err)
		}
		if matched, _ := s.MatchAccessGrant(ctx, "10.0.0.1"); !matched {
			t.Fatal("live grant did not match")
		}
		if matched, _ := s.MatchAccessGrant(ctx, "10.0.0.9"); matched {
			t.Fatal("expired grant matched")
		}
		if matched, _ := s.MatchAccessGrant(ctx, "10.0.0.2"); matched {
			t.Fatal("unrelated IP matched")
		}
		grants, err := s.ListAccessGrants(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range grants {
			if g.IP == "10.0.0.9" {
				t.Fatal("list returned an expired grant")
			}
		}
		if err := s.CleanExpiredGrants(ctx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("EnrollmentApproveExchangeLifecycle", func(t *testing.T) {
		s := newStore(t)
		e := newEnrollment()
		if err := s.CreateAccessEnrollment(ctx, e); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetAccessEnrollment(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "pending" {
			t.Fatalf("new enrollment status = %q, want pending", got.Status)
		}
		approved, err := s.ApproveAccessEnrollment(ctx, e.ID, uuid.NewString(), []string{"api:read"})
		if err != nil {
			t.Fatal(err)
		}
		if approved.Status != "approved" {
			t.Fatalf("status after approve = %q", approved.Status)
		}
		exchanged, err := s.ExchangeAccessEnrollment(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if exchanged.Status != "exchanged" {
			t.Fatalf("status after exchange = %q", exchanged.Status)
		}
		if _, err := s.ApproveAccessEnrollment(ctx, e.ID, "dev", nil); err == nil {
			t.Fatal("approving a non-pending enrollment should error")
		}
	})

	t.Run("EnrollmentLocksAfterRepeatedFailures", func(t *testing.T) {
		s := newStore(t)
		e := newEnrollment()
		if err := s.CreateAccessEnrollment(ctx, e); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApproveAccessEnrollment(ctx, e.ID, uuid.NewString(), []string{"api:read"}); err != nil {
			t.Fatal(err)
		}
		locked := false
		for i := 0; i < 8; i++ {
			if errors.Is(s.RecordAccessEnrollmentFailure(ctx, e.ID), store.ErrEnrollmentLocked) {
				locked = true
				break
			}
		}
		if !locked {
			t.Fatal("enrollment did not lock after repeated verifier failures")
		}
	})
}
