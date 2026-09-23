package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestExecSessionStoreConformance_Postgres runs the shared exec-session
// conformance suite against the PostgreSQL adapter. Opt-in via
// NORN_TEST_DATABASE_URL.
func TestExecSessionStoreConformance_Postgres(t *testing.T) {
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
	storetest.RunExecSessionStoreConformance(t, func(t *testing.T) store.ExecSessionStore {
		for _, table := range []string{"exec_sessions", "step_up_challenges", "access_devices"} {
			if _, err := db.Pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
				t.Fatalf("reset %s: %v", table, err)
			}
		}
		return db
	}, func(t *testing.T, deviceID string) {
		if err := db.CreateAccessDevice(context.Background(), &store.AccessDevice{ID: deviceID, Name: "conf-device", CreatedAt: time.Now()}); err != nil {
			t.Fatalf("register device: %v", err)
		}
	})
}
