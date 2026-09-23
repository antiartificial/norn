package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/store"
)

// RunExecSessionStoreConformance is the backend-neutral behavioral contract for
// store.ExecSessionStore — step-up challenge verification, session creation
// gated on a verified challenge with a per-device active cap, connect/finish/
// expire lifecycle, and credential-bound cancellation.
//
// registerDevice records a live (non-revoked) device so its step-up challenges
// and exec sessions are valid — a required dependency because step_up_challenges
// and exec_sessions reference access_devices. That the exec-session boundary
// cannot stand up without an identity device is the concrete evidence that
// identity and exec-sessions are one coupled auth aggregate.
func RunExecSessionStoreConformance(t *testing.T, newStore func(t *testing.T) store.ExecSessionStore, registerDevice func(t *testing.T, deviceID string)) {
	ctx := context.Background()
	future := func() time.Time { return time.Now().Add(time.Hour) }

	mkVerifiedChallenge := func(t *testing.T, s store.ExecSessionStore, deviceID, appID, jti string) string {
		t.Helper()
		id := uuid.NewString()
		ch := &store.StepUpChallenge{
			ID: id, DeviceID: deviceID, TokenJTI: jti, Purpose: "exec", Resource: appID,
			NonceHash: uuid.NewString(), CreatedAt: time.Now(), ExpiresAt: future(),
		}
		if err := s.CreateStepUpChallenge(ctx, ch); err != nil {
			t.Fatal(err)
		}
		if err := s.VerifyStepUpChallenge(ctx, id, deviceID); err != nil {
			t.Fatal(err)
		}
		return id
	}
	newSession := func(deviceID, appID, jti, challengeID string) *store.ExecSession {
		return &store.ExecSession{
			ID: uuid.NewString(), DeviceID: deviceID, TokenJTI: jti, ChallengeID: challengeID,
			AppID: appID, AllocationID: "alloc", Task: "web", Command: []string{"sh"},
			CommandDigest: "digest", CreatedAt: time.Now(), ExpiresAt: future(),
		}
	}

	t.Run("StepUpChallengeVerifiesOnce", func(t *testing.T) {
		s := newStore(t)
		id := uuid.NewString()
		dev := uuid.NewString()
		registerDevice(t, dev)
		ch := &store.StepUpChallenge{ID: id, DeviceID: dev, TokenJTI: "jti", Purpose: "exec", Resource: "web", NonceHash: uuid.NewString(), CreatedAt: time.Now(), ExpiresAt: future()}
		if err := s.CreateStepUpChallenge(ctx, ch); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetStepUpChallenge(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "pending" {
			t.Fatalf("challenge status = %q, want pending", got.Status)
		}
		if err := s.VerifyStepUpChallenge(ctx, id, dev); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetStepUpChallenge(ctx, id); got.Status != "verified" {
			t.Fatalf("status after verify = %q", got.Status)
		}
		if err := s.VerifyStepUpChallenge(ctx, id, dev); err == nil {
			t.Fatal("verifying an already-verified challenge should error")
		}
	})

	t.Run("CreateRequiresVerifiedChallenge", func(t *testing.T) {
		s := newStore(t)
		dev, app, jti := uuid.NewString(), "web", "jti-1"
		registerDevice(t, dev)
		// No matching verified challenge -> rejected.
		if err := s.CreateExecSession(ctx, newSession(dev, app, jti, uuid.NewString())); err == nil {
			t.Fatal("create without a verified challenge should error")
		}
		ch := mkVerifiedChallenge(t, s, dev, app, jti)
		session := newSession(dev, app, jti, ch)
		if err := s.CreateExecSession(ctx, session); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetExecSession(ctx, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "pending" {
			t.Fatalf("new session status = %q, want pending", got.Status)
		}
	})

	t.Run("PerDeviceActiveCapEnforced", func(t *testing.T) {
		s := newStore(t)
		dev, app, jti := uuid.NewString(), "web", "jti-cap"
		registerDevice(t, dev)
		for i := 0; i < 3; i++ {
			ch := mkVerifiedChallenge(t, s, dev, app, jti)
			if err := s.CreateExecSession(ctx, newSession(dev, app, jti, ch)); err != nil {
				t.Fatalf("session %d: %v", i, err)
			}
		}
		ch := mkVerifiedChallenge(t, s, dev, app, jti)
		err := s.CreateExecSession(ctx, newSession(dev, app, jti, ch))
		if !errors.Is(err, store.ErrTooManyActiveExecSessions) {
			t.Fatalf("4th session error = %v, want ErrTooManyActiveExecSessions", err)
		}
	})

	t.Run("CancelByCredentialCancelsActive", func(t *testing.T) {
		s := newStore(t)
		dev, app, jti := uuid.NewString(), "web", "jti-cancel"
		registerDevice(t, dev)
		ch := mkVerifiedChallenge(t, s, dev, app, jti)
		session := newSession(dev, app, jti, ch)
		if err := s.CreateExecSession(ctx, session); err != nil {
			t.Fatal(err)
		}
		ids, err := s.CancelActiveExecSessions(ctx, "token_jti", jti, "token_revoked")
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != session.ID {
			t.Fatalf("cancel returned %v, want [%s]", ids, session.ID)
		}
		if got, _ := s.GetExecSession(ctx, session.ID); got.Status != "canceled" {
			t.Fatalf("session status after cancel = %q", got.Status)
		}
		if _, err := s.CancelActiveExecSessions(ctx, "bogus_column", jti, "x"); err == nil {
			t.Fatal("cancel with a non-allowlisted column should error")
		}
	})

	t.Run("FinishTransitionsSession", func(t *testing.T) {
		s := newStore(t)
		dev, app, jti := uuid.NewString(), "web", "jti-finish"
		registerDevice(t, dev)
		ch := mkVerifiedChallenge(t, s, dev, app, jti)
		session := newSession(dev, app, jti, ch)
		if err := s.CreateExecSession(ctx, session); err != nil {
			t.Fatal(err)
		}
		code := 0
		if err := s.FinishExecSession(ctx, session.ID, "completed", &code, ""); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetExecSession(ctx, session.ID); got.Status != "completed" {
			t.Fatalf("status after finish = %q", got.Status)
		}
	})

	t.Run("ExpiredSessionsAreExpired", func(t *testing.T) {
		s := newStore(t)
		dev, app, jti := uuid.NewString(), "web", "jti-expire"
		registerDevice(t, dev)
		ch := mkVerifiedChallenge(t, s, dev, app, jti)
		session := newSession(dev, app, jti, ch)
		session.ExpiresAt = time.Now().Add(-time.Minute)
		if err := s.CreateExecSession(ctx, session); err != nil {
			t.Fatal(err)
		}
		if err := s.ExpireExecSessions(ctx); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetExecSession(ctx, session.ID); got.Status != "expired" {
			t.Fatalf("status after expire = %q, want expired", got.Status)
		}
	})

	t.Run("ConnectTransitionsToRunningForLiveDevice", func(t *testing.T) {
		s := newStore(t)
		dev, app, jti := uuid.NewString(), "web", "jti-connect"
		registerDevice(t, dev)
		ch := mkVerifiedChallenge(t, s, dev, app, jti)
		session := newSession(dev, app, jti, ch)
		if err := s.CreateExecSession(ctx, session); err != nil {
			t.Fatal(err)
		}
		if err := s.ConnectExecSession(ctx, session.ID, "owner-A"); err != nil {
			t.Fatalf("connect for a live device failed: %v", err)
		}
		if got, _ := s.GetExecSession(ctx, session.ID); got.Status != "running" {
			t.Fatalf("status after connect = %q, want running", got.Status)
		}
		// A second connect on a non-pending session is rejected.
		if err := s.ConnectExecSession(ctx, session.ID, "owner-A"); err == nil {
			t.Fatal("connecting an already-running session should error")
		}
	})

	t.Run("RecoverFailsOwnedAndOrphansNotOthers", func(t *testing.T) {
		s := newStore(t)
		connect := func(owner string) string {
			dev, app, jti := uuid.NewString(), "web", uuid.NewString()
			registerDevice(t, dev)
			ch := mkVerifiedChallenge(t, s, dev, app, jti)
			session := newSession(dev, app, jti, ch)
			if err := s.CreateExecSession(ctx, session); err != nil {
				t.Fatal(err)
			}
			if err := s.ConnectExecSession(ctx, session.ID, owner); err != nil {
				t.Fatal(err)
			}
			return session.ID
		}
		mine := connect("owner-A")
		other := connect("owner-B")
		// This instance (owner-A) recovers after a restart.
		if err := s.RecoverExecSessions(ctx, "owner-A"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetExecSession(ctx, mine); got.Status != "failed" || got.ErrorCode != "server_restarted" {
			t.Fatalf("own orphaned session not recovered: status=%q code=%q", got.Status, got.ErrorCode)
		}
		if got, _ := s.GetExecSession(ctx, other); got.Status != "running" {
			t.Fatalf("another live instance's session was invalidated: status=%q", got.Status)
		}
	})
}
