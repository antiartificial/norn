package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/store"
)

// RunAuthAggregateConformance is the cross-boundary behavioral contract that the
// boundary-local IdentityStore and ExecSessionStore suites cannot express: that
// revoking a credential and cancelling the exec sessions it authorized is a
// single atomic operation, with the cancelled ids observable to the caller.
// This is the release-gating invariant of ADR 0007 (Accepted). A second-backend
// AuthStore adapter must pass this suite in addition to both boundary suites.
//
// On a single-store backend (PostgreSQL transaction, or one etcd multi-key
// compare-and-swap) there is no intermediate state a crash could expose between
// the credential mutation and the session cancellation — they commit or fail as
// one unit — so "a crash leaves no authorized-but-revoked session" is satisfied
// structurally rather than by fault injection here.
func RunAuthAggregateConformance(t *testing.T, newStore func(t *testing.T) store.AuthStore) {
	ctx := context.Background()
	future := func() time.Time { return time.Now().Add(time.Hour) }

	// provisionSession creates a live device, an active token bound to it, a
	// verified step-up challenge, and a pending exec session authorized by both.
	// It returns the session id.
	provisionSession := func(t *testing.T, s store.AuthStore, deviceID, jti, appID string) string {
		t.Helper()
		token := store.AccessToken{JTI: jti, DeviceID: deviceID, Subject: "device", Scopes: []string{"api:read"}, IssuedAt: time.Now(), ExpiresAt: future()}
		if err := s.RecordAccessToken(ctx, &token); err != nil {
			t.Fatal(err)
		}
		challengeID := uuid.NewString()
		ch := &store.StepUpChallenge{ID: challengeID, DeviceID: deviceID, TokenJTI: jti, Purpose: "exec", Resource: appID, NonceHash: uuid.NewString(), CreatedAt: time.Now(), ExpiresAt: future()}
		if err := s.CreateStepUpChallenge(ctx, ch); err != nil {
			t.Fatal(err)
		}
		if err := s.VerifyStepUpChallenge(ctx, challengeID, deviceID); err != nil {
			t.Fatal(err)
		}
		session := &store.ExecSession{ID: uuid.NewString(), DeviceID: deviceID, TokenJTI: jti, ChallengeID: challengeID, AppID: appID, AllocationID: "alloc", Task: "web", Command: []string{"sh"}, CommandDigest: "digest", CreatedAt: time.Now(), ExpiresAt: future()}
		if err := s.CreateExecSession(ctx, session); err != nil {
			t.Fatal(err)
		}
		return session.ID
	}

	registerDevice := func(t *testing.T, s store.AuthStore, deviceID string) {
		t.Helper()
		if err := s.CreateAccessDevice(ctx, &store.AccessDevice{ID: deviceID, Name: "aggregate-device", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	assertCanceled := func(t *testing.T, s store.AuthStore, sessionID string) {
		t.Helper()
		got, err := s.GetExecSession(ctx, sessionID)
		if err != nil {
			t.Fatalf("get session %s: %v", sessionID, err)
		}
		if got.Status != "canceled" {
			t.Fatalf("session %s status = %q, want canceled", sessionID, got.Status)
		}
	}

	t.Run("RevokeTokenCancelsItsSessions", func(t *testing.T) {
		s := newStore(t)
		dev, jti := uuid.NewString(), uuid.NewString()
		registerDevice(t, s, dev)
		sessionID := provisionSession(t, s, dev, jti, "web")

		ids, err := s.RevokeAccessToken(ctx, jti)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != sessionID {
			t.Fatalf("revoke returned %v, want [%s]", ids, sessionID)
		}
		assertCanceled(t, s, sessionID)
		if active, _ := s.AccessTokenActive(ctx, jti); active {
			t.Fatal("token still active after revoke")
		}
	})

	t.Run("RevokeDeviceCancelsAllItsSessions", func(t *testing.T) {
		s := newStore(t)
		dev := uuid.NewString()
		registerDevice(t, s, dev)
		// Two sessions under the same device, each with its own token/challenge.
		a := provisionSession(t, s, dev, uuid.NewString(), "web")
		b := provisionSession(t, s, dev, uuid.NewString(), "api")

		ids, err := s.RevokeAccessDevice(ctx, dev)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 2 {
			t.Fatalf("revoke device cancelled %d sessions, want 2 (%v)", len(ids), ids)
		}
		assertCanceled(t, s, a)
		assertCanceled(t, s, b)
	})

	t.Run("RotateCancelsPreviousSessions", func(t *testing.T) {
		s := newStore(t)
		dev, prev := uuid.NewString(), uuid.NewString()
		registerDevice(t, s, dev)
		sessionID := provisionSession(t, s, dev, prev, "web")

		next := store.AccessToken{JTI: uuid.NewString(), DeviceID: dev, Subject: "device", Scopes: []string{"api:read"}, IssuedAt: time.Now(), ExpiresAt: future(), RotatedFrom: prev}
		ids, err := s.RotateAccessToken(ctx, prev, &next)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != sessionID {
			t.Fatalf("rotate returned %v, want [%s]", ids, sessionID)
		}
		assertCanceled(t, s, sessionID)
		if active, _ := s.AccessTokenActive(ctx, next.JTI); !active {
			t.Fatal("rotated-in token not active")
		}
	})

	t.Run("FenceRefusesAfterDeviceRevocation", func(t *testing.T) {
		s := newStore(t)
		dev, jti := uuid.NewString(), uuid.NewString()
		registerDevice(t, s, dev)
		sessionID := provisionSession(t, s, dev, jti, "web")
		if err := s.ConnectExecSession(ctx, sessionID, "owner-A"); err != nil {
			t.Fatal(err)
		}
		if ok, _ := s.ExecSessionAuthorized(ctx, sessionID); !ok {
			t.Fatal("session should be authorized before revocation")
		}
		if _, err := s.RevokeAccessDevice(ctx, dev); err != nil {
			t.Fatal(err)
		}
		// The execution-boundary fence refuses the session once its device is
		// revoked, independent of the cancel cascade.
		if ok, _ := s.ExecSessionAuthorized(ctx, sessionID); ok {
			t.Fatal("session must be fenced after its device is revoked")
		}
	})

	t.Run("NoSessionSurvivesItsCredentialRevocation", func(t *testing.T) {
		s := newStore(t)
		dev, jti := uuid.NewString(), uuid.NewString()
		registerDevice(t, s, dev)
		provisionSession(t, s, dev, jti, "web")

		if _, err := s.RevokeAccessToken(ctx, jti); err != nil {
			t.Fatal(err)
		}
		// No pending/running session may remain bound to the revoked credential.
		sessions, err := s.ListExecSessions(ctx, dev, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, sess := range sessions {
			if sess.TokenJTI == jti && (sess.Status == "pending" || sess.Status == "running") {
				t.Fatalf("session %s still active under revoked token", sess.ID)
			}
		}
	})
}
