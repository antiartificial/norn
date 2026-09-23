package store_test

import (
	"context"
	"os"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestMutationAuditStoreConformance_Postgres runs the shared evidence-store
// conformance suite (storetest.RunMutationAuditStoreConformance) against the
// PostgreSQL adapter. Opt-in via NORN_TEST_DATABASE_URL.
func TestMutationAuditStoreConformance_Postgres(t *testing.T) {
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
	storetest.RunMutationAuditStoreConformance(t, func(t *testing.T) store.MutationAuditStore {
		for _, table := range []string{"mutation_audit_incidents", "mutation_audit_events"} {
			if _, err := db.Pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
				t.Fatalf("reset %s: %v", table, err)
			}
		}
		return db
	})
}
