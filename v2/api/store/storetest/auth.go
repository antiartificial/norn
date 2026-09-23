package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/store"
)

// RunAuthAggregateConformance verifies the invariant that authorization
// revocation and cancellation of sessions it authorized have one outcome. It
// intentionally exercises the owner-token lifecycle: after a revoke or
// rotation, a previous owner can neither renew nor terminalize its session.
func RunAuthAggregateConformance(t *testing.T, newStore func(t *testing.T) store.AuthStore) {
	ctx := context.Background()
	future := func() time.Time { return time.Now().UTC().Add(time.Hour) }

	registerDevice := func(t *testing.T, s store.AuthStore, id string) {
		t.Helper()
		if err := s.CreateAccessDevice(ctx, &store.AccessDevice{ID: id, Name: "auth-conformance", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	provision := func(t *testing.T, s store.AuthStore, deviceID, tokenID, appID string) (string, store.ExecSessionClaim) {
		t.Helper()
		if err := s.RecordAccessToken(ctx, &store.AccessToken{JTI: tokenID, DeviceID: deviceID, Subject: "device", Scopes: []string{"api:read"}, IssuedAt: time.Now().UTC(), ExpiresAt: future()}); err != nil {
			t.Fatal(err)
		}
		challengeID := uuid.NewString()
		if err := s.CreateStepUpChallenge(ctx, &store.StepUpChallenge{ID: challengeID, DeviceID: deviceID, TokenJTI: tokenID, Purpose: "exec", Resource: appID, NonceHash: uuid.NewString(), CreatedAt: time.Now().UTC(), ExpiresAt: future()}); err != nil {
			t.Fatal(err)
		}
		if err := s.VerifyStepUpChallenge(ctx, challengeID, deviceID); err != nil {
			t.Fatal(err)
		}
		sessionID := uuid.NewString()
		if err := s.CreateExecSession(ctx, &store.ExecSession{ID: sessionID, DeviceID: deviceID, TokenJTI: tokenID, ChallengeID: challengeID, AppID: appID, AllocationID: "alloc", Task: "web", Command: []string{"sh"}, CommandDigest: "digest", CreatedAt: time.Now().UTC(), ExpiresAt: future()}); err != nil {
			t.Fatal(err)
		}
		claim := store.ExecSessionClaim{OwnerID: "auth-conformance", OwnerToken: uuid.NewString(), LeaseDuration: time.Minute}
		if claimed, err := s.ConnectExecSession(ctx, sessionID, claim); err != nil || !claimed {
			t.Fatalf("connect claimed=%v err=%v", claimed, err)
		}
		return sessionID, claim
	}
	assertCanceled := func(t *testing.T, s store.AuthStore, id string) {
		t.Helper()
		got, err := s.GetExecSession(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "canceled" {
			t.Fatalf("session %s status=%q, want canceled", id, got.Status)
		}
	}
	assertFenced := func(t *testing.T, s store.AuthStore, id string, claim store.ExecSessionClaim) {
		t.Helper()
		if renewed, err := s.RenewExecSession(ctx, id, claim.OwnerID, claim.OwnerToken, time.Minute); err != nil || renewed {
			t.Fatalf("revoked session renewed=%v err=%v", renewed, err)
		}
		if finished, err := s.FinishOwnedExecSession(ctx, id, claim.OwnerID, claim.OwnerToken, "completed", nil, ""); err != nil || finished {
			t.Fatalf("revoked session finished=%v err=%v", finished, err)
		}
	}

	t.Run("RevokeTokenCancelsAndFencesItsSessions", func(t *testing.T) {
		s := newStore(t)
		deviceID, tokenID := uuid.NewString(), uuid.NewString()
		registerDevice(t, s, deviceID)
		sessionID, claim := provision(t, s, deviceID, tokenID, "web")
		ids, err := s.RevokeAccessToken(ctx, tokenID)
		if err != nil || len(ids) != 1 || ids[0] != sessionID {
			t.Fatalf("revoke ids=%v err=%v", ids, err)
		}
		assertCanceled(t, s, sessionID)
		assertFenced(t, s, sessionID, claim)
		if active, err := s.AccessTokenActive(ctx, tokenID); err != nil || active {
			t.Fatalf("revoked token active=%v err=%v", active, err)
		}
	})

	t.Run("RotateCancelsAndFencesPreviousSessions", func(t *testing.T) {
		s := newStore(t)
		deviceID, previous := uuid.NewString(), uuid.NewString()
		registerDevice(t, s, deviceID)
		sessionID, claim := provision(t, s, deviceID, previous, "web")
		next := &store.AccessToken{JTI: uuid.NewString(), DeviceID: deviceID, Subject: "device", Scopes: []string{"api:read"}, IssuedAt: time.Now().UTC(), ExpiresAt: future(), RotatedFrom: previous}
		ids, err := s.RotateAccessToken(ctx, previous, next)
		if err != nil || len(ids) != 1 || ids[0] != sessionID {
			t.Fatalf("rotate ids=%v err=%v", ids, err)
		}
		assertCanceled(t, s, sessionID)
		assertFenced(t, s, sessionID, claim)
		if active, err := s.AccessTokenActive(ctx, next.JTI); err != nil || !active {
			t.Fatalf("replacement active=%v err=%v", active, err)
		}
	})

	t.Run("RevokeDeviceCancelsEveryCredentialSession", func(t *testing.T) {
		s := newStore(t)
		deviceID := uuid.NewString()
		registerDevice(t, s, deviceID)
		a, claimA := provision(t, s, deviceID, uuid.NewString(), "web")
		b, claimB := provision(t, s, deviceID, uuid.NewString(), "api")
		ids, err := s.RevokeAccessDevice(ctx, deviceID)
		if err != nil || len(ids) != 2 {
			t.Fatalf("revoke device ids=%v err=%v", ids, err)
		}
		assertCanceled(t, s, a)
		assertCanceled(t, s, b)
		assertFenced(t, s, a, claimA)
		assertFenced(t, s, b, claimB)
	})
}
