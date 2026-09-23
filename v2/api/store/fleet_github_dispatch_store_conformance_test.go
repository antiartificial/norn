package store_test

import (
	"context"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestFleetGitHubDispatchStoreConformance_Postgres runs the shared dispatch suite
// against PG. Opt-in via NORN_TEST_DATABASE_URL.
func TestFleetGitHubDispatchStoreConformance_Postgres(t *testing.T) {
	db := connectForConformance(t)
	storetest.RunFleetGitHubDispatchStoreConformance(t, func(t *testing.T) store.FleetGitHubDispatchStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM fleet_github_dispatches"); err != nil {
			t.Fatalf("reset fleet_github_dispatches: %v", err)
		}
		return db
	})
}
