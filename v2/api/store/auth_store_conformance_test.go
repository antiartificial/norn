package store_test

import (
	"context"
	"os"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestAuthAggregateConformance_Postgres runs the cross-boundary auth aggregate
// suite (atomic revoke-plus-cancel, the ADR 0007 invariant) against the
// PostgreSQL adapter. The PG adapter and this suite are the contract a second
// backend must reproduce. Opt-in via NORN_TEST_DATABASE_URL.
func TestAuthAggregateConformance_Postgres(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := store.Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	storetest.RunAuthAggregateConformance(t, func(t *testing.T) store.AuthStore {
		for _, table := range []string{"exec_sessions", "step_up_challenges", "access_tokens", "access_devices"} {
			if _, err := db.Pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
				t.Fatalf("reset %s: %v", table, err)
			}
		}
		return db
	})
}
