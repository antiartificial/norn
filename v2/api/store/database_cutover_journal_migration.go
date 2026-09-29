package store

// Migration 46 reserves durable, private state for a future application
// database cutover operation. No API route or worker writes these rows yet.
const databaseCutoverJournalMigrationSQL = `
CREATE TABLE database_cutover_journals (
	operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
	app TEXT NOT NULL CHECK (app <> ''),
	logical_database TEXT NOT NULL CHECK (logical_database <> ''),
	intent JSONB NOT NULL CHECK (jsonb_typeof(intent) = 'object'),
	intent_sha256 CHAR(64) NOT NULL CHECK (intent_sha256 ~ '^[0-9a-f]{64}$'),
	phase TEXT NOT NULL DEFAULT 'prepare' CHECK (phase IN ('prepare','quiesce','final-sync','activate','verify','accept')),
	revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
	receipts JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(receipts) = 'object'),
	created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
	retired_at TIMESTAMPTZ,
	UNIQUE (operation_id, intent_sha256)
);
CREATE UNIQUE INDEX database_cutover_journals_active_resource
	ON database_cutover_journals(app,logical_database) WHERE retired_at IS NULL;
`

func databaseCutoverJournalMigration() SchemaMigration {
	return SchemaMigration{Version: 46, Name: "database-cutover-journal", SQL: databaseCutoverJournalMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: SnapshotExportIntentWriterVersion}
}
