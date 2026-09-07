package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestExpiredRunningExecSessionsReleaseQuota(t *testing.T) {
	dsn := os.Getenv("NORN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	deviceID, tokenID := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	if err := db.CreateAccessDevice(ctx, &AccessDevice{ID: deviceID, Name: "deadline test", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM exec_sessions WHERE device_id=$1`, deviceID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM step_up_challenges WHERE device_id=$1`, deviceID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM access_tokens WHERE device_id=$1`, deviceID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM access_devices WHERE id=$1`, deviceID)
	})
	if err := db.RecordAccessToken(ctx, &AccessToken{JTI: tokenID, DeviceID: deviceID, Subject: "deadline test", Scopes: []string{"apps:exec"}, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	create := func() string {
		t.Helper()
		challengeID, sessionID := uuid.NewString(), uuid.NewString()
		challenge := &StepUpChallenge{ID: challengeID, DeviceID: deviceID, TokenJTI: tokenID, Purpose: "exec", Resource: "test", NonceHash: "test", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := db.CreateStepUpChallenge(ctx, challenge); err != nil {
			t.Fatal(err)
		}
		if err := db.VerifyStepUpChallenge(ctx, challengeID, deviceID); err != nil {
			t.Fatal(err)
		}
		s := &ExecSession{ID: sessionID, DeviceID: deviceID, TokenJTI: tokenID, ChallengeID: challengeID, AppID: "test", AllocationID: "test", Task: "test", Command: []string{"true"}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := db.CreateExecSession(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := db.ConnectExecSession(ctx, sessionID); err != nil {
			t.Fatal(err)
		}
		return sessionID
	}
	ids := []string{create(), create(), create()}
	if _, err := db.Pool.Exec(ctx, `UPDATE exec_sessions SET expires_at=now()-interval '1 second' WHERE device_id=$1`, deviceID); err != nil {
		t.Fatal(err)
	}
	// No Get/List call before create: the admission transaction itself must reap
	// expired running sessions before applying the three-active-session quota.
	live := create()
	for _, id := range ids {
		s, err := db.GetExecSession(ctx, id)
		if err != nil || s.Status != "expired" {
			t.Fatalf("expired session=%+v err=%v", s, err)
		}
		if err := db.FinishExecSession(ctx, id, "completed", nil, ""); err != nil {
			t.Fatal(err)
		}
		s, err = db.GetExecSession(ctx, id)
		if err != nil || s.Status != "expired" {
			t.Fatalf("late completion overwrote expiry: %+v %v", s, err)
		}
	}
	if err := db.ExpireExecSessions(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := db.GetExecSession(ctx, live)
	if err != nil || s.Status != "running" {
		t.Fatalf("live session reaped: %+v %v", s, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE exec_sessions SET expires_at=now()-interval '1 second' WHERE id=$1`, live); err != nil {
		t.Fatal(err)
	}
	// The stream revocation watcher calls GetExecSession: it must observe a
	// terminal state at the hard deadline without another session being created.
	s, err = db.GetExecSession(ctx, live)
	if err != nil || s.Status != "expired" {
		t.Fatalf("watcher lookup failed to expire: %+v %v", s, err)
	}
}
