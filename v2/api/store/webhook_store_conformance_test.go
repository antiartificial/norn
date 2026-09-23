package store_test

import (
	"context"
	"os"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestWebhookStoreConformance_Postgres runs the shared webhook-store conformance
// suite against PostgreSQL. Opt-in via NORN_TEST_DATABASE_URL.
func TestWebhookStoreConformance_Postgres(t *testing.T) {
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
	storetest.RunWebhookStoreConformance(t, func(t *testing.T) store.WebhookStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM webhook_deliveries"); err != nil {
			t.Fatalf("reset webhook_deliveries: %v", err)
		}
		return db
	})
}
