package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TestExecAuthorizationLifecycle exercises the transaction boundaries that
// mocks cannot validate. It is opt-in so the normal unit suite needs no local
// PostgreSQL instance.
func TestExecAuthorizationLifecycle(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	// Register pool closure first: testing runs cleanups in LIFO order, so the
	// row cleanup registered below executes while the pool is still available.
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	suffix := uuid.NewString()
	deviceID := "security-device-" + suffix
	tokenID := "security-token-" + suffix
	rotateTokenID := "security-rotate-token-" + suffix
	replacementTokenID := "security-replacement-token-" + suffix
	challengeID := "security-challenge-" + suffix
	rotateChallengeID := "security-rotate-challenge-" + suffix
	sessionID := "security-session-" + suffix
	rotateSessionID := "security-rotate-session-" + suffix
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM exec_sessions WHERE id=$1`, sessionID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM exec_sessions WHERE id=$1`, rotateSessionID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM step_up_challenges WHERE id=$1`, challengeID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM step_up_challenges WHERE id=$1`, rotateChallengeID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM access_tokens WHERE jti=$1`, tokenID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM access_tokens WHERE jti IN ($1,$2)`, rotateTokenID, replacementTokenID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM access_devices WHERE id=$1`, deviceID)
	})

	now := time.Now().UTC()
	if err := db.CreateAccessDevice(ctx, &AccessDevice{ID: deviceID, Name: "security test", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordAccessToken(ctx, &AccessToken{
		JTI: tokenID, DeviceID: deviceID, Subject: "security test", Scopes: []string{"apps:exec"},
		IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	challenge := &StepUpChallenge{
		ID: challengeID, DeviceID: deviceID, TokenJTI: tokenID, Purpose: "exec", Resource: "atlas",
		NonceHash: "digest", Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	if err := db.CreateStepUpChallenge(ctx, challenge); err != nil {
		t.Fatal(err)
	}
	if err := db.VerifyStepUpChallenge(ctx, challengeID, deviceID); err != nil {
		t.Fatal(err)
	}

	baseSession := ExecSession{
		ID: sessionID, DeviceID: deviceID, TokenJTI: "different-token", ChallengeID: challengeID,
		AppID: "atlas", AllocationID: "alloc", Task: "web", Command: []string{"/bin/sh"},
		CommandDigest: "command-digest", Terminal: true, Columns: 80, Rows: 24,
		Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := db.CreateExecSession(ctx, &baseSession); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("different token consumed challenge: %v", err)
	}

	baseSession.TokenJTI = tokenID
	if err := db.CreateExecSession(ctx, &baseSession); err != nil {
		t.Fatal(err)
	}
	if err := db.ConnectExecSession(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	connected, err := db.GetExecSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(connected.Command) != 0 || connected.CommandDigest != "command-digest" {
		t.Fatalf("connected command=%v digest=%q", connected.Command, connected.CommandDigest)
	}
	// Starting/migrating a second replica must not reap the first one's stream.
	peer, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(peer.Close)
	if err := Migrate(peer); err != nil {
		t.Fatal(err)
	}
	preserved, err := db.GetExecSession(ctx, sessionID)
	if err != nil || preserved.Status != "running" {
		t.Fatalf("peer migration changed live session: session=%+v err=%v", preserved, err)
	}

	canceled, err := db.RevokeAccessToken(ctx, tokenID)
	if err != nil {
		t.Fatal(err)
	}
	if len(canceled) != 1 || canceled[0] != sessionID {
		t.Fatalf("canceled sessions = %v", canceled)
	}
	finished, err := db.GetExecSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "canceled" || finished.ErrorCode != "token_revoked" {
		t.Fatalf("revoked session status=%q error=%q", finished.Status, finished.ErrorCode)
	}

	if err := db.RecordAccessToken(ctx, &AccessToken{
		JTI: rotateTokenID, DeviceID: deviceID, Subject: "security rotation test", Scopes: []string{"apps:exec"},
		IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	rotateChallenge := &StepUpChallenge{
		ID: rotateChallengeID, DeviceID: deviceID, TokenJTI: rotateTokenID, Purpose: "exec", Resource: "atlas",
		NonceHash: "rotate-digest", Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	if err := db.CreateStepUpChallenge(ctx, rotateChallenge); err != nil {
		t.Fatal(err)
	}
	if err := db.VerifyStepUpChallenge(ctx, rotateChallengeID, deviceID); err != nil {
		t.Fatal(err)
	}
	rotateSession := ExecSession{
		ID: rotateSessionID, DeviceID: deviceID, TokenJTI: rotateTokenID, ChallengeID: rotateChallengeID,
		AppID: "atlas", AllocationID: "alloc", Task: "web", Command: []string{"/bin/sh"},
		CommandDigest: "rotate-command-digest", Terminal: true, Columns: 80, Rows: 24,
		Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := db.CreateExecSession(ctx, &rotateSession); err != nil {
		t.Fatal(err)
	}
	if err := db.ConnectExecSession(ctx, rotateSessionID); err != nil {
		t.Fatal(err)
	}
	replacement := &AccessToken{
		JTI: replacementTokenID, DeviceID: deviceID, Subject: "security rotation test", Scopes: []string{"apps:exec"},
		IssuedAt: now, ExpiresAt: now.Add(time.Hour), RotatedFrom: rotateTokenID,
	}
	rotatedSessions, err := db.RotateAccessToken(ctx, rotateTokenID, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if len(rotatedSessions) != 1 || rotatedSessions[0] != rotateSessionID {
		t.Fatalf("rotation canceled sessions = %v", rotatedSessions)
	}
	rotatedSession, err := db.GetExecSession(ctx, rotateSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if rotatedSession.Status != "canceled" || rotatedSession.ErrorCode != "token_rotated" {
		t.Fatalf("rotated session status=%q error=%q", rotatedSession.Status, rotatedSession.ErrorCode)
	}
}
