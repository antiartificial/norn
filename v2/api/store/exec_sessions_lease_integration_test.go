package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestExecSessionLeaseOwnership(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	// A second store models an already-running replica B. It must recover only
	// A's expired lease without requiring B itself to restart.
	dbB, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dbB.Close)

	ctx := context.Background()
	suffix := uuid.NewString()
	deviceID := "exec-lease-device-" + suffix
	now := time.Now().UTC()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO access_devices(id,name,created_at) VALUES($1,'exec lease test',$2)`, deviceID, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM exec_sessions WHERE device_id=$1`, deviceID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM step_up_challenges WHERE device_id=$1`, deviceID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM access_devices WHERE id=$1`, deviceID)
	})
	insert := func(name, ownerID, ownerToken string, leaseUntil, expiresAt time.Time) string {
		t.Helper()
		challengeID := "exec-lease-challenge-" + name + "-" + suffix
		sessionID := "exec-lease-session-" + name + "-" + suffix
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO step_up_challenges(id,device_id,token_jti,purpose,resource,nonce_hash,status,expires_at)
			VALUES($1,$2,'token','exec','atlas','digest','consumed',$3)
		`, challengeID, deviceID, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO exec_sessions(id,device_id,token_jti,challenge_id,app_id,allocation_id,task,status,expires_at,owner_id,owner_token,owner_lease_until)
			VALUES($1,$2,'token',$3,'atlas','alloc','web','running',$4,$5,$6,$7)
		`, sessionID, deviceID, challengeID, expiresAt, ownerID, ownerToken, nullableTime(leaseUntil)); err != nil {
			t.Fatal(err)
		}
		return sessionID
	}

	liveID := insert("live", "runtime-a", "token-a", now.Add(time.Minute), now.Add(time.Hour))
	expiredID := insert("expired", "runtime-b", "token-b", now.Add(-time.Minute), now.Add(time.Hour))
	legacyID := insert("legacy", "", "", time.Time{}, now.Add(time.Hour))
	staleID := insert("stale", "runtime-c", "token-c", now.Add(-time.Minute), now.Add(time.Hour))
	completedID := insert("completed", "runtime-d", "token-d", now.Add(time.Minute), now.Add(time.Hour))
	revokedID := insert("revoked", "runtime-e", "token-e", now.Add(time.Minute), now.Add(time.Hour))
	revokedTokenID := "exec-lease-revoked-token-" + suffix
	if err := db.RecordAccessToken(ctx, &AccessToken{JTI: revokedTokenID, DeviceID: deviceID, Subject: "exec lease test", IssuedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE exec_sessions SET token_jti=$2 WHERE id=$1`, revokedID, revokedTokenID); err != nil {
		t.Fatal(err)
	}

	// Re-running migration must not invalidate either newly owned or legacy
	// sessions. It only adds compatible schema/indexes.
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{liveID, expiredID, legacyID, staleID, completedID, revokedID} {
		var status string
		if err := db.Pool.QueryRow(ctx, `SELECT status FROM exec_sessions WHERE id=$1`, id).Scan(&status); err != nil || status != "running" {
			t.Fatalf("Migrate preserved session %s: status=%q err=%v", id, status, err)
		}
	}

	// A stale lease cannot be renewed or completed before a recovery loop gets
	// to it, so it cannot be resurrected by its former websocket.
	if renewed, err := db.RenewExecSession(ctx, staleID, "runtime-c", "token-c", time.Hour); err != nil || renewed {
		t.Fatalf("stale lease renewal renewed=%v err=%v", renewed, err)
	}
	if finished, err := db.FinishOwnedExecSession(ctx, staleID, "runtime-c", "token-c", "completed", nil, ""); err != nil || finished {
		t.Fatalf("stale lease finish finished=%v err=%v", finished, err)
	}
	if err := dbB.ReconcileExecSessions(ctx); err != nil {
		t.Fatal(err)
	}
	live, err := db.GetExecSession(ctx, liveID)
	if err != nil || live.Status != "running" {
		t.Fatalf("live owner was recovered: status=%q err=%v", live.Status, err)
	}
	expired, err := db.GetExecSession(ctx, expiredID)
	if err != nil || expired.Status != "failed" || expired.ErrorCode != "exec_owner_lost" {
		t.Fatalf("expired owner recovery: session=%+v err=%v", expired, err)
	}
	stale, err := db.GetExecSession(ctx, staleID)
	if err != nil || stale.Status != "failed" || stale.ErrorCode != "exec_owner_lost" {
		t.Fatalf("stale owner recovery: session=%+v err=%v", stale, err)
	}
	legacy, err := db.GetExecSession(ctx, legacyID)
	if err != nil || legacy.Status != "running" {
		t.Fatalf("legacy session was recovered: status=%q err=%v", legacy.Status, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE exec_sessions SET expires_at=now()-interval '1 second' WHERE id=$1`, legacyID); err != nil {
		t.Fatal(err)
	}
	if err := db.ExpireExecSessions(ctx); err != nil {
		t.Fatal(err)
	}
	legacy, err = db.GetExecSession(ctx, legacyID)
	if err != nil || legacy.Status != "expired" {
		t.Fatalf("legacy hard TTL: status=%q err=%v", legacy.Status, err)
	}

	if renewed, err := db.RenewExecSession(ctx, liveID, "", "", time.Hour); err == nil || renewed {
		t.Fatalf("empty renewal renewed=%v err=%v", renewed, err)
	}
	if finished, err := db.FinishOwnedExecSession(ctx, liveID, "", "", "completed", nil, ""); err == nil || finished {
		t.Fatalf("empty finish finished=%v err=%v", finished, err)
	}
	if finished, err := db.FinishOwnedExecSession(ctx, completedID, "runtime-d", "token-d", "completed", nil, ""); err != nil || !finished {
		t.Fatalf("current owner finish finished=%v err=%v", finished, err)
	}
	if renewed, err := db.RenewExecSession(ctx, liveID, "runtime-a", "wrong-token", time.Hour); err != nil || renewed {
		t.Fatalf("wrong renewal renewed=%v err=%v", renewed, err)
	}
	if finished, err := db.FinishOwnedExecSession(ctx, liveID, "runtime-a", "wrong-token", "completed", nil, ""); err != nil || finished {
		t.Fatalf("wrong finish finished=%v err=%v", finished, err)
	}
	if renewed, err := db.RenewExecSession(ctx, liveID, "runtime-a", "token-a", time.Hour); err != nil || !renewed {
		t.Fatalf("correct renewal renewed=%v err=%v", renewed, err)
	}
	if err := db.FinishExecSession(ctx, liveID, "canceled", nil, "exec_session_canceled"); err != nil {
		t.Fatal(err)
	}
	if finished, err := db.FinishOwnedExecSession(ctx, liveID, "runtime-a", "token-a", "completed", nil, ""); err != nil || finished {
		t.Fatalf("stale completion after cancel finished=%v err=%v", finished, err)
	}
	finished, err := db.GetExecSession(ctx, liveID)
	if err != nil || finished.Status != "canceled" {
		t.Fatalf("cancel must win stale completion: status=%q err=%v", finished.Status, err)
	}
	if _, err := db.RevokeAccessToken(ctx, revokedTokenID); err != nil {
		t.Fatal(err)
	}
	if staleFinish, err := db.FinishOwnedExecSession(ctx, revokedID, "runtime-e", "token-e", "completed", nil, ""); err != nil || staleFinish {
		t.Fatalf("stale completion after revoke finished=%v err=%v", staleFinish, err)
	}
	revoked, err := db.GetExecSession(ctx, revokedID)
	if err != nil || revoked.Status != "canceled" || revoked.ErrorCode != "token_revoked" {
		t.Fatalf("revoke must win stale completion: session=%+v err=%v", revoked, err)
	}
}

func nullableTime(value time.Time) interface{} {
	if value.IsZero() {
		return nil
	}
	return value
}
