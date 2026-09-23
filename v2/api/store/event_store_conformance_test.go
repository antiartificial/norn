package store_test

import (
	"context"
	"os"
	"testing"

	"norn/v2/api/hub"
	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestEventStoreConformance_Postgres runs the shared event-store conformance
// suite (storetest.RunEventStoreConformance) against the PostgreSQL adapter.
// Opt-in via NORN_TEST_DATABASE_URL. The in-memory adapter runs the same suite
// in memstore's tests.
func TestEventStoreConformance_Postgres(t *testing.T) {
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
	storetest.RunEventStoreConformance(t, func(t *testing.T) hub.EventStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM control_events"); err != nil {
			t.Fatalf("reset control_events: %v", err)
		}
		return db
	})
}
