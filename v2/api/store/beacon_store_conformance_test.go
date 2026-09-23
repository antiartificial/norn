package store_test

import (
	"context"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestBeaconStoreConformance_Postgres runs the shared beacon suite against PG.
// Opt-in via NORN_TEST_DATABASE_URL.
func TestBeaconStoreConformance_Postgres(t *testing.T) {
	db := connectForConformance(t)
	storetest.RunBeaconStoreConformance(t, func(t *testing.T) store.BeaconStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM beacon_events"); err != nil {
			t.Fatalf("reset beacon_events: %v", err)
		}
		return db
	})
}
