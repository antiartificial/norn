package store_test

import (
	"context"
	"os"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestFleetAttemptStoreConformance_Postgres runs the shared Fleet-attempt
// conformance suite (storetest.RunFleetAttemptStoreConformance) against the
// PostgreSQL adapter. Opt-in via NORN_TEST_DATABASE_URL.
func TestFleetAttemptStoreConformance_Postgres(t *testing.T) {
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
	storetest.RunFleetAttemptStoreConformance(t, func(t *testing.T) store.FleetAttemptStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM fleet_runner_attempts"); err != nil {
			t.Fatalf("reset fleet_runner_attempts: %v", err)
		}
		return db
	})
}
