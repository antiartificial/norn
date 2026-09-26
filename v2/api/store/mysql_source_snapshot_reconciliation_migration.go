package store

// A reconciled predecessor remains inspectable after its physical source row
// moves to a newly signed, one-attempt source operation. The table is additive;
// migration alone does not retire the Mini rollback reader/writer contract.

const mysqlSourceSnapshotReconciliationMigrationSQL = `
CREATE TABLE mysql_source_snapshot_reconciliations (
 prior_operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
 successor_operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE RESTRICT,
 prior_intent JSONB NOT NULL,
 checkpoint TEXT NOT NULL CHECK (checkpoint IN ('stop-intended','lock-intended')),
 proof_canonical BYTEA NOT NULL,
 proof_sha256 TEXT NOT NULL CHECK (proof_sha256 ~ '^[0-9a-f]{64}$'),
 proof_signing_algorithm TEXT NOT NULL,
 proof_signing_key_id TEXT NOT NULL,
 proof_signature TEXT NOT NULL,
 transferred_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
`

func mysqlSourceSnapshotReconciliationMigration() SchemaMigration {
	return SchemaMigration{Version: 40, Name: "mysql-source-snapshot-reconciled-predecessor",
		SQL:                  mysqlSourceSnapshotReconciliationMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion,
		MinimumWriterVersion: SnapshotExportIntentWriterVersion}
}

// A failed successor can itself become the signed predecessor after its
// transfer committed. Its proved stop/lock state is a safe read-only restart
// checkpoint; later one-way stage and publication states remain excluded.
func mysqlSourceSnapshotProvedSuccessorMigration() SchemaMigration {
	return SchemaMigration{Version: 41, Name: "mysql-source-snapshot-proved-successor",
		SQL: `ALTER TABLE mysql_source_snapshot_reconciliations
		DROP CONSTRAINT mysql_source_snapshot_reconciliations_checkpoint_check;
		ALTER TABLE mysql_source_snapshot_reconciliations
		ADD CONSTRAINT mysql_source_snapshot_reconciliations_checkpoint_check
		CHECK (checkpoint IN ('stop-intended','lock-intended','stop-proved','lock-proved'));`,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion,
		MinimumWriterVersion: SnapshotExportIntentWriterVersion}
}

// An unsigned local dump may exist at stage-intended. It is never adopted as
// a source receipt; a freshly proved successor stages new bytes under its own
// operation after the original stopped/locked source is observed again.
func mysqlSourceSnapshotStageSuccessorMigration() SchemaMigration {
	return SchemaMigration{Version: 42, Name: "mysql-source-snapshot-stage-successor",
		SQL: `ALTER TABLE mysql_source_snapshot_reconciliations
		DROP CONSTRAINT mysql_source_snapshot_reconciliations_checkpoint_check;
		ALTER TABLE mysql_source_snapshot_reconciliations
		ADD CONSTRAINT mysql_source_snapshot_reconciliations_checkpoint_check
		CHECK (checkpoint IN ('stop-intended','lock-intended','stop-proved','lock-proved','stage-intended'));`,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion,
		MinimumWriterVersion: SnapshotExportIntentWriterVersion}
}
