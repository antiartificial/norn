package store_test

import (
	"context"
	"os"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

func connectForConformance(t *testing.T) *store.DB {
	t.Helper()
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
	return db
}

// TestCronStoreConformance_Postgres runs the shared cron-store suite against PG.
func TestCronStoreConformance_Postgres(t *testing.T) {
	db := connectForConformance(t)
	storetest.RunCronStoreConformance(t, func(t *testing.T) store.CronStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM cron_states"); err != nil {
			t.Fatalf("reset cron_states: %v", err)
		}
		return db
	})
}

// TestFuncExecutionStoreConformance_Postgres runs the shared func-execution
// suite against PG.
func TestFuncExecutionStoreConformance_Postgres(t *testing.T) {
	db := connectForConformance(t)
	storetest.RunFuncExecutionStoreConformance(t, func(t *testing.T) store.FuncExecutionStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM func_executions"); err != nil {
			t.Fatalf("reset func_executions: %v", err)
		}
		return db
	})
}
