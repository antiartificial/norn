package store_test

import (
	"context"
	"os"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestNotificationStoreConformance_Postgres runs the shared notification-store
// conformance suite against PostgreSQL. Opt-in via NORN_TEST_DATABASE_URL.
func TestNotificationStoreConformance_Postgres(t *testing.T) {
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
	storetest.RunNotificationStoreConformance(t, func(t *testing.T) store.NotificationStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM notification_channels"); err != nil {
			t.Fatalf("reset notification_channels: %v", err)
		}
		return db
	})
}
