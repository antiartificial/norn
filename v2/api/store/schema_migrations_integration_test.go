package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSchemaMigratorEmptyInitializationAndRepeatNoOp(t *testing.T) {
	pools := schemaMigrationTestPools(t, 1)
	definitions := schemaTestDefinitions(1, 1)
	migrator := newSchemaTestMigrator(t, pools[0], definitions, BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 1}, SchemaMigratorOptions{})

	status, err := migrator.Migrate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.CurrentMigrationVersion != 2 || !equalVersions(status.AppliedVersions, []int64{1, 2}) {
		t.Fatalf("first status = %+v", status)
	}
	var ledgerRows int
	var updatedAt time.Time
	if err := pools[0].QueryRow(context.Background(), `SELECT count(*) FROM norn_schema_migrations`).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if err := pools[0].QueryRow(context.Background(), `SELECT updated_at FROM norn_schema_compatibility WHERE singleton`).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 2 {
		t.Fatalf("ledger rows = %d, want 2", ledgerRows)
	}

	second, err := migrator.Migrate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.AppliedVersions) != 0 {
		t.Fatalf("repeat applied versions = %v, want none", second.AppliedVersions)
	}
	var repeatedUpdatedAt time.Time
	if err := pools[0].QueryRow(context.Background(), `SELECT updated_at FROM norn_schema_compatibility WHERE singleton`).Scan(&repeatedUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if !repeatedUpdatedAt.Equal(updatedAt) {
		t.Fatalf("repeat migration rewrote compatibility timestamp: %s -> %s", updatedAt, repeatedUpdatedAt)
	}
	checked, err := migrator.Check(context.Background(), SchemaAccessReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if checked.CurrentMigrationVersion != 2 || len(checked.AppliedVersions) != 0 {
		t.Fatalf("checked status = %+v", checked)
	}
}

func TestSchemaMigratorAdoptsLegacySchemaWithoutChangingRowsOrBytes(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE legacy_control_fixture (
			id TEXT PRIMARY KEY,
			identity_bytes BYTEA NOT NULL,
			signed_receipt_bytes BYTEA NOT NULL,
			label TEXT NOT NULL
		)`); err != nil {
		t.Fatal(err)
	}
	identity := []byte{0, 1, 2, 0xff, 0x10}
	receipt := []byte("signed\x00receipt\xffbytes")
	if _, err := pool.Exec(ctx, `INSERT INTO legacy_control_fixture VALUES ('stable-id', $1, $2, 'preserve me')`, identity, receipt); err != nil {
		t.Fatal(err)
	}
	definitions := []SchemaMigration{{
		Version: 1,
		Name:    "adopt legacy control schema",
		SQL: `CREATE TABLE IF NOT EXISTS legacy_control_fixture (
			id TEXT PRIMARY KEY,
			identity_bytes BYTEA NOT NULL,
			signed_receipt_bytes BYTEA NOT NULL,
			label TEXT NOT NULL
		);
		ALTER TABLE legacy_control_fixture ADD COLUMN IF NOT EXISTS additive_note TEXT NOT NULL DEFAULT '';`,
		MinimumReaderVersion: 1,
		MinimumWriterVersion: 1,
	}}
	migrator := newSchemaTestMigrator(t, pool, definitions, BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 1}, SchemaMigratorOptions{})
	if _, err := migrator.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var gotID, gotLabel, gotNote string
	var gotIdentity, gotReceipt []byte
	if err := pool.QueryRow(ctx, `SELECT id, identity_bytes, signed_receipt_bytes, label, additive_note FROM legacy_control_fixture`).Scan(&gotID, &gotIdentity, &gotReceipt, &gotLabel, &gotNote); err != nil {
		t.Fatal(err)
	}
	if gotID != "stable-id" || gotLabel != "preserve me" || gotNote != "" || !bytes.Equal(gotIdentity, identity) || !bytes.Equal(gotReceipt, receipt) {
		t.Fatalf("legacy row changed: id=%q identity=%x receipt=%x label=%q note=%q", gotID, gotIdentity, gotReceipt, gotLabel, gotNote)
	}
}

func TestSchemaMigratorTwoPoolsRaceAppliesExactlyOnce(t *testing.T) {
	pools := schemaMigrationTestPools(t, 2)
	definitions := []SchemaMigration{
		{Version: 1, Name: "race table", SQL: `CREATE TABLE schema_race_fixture (marker TEXT PRIMARY KEY)`, MinimumReaderVersion: 1, MinimumWriterVersion: 1},
		{Version: 2, Name: "race marker", SQL: `INSERT INTO schema_race_fixture(marker) VALUES ('applied-once')`, MinimumReaderVersion: 1, MinimumWriterVersion: 1},
	}
	migrators := []*SchemaMigrator{
		newSchemaTestMigrator(t, pools[0], definitions, BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 1}, SchemaMigratorOptions{}),
		newSchemaTestMigrator(t, pools[1], definitions, BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 1}, SchemaMigratorOptions{}),
	}
	start := make(chan struct{})
	results := make(chan SchemaStatus, 2)
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	for _, migrator := range migrators {
		wait.Add(1)
		go func(item *SchemaMigrator) {
			defer wait.Done()
			<-start
			status, err := item.Migrate(context.Background())
			if err != nil {
				errorsFound <- err
				return
			}
			results <- status
		}(migrator)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("racing migration: %v", err)
	}
	appliers := 0
	for status := range results {
		if len(status.AppliedVersions) > 0 {
			appliers++
		}
	}
	if appliers != 1 {
		t.Fatalf("migration appliers = %d, want 1", appliers)
	}
	var markers, ledgerRows int
	if err := pools[0].QueryRow(context.Background(), `SELECT count(*) FROM schema_race_fixture`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if err := pools[0].QueryRow(context.Background(), `SELECT count(*) FROM norn_schema_migrations`).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if markers != 1 || ledgerRows != 2 {
		t.Fatalf("markers=%d ledgerRows=%d, want 1 and 2", markers, ledgerRows)
	}
}

func TestSchemaMigratorDDLAndLedgerFailureRollsBackAtomically(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	definitions := []SchemaMigration{
		{Version: 1, Name: "first DDL", SQL: `CREATE TABLE atomic_first_fixture (id BIGINT PRIMARY KEY)`, MinimumReaderVersion: 1, MinimumWriterVersion: 1},
		{Version: 2, Name: "failing DDL", SQL: `CREATE TABLE atomic_second_fixture (id BIGINT PRIMARY KEY); INSERT INTO table_that_does_not_exist VALUES (1)`, MinimumReaderVersion: 1, MinimumWriterVersion: 1},
	}
	migrator := newSchemaTestMigrator(t, pool, definitions, BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 1}, SchemaMigratorOptions{})
	_, err := migrator.Migrate(context.Background())
	var applyErr *MigrationApplyError
	if !errors.As(err, &applyErr) || applyErr.Version != 2 {
		t.Fatalf("error = %T %v, want migration 2 apply error", err, err)
	}
	for _, table := range []string{"atomic_first_fixture", "atomic_second_fixture", "norn_schema_migrations", "norn_schema_compatibility"} {
		var present bool
		if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&present); err != nil {
			t.Fatal(err)
		}
		if present {
			t.Errorf("%s survived failed atomic migration", table)
		}
	}
}

func TestSchemaMigratorLockTimeoutAndRelease(t *testing.T) {
	pools := schemaMigrationTestPools(t, 2)
	ctx := context.Background()
	holder, err := pools[0].Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, defaultSchemaMigrationLockKey); err != nil {
		_ = holder.Rollback(ctx)
		t.Fatal(err)
	}
	migrator := newSchemaTestMigrator(t, pools[1], schemaTestDefinitions(1, 1), BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 1}, SchemaMigratorOptions{LockTimeout: 100 * time.Millisecond})
	_, err = migrator.Migrate(ctx)
	var timeoutErr *MigrationLockTimeoutError
	if !errors.As(err, &timeoutErr) {
		_ = holder.Rollback(ctx)
		t.Fatalf("error = %T %v, want *MigrationLockTimeoutError", err, err)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Migrate(ctx); err != nil {
		t.Fatalf("migration after lock release: %v", err)
	}
	// A successful transaction-scoped owner must also release at commit.
	if _, err := migrator.Migrate(ctx); err != nil {
		t.Fatalf("repeat after migration commit: %v", err)
	}
}

func TestSchemaCheckIsReadOnlyAndRejectsAbsentCorruptAndIncompatibleMetadata(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		pool := schemaMigrationTestPools(t, 1)[0]
		migrator := newSchemaTestMigrator(t, pool, schemaTestDefinitions(1, 1), BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 1}, SchemaMigratorOptions{})
		_, err := migrator.Check(context.Background(), SchemaAccessReadOnly)
		var metadataErr *SchemaMetadataError
		if !errors.As(err, &metadataErr) || metadataErr.Kind != SchemaMetadataAbsent {
			t.Fatalf("error = %T %v, want absent metadata", err, err)
		}
		for _, table := range []string{"norn_schema_migrations", "norn_schema_compatibility"} {
			var present bool
			if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&present); err != nil {
				t.Fatal(err)
			}
			if present {
				t.Fatalf("read-only check created %s", table)
			}
		}
	})

	t.Run("partial metadata", func(t *testing.T) {
		pool := schemaMigrationTestPools(t, 1)[0]
		if _, err := pool.Exec(context.Background(), `CREATE TABLE norn_schema_migrations (version BIGINT)`); err != nil {
			t.Fatal(err)
		}
		migrator := newSchemaTestMigrator(t, pool, schemaTestDefinitions(1, 1), BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 1}, SchemaMigratorOptions{})
		_, err := migrator.Check(context.Background(), SchemaAccessReadOnly)
		var metadataErr *SchemaMetadataError
		if !errors.As(err, &metadataErr) || metadataErr.Kind != SchemaMetadataCorrupt {
			t.Fatalf("error = %T %v, want corrupt metadata", err, err)
		}
		var compatibilityPresent bool
		if err := pool.QueryRow(context.Background(), `SELECT to_regclass('norn_schema_compatibility') IS NOT NULL`).Scan(&compatibilityPresent); err != nil {
			t.Fatal(err)
		}
		if compatibilityPresent {
			t.Fatal("read-only check repaired corrupt metadata")
		}
	})

	t.Run("reader and writer compatibility", func(t *testing.T) {
		pool := schemaMigrationTestPools(t, 1)[0]
		definitions := []SchemaMigration{{Version: 1, Name: "new contract", SQL: `CREATE TABLE compatibility_fixture (id BIGINT)`, MinimumReaderVersion: 2, MinimumWriterVersion: 3}}
		current := newSchemaTestMigrator(t, pool, definitions, BinarySchemaCompatibility{ReaderVersion: 3, WriterVersion: 3}, SchemaMigratorOptions{})
		if _, err := current.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		var before time.Time
		if err := pool.QueryRow(context.Background(), `SELECT updated_at FROM norn_schema_compatibility`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		oldReader := newSchemaTestMigrator(t, pool, definitions, BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 3}, SchemaMigratorOptions{})
		_, err := oldReader.Check(context.Background(), SchemaAccessReadOnly)
		var compatibilityErr *SchemaCompatibilityError
		if !errors.As(err, &compatibilityErr) || compatibilityErr.Contract != "reader" {
			t.Fatalf("reader error = %T %v", err, err)
		}
		oldWriter := newSchemaTestMigrator(t, pool, definitions, BinarySchemaCompatibility{ReaderVersion: 3, WriterVersion: 2}, SchemaMigratorOptions{})
		if _, err := oldWriter.Check(context.Background(), SchemaAccessReadOnly); err != nil {
			t.Fatalf("read-only writer check should pass: %v", err)
		}
		_, err = oldWriter.Check(context.Background(), SchemaAccessReadWrite)
		if !errors.As(err, &compatibilityErr) || compatibilityErr.Contract != "writer" {
			t.Fatalf("writer error = %T %v", err, err)
		}
		var after time.Time
		if err := pool.QueryRow(context.Background(), `SELECT updated_at FROM norn_schema_compatibility`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if !after.Equal(before) {
			t.Fatalf("read-only checks rewrote metadata: %s -> %s", before, after)
		}
	})
}

func TestSchemaMigratorRejectsPreexistingEmptyMetadataInsteadOfAdoptingIt(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	if _, err := pool.Exec(context.Background(), schemaMetadataDDL); err != nil {
		t.Fatal(err)
	}
	migrator := newSchemaTestMigrator(t, pool, schemaTestDefinitions(1, 1), BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 1}, SchemaMigratorOptions{})
	_, err := migrator.Migrate(context.Background())
	var metadataErr *SchemaMetadataError
	if !errors.As(err, &metadataErr) || metadataErr.Kind != SchemaMetadataCorrupt {
		t.Fatalf("error = %T %v, want corrupt metadata", err, err)
	}
	var ledgerRows, compatibilityRows int
	if err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM norn_schema_migrations), (SELECT count(*) FROM norn_schema_compatibility)`).Scan(&ledgerRows, &compatibilityRows); err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 0 || compatibilityRows != 0 {
		t.Fatalf("corrupt empty metadata was changed: ledger=%d compatibility=%d", ledgerRows, compatibilityRows)
	}
}

func TestSchemaMigratorDetectsChecksumAndLedgerGap(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	definitions := append(schemaTestDefinitions(1, 1), SchemaMigration{Version: 3, Name: "third", SQL: `ALTER TABLE schema_test_fixture ADD COLUMN third TEXT NOT NULL DEFAULT ''`, MinimumReaderVersion: 1, MinimumWriterVersion: 1})
	migrator := newSchemaTestMigrator(t, pool, definitions, BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 1}, SchemaMigratorOptions{})
	if _, err := migrator.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	original := MigrationChecksum(definitions[0])
	if _, err := pool.Exec(context.Background(), `UPDATE norn_schema_migrations SET checksum=$1 WHERE version=1`, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	_, err := migrator.Check(context.Background(), SchemaAccessReadWrite)
	var checksumErr *MigrationChecksumError
	if !errors.As(err, &checksumErr) || checksumErr.Version != 1 {
		t.Fatalf("checksum error = %T %v", err, err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE norn_schema_migrations SET checksum=$1 WHERE version=1`, original); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM norn_schema_migrations WHERE version=2`); err != nil {
		t.Fatal(err)
	}
	_, err = migrator.Check(context.Background(), SchemaAccessReadWrite)
	var historyErr *MigrationHistoryError
	if !errors.As(err, &historyErr) {
		t.Fatalf("gap error = %T %v, want *MigrationHistoryError", err, err)
	}
}

func TestSchemaCompatibilityAllowsOldDefinitionsUntilMinimumRaised(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	oldDefinitions := []SchemaMigration{{Version: 1, Name: "old baseline", SQL: `CREATE TABLE mixed_version_fixture (id BIGINT PRIMARY KEY)`, MinimumReaderVersion: 1, MinimumWriterVersion: 1}}
	oldBinary := newSchemaTestMigrator(t, pool, oldDefinitions, BinarySchemaCompatibility{ReaderVersion: 1, WriterVersion: 1}, SchemaMigratorOptions{})
	if _, err := oldBinary.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	windowDefinitions := append(append([]SchemaMigration(nil), oldDefinitions...), SchemaMigration{
		Version: 2, Name: "compatible expansion", SQL: `ALTER TABLE mixed_version_fixture ADD COLUMN note TEXT NOT NULL DEFAULT ''`, MinimumReaderVersion: 1, MinimumWriterVersion: 1,
	})
	newBinary := newSchemaTestMigrator(t, pool, windowDefinitions, BinarySchemaCompatibility{ReaderVersion: 2, WriterVersion: 2}, SchemaMigratorOptions{})
	if _, err := newBinary.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := oldBinary.Check(context.Background(), SchemaAccessReadWrite)
	if err != nil {
		t.Fatalf("old catalog rejected during compatible window: %v", err)
	}
	if status.CurrentMigrationVersion != 2 {
		t.Fatalf("old catalog observed migration version %d, want 2", status.CurrentMigrationVersion)
	}

	raisedDefinitions := append(append([]SchemaMigration(nil), windowDefinitions...), SchemaMigration{
		Version: 3, Name: "retire old contract", SQL: `ALTER TABLE mixed_version_fixture ADD COLUMN generation BIGINT NOT NULL DEFAULT 0`, MinimumReaderVersion: 2, MinimumWriterVersion: 2,
	})
	raisedBinary := newSchemaTestMigrator(t, pool, raisedDefinitions, BinarySchemaCompatibility{ReaderVersion: 2, WriterVersion: 2}, SchemaMigratorOptions{})
	if _, err := raisedBinary.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = oldBinary.Check(context.Background(), SchemaAccessReadOnly)
	var compatibilityErr *SchemaCompatibilityError
	if !errors.As(err, &compatibilityErr) || compatibilityErr.Required != 2 || compatibilityErr.Provided != 1 {
		t.Fatalf("old binary after raised minimum = %T %v", err, err)
	}
}

func schemaTestDefinitions(minimumReader, minimumWriter int64) []SchemaMigration {
	return []SchemaMigration{
		{Version: 1, Name: "test baseline", SQL: `CREATE TABLE schema_test_fixture (id BIGINT PRIMARY KEY, payload BYTEA NOT NULL)`, MinimumReaderVersion: minimumReader, MinimumWriterVersion: minimumWriter},
		{Version: 2, Name: "test expansion", SQL: `ALTER TABLE schema_test_fixture ADD COLUMN label TEXT NOT NULL DEFAULT ''`, MinimumReaderVersion: minimumReader, MinimumWriterVersion: minimumWriter},
	}
}

func newSchemaTestMigrator(t *testing.T, pool *pgxpool.Pool, definitions []SchemaMigration, compatibility BinarySchemaCompatibility, options SchemaMigratorOptions) *SchemaMigrator {
	t.Helper()
	migrator, err := NewSchemaMigrator(pool, definitions, compatibility, options)
	if err != nil {
		t.Fatal(err)
	}
	return migrator
}

func schemaMigrationTestPools(t *testing.T, count int) []*pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "norn_schema_migration_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	pools := make([]*pgxpool.Pool, 0, count)
	for range count {
		config, err := pgxpool.ParseConfig(databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		config.ConnConfig.RuntimeParams["search_path"] = schema
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			t.Fatal(err)
		}
		pools = append(pools, pool)
	}
	t.Cleanup(func() {
		for _, pool := range pools {
			pool.Close()
		}
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		admin.Close()
	})
	return pools
}

func equalVersions(actual, expected []int64) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range actual {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}
