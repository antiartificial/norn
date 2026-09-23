package store_test

import (
	"context"
	"os"
	"testing"

	"norn/v2/api/store"
)

// TestMigrationOwnerAndCompatibilityGuard verifies migrations are idempotent,
// record the schema version, and refuse to run against a newer persisted schema.
// Opt-in via NORN_TEST_DATABASE_URL.
func TestMigrationOwnerAndCompatibilityGuard(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := store.Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	// Idempotent: running twice succeeds.
	if err := store.Migrate(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("second migrate should be idempotent: %v", err)
	}

	var version int
	if err := db.Pool.QueryRow(ctx, `SELECT version FROM control_schema_version WHERE id=1`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != store.SchemaVersion {
		t.Fatalf("recorded schema version = %d, want %d", version, store.SchemaVersion)
	}

	// Compatibility guard: a persisted version newer than the binary refuses.
	if _, err := db.Pool.Exec(ctx, `UPDATE control_schema_version SET version=$1 WHERE id=1`, store.SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err == nil {
		t.Fatal("migrate should refuse a newer persisted schema version")
	}
	// Restore so other suites are unaffected.
	if _, err := db.Pool.Exec(ctx, `UPDATE control_schema_version SET version=$1 WHERE id=1`, store.SchemaVersion); err != nil {
		t.Fatal(err)
	}
}
