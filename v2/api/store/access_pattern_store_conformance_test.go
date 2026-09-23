package store_test

import (
	"context"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestAccessPatternStoreConformance_Postgres runs the shared access-pattern
// suite against PG. Opt-in via NORN_TEST_DATABASE_URL.
func TestAccessPatternStoreConformance_Postgres(t *testing.T) {
	db := connectForConformance(t)
	storetest.RunAccessPatternStoreConformance(t, func(t *testing.T) store.AccessPatternStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM access_observation_buckets"); err != nil {
			t.Fatalf("reset access_observation_buckets: %v", err)
		}
		return db
	})
}
