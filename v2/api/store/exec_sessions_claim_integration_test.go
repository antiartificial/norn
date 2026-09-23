package store

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestConnectExecSessionHasSingleClaimWinner(t *testing.T) {
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
	ctx := context.Background()
	suffix := uuid.NewString()
	deviceID := "exec-claim-device-" + suffix
	challengeID := "exec-claim-challenge-" + suffix
	sessionID := "exec-claim-session-" + suffix
	now := time.Now().UTC()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO access_devices(id,name,created_at) VALUES($1,'exec claim test',$2)`, deviceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO step_up_challenges(id,device_id,token_jti,purpose,resource,nonce_hash,status,expires_at) VALUES($1,$2,'token','exec','atlas','digest','consumed',$3)`, challengeID, deviceID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO exec_sessions(id,device_id,token_jti,challenge_id,app_id,allocation_id,task,status,expires_at) VALUES($1,$2,'token',$3,'atlas','alloc','web','pending',$4)`, sessionID, deviceID, challengeID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM exec_sessions WHERE id=$1`, sessionID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM step_up_challenges WHERE id=$1`, challengeID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM access_devices WHERE id=$1`, deviceID)
	})

	claims := []ExecSessionClaim{{OwnerID: "runtime-a", OwnerToken: "token-a", LeaseDuration: time.Minute}, {OwnerID: "runtime-b", OwnerToken: "token-b", LeaseDuration: time.Minute}}
	var wg sync.WaitGroup
	results := make(chan bool, len(claims))
	for _, claim := range claims {
		wg.Add(1)
		go func(claim ExecSessionClaim) {
			defer wg.Done()
			claimed, err := db.ConnectExecSession(ctx, sessionID, claim)
			if err != nil {
				t.Errorf("ConnectExecSession: %v", err)
				return
			}
			results <- claimed
		}(claim)
	}
	wg.Wait()
	close(results)
	winners := 0
	for claimed := range results {
		if claimed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners=%d, want 1", winners)
	}
}
