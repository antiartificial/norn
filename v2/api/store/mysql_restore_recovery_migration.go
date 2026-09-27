package store

// A recovery worker must commit a claim-bound checkpoint before touching
// MySQL. This first state has no external effect and cannot release a fence.
const MySQLRestoreRecoveryWriterVersion int64 = 28

const mysqlRestoreRecoveryMigrationSQL = `
CREATE TABLE mysql_restore_recovery_intents (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
 restore_operation_id TEXT NOT NULL UNIQUE REFERENCES mysql_restore_intents(operation_id) ON DELETE RESTRICT,
 acceptance_intent_id TEXT NOT NULL,
 catalog_revision BIGINT NOT NULL REFERENCES database_catalog_revisions(revision) ON DELETE RESTRICT,
 runtime_fence_epoch BIGINT NOT NULL CHECK (runtime_fence_epoch > 0),
 runtime_fence_owner TEXT NOT NULL,
 claim_owner TEXT NOT NULL,
 claim_generation BIGINT NOT NULL CHECK (claim_generation > 0),
 state TEXT NOT NULL CHECK (state IN ('prepared')),
 prepared_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK (runtime_fence_owner = 'mysql-restore:' || restore_operation_id),
 CHECK (claim_owner <> '')
);
`

func mysqlRestoreRecoveryMigration() SchemaMigration {
	return SchemaMigration{Version: 36, Name: "mysql-restore-recovery-intents", SQL: mysqlRestoreRecoveryMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: MySQLRestoreRecoveryWriterVersion}
}
