package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// This opt-in integration test proves the PostgreSQL uniqueness boundary used
// by GitHub Actions exchange. It intentionally exercises concurrent callers;
// the normal unit suite does not require a local PostgreSQL service.
func TestConsumeGitHubActionsAssertionRejectsReplayAndConcurrentUse(t *testing.T) {
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
	issuer, jti := "https://token.actions.githubusercontent.com", "test-"+uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM github_actions_assertion_uses WHERE issuer=$1 AND jti=$2`, issuer, jti)
	})
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- db.ConsumeGitHubActionsAssertion(context.Background(), issuer, jti, time.Now().Add(time.Minute))
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result == nil {
			successes++
			continue
		}
		if !errors.Is(result, ErrGitHubActionsAssertionConsumed) {
			t.Fatalf("unexpected consume result: %v", result)
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent consumes = %d, want 1", successes)
	}
	if err := db.ConsumeGitHubActionsAssertion(context.Background(), issuer, jti, time.Now().Add(time.Minute)); !errors.Is(err, ErrGitHubActionsAssertionConsumed) {
		t.Fatalf("replay result = %v, want consumed", err)
	}
	expiredJTI := "expired-" + uuid.NewString()
	_, err = db.Pool.Exec(context.Background(), `INSERT INTO github_actions_assertion_uses(issuer,jti,expires_at) VALUES($1,$2,$3)`, issuer, expiredJTI, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ConsumeGitHubActionsAssertion(context.Background(), issuer, expiredJTI, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("expired assertion reservation was not pruned: %v", err)
	}
}
