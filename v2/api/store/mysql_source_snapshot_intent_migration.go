package store

// Source quiescence must start from signed input that exists before the SQL
// artifact. This permanent reservation is deliberately private and cannot be
// released by elapsed time or an ordinary failed operation recovery.
const MySQLSourceSnapshotWriterVersion int64 = 21

const mysqlSourceSnapshotIntentMigrationSQL = `
CREATE TABLE mysql_source_snapshot_intents (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
 acceptance_intent_id TEXT NOT NULL,
 catalog_revision BIGINT NOT NULL REFERENCES database_catalog_revisions(revision) ON DELETE RESTRICT,
 profile_id TEXT NOT NULL,
 logical_id TEXT NOT NULL,
 source_key TEXT NOT NULL UNIQUE,
 source JSONB NOT NULL,
 maintenance JSONB NOT NULL,
 job_identity JSONB NOT NULL,
 dump_tool_sha256 TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('quiesce-intended')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
`

func mysqlSourceSnapshotIntentMigration() SchemaMigration {
	return SchemaMigration{
		Version:              29,
		Name:                 "mysql-source-snapshot-intents",
		SQL:                  mysqlSourceSnapshotIntentMigrationSQL,
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion,
		MinimumWriterVersion: MySQLSourceSnapshotWriterVersion,
	}
}
