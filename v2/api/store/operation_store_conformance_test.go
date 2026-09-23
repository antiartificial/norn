package store_test

import (
	"context"
	"os"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestOperationStoreConformance_Postgres runs the shared operation-store
// conformance suite (storetest.RunOperationStoreConformance) against the
// PostgreSQL adapter. Opt-in: set NORN_TEST_DATABASE_URL to a disposable test
// database. The in-memory adapter runs the same suite in memstore's tests.
func TestOperationStoreConformance_Postgres(t *testing.T) {
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
	storetest.RunOperationStoreConformance(t, func(t *testing.T) store.OperationStore {
		// Clear the operation queue and the deployment-aggregate tables its
		// recovery query references, so each subtest starts from an empty queue.
		for _, table := range []string{"deployment_steps", "deployment_regions", "deployments", "operations"} {
			if _, err := db.Pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
				t.Fatalf("reset %s: %v", table, err)
			}
		}
		return db
	})
}
