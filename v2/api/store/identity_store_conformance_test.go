package store_test

import (
	"context"
	"os"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestIdentityStoreConformance_Postgres runs the shared identity/revocation
// conformance suite against the PostgreSQL adapter. Opt-in via
// NORN_TEST_DATABASE_URL.
func TestIdentityStoreConformance_Postgres(t *testing.T) {
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
	storetest.RunIdentityStoreConformance(t, func(t *testing.T) store.IdentityStore {
		// Children before parents to respect foreign keys.
		for _, table := range []string{"exec_sessions", "access_tokens", "github_actions_assertion_uses", "access_grants", "access_enrollments", "access_devices"} {
			if _, err := db.Pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
				t.Fatalf("reset %s: %v", table, err)
			}
		}
		return db
	})
}
