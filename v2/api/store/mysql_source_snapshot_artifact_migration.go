package store

// Staging is an external effect. A stage-intended row is never retried
// automatically because the dump may have been written before an error.
const MySQLSourceArtifactWriterVersion int64 = 24

const mysqlSourceSnapshotArtifactMigrationSQL = `
ALTER TABLE mysql_source_snapshot_intents
 ADD COLUMN stage_intended_at TIMESTAMPTZ,
 ADD COLUMN stage_proved_at TIMESTAMPTZ,
 ADD COLUMN artifact_path TEXT,
 ADD COLUMN artifact JSONB,
 ADD COLUMN artifact_receipt_canonical BYTEA,
 ADD COLUMN artifact_receipt_sha256 TEXT,
 ADD COLUMN artifact_receipt_signing_algorithm TEXT,
 ADD COLUMN artifact_receipt_signing_key_id TEXT,
 ADD COLUMN artifact_receipt_signature TEXT;
ALTER TABLE mysql_source_snapshot_intents DROP CONSTRAINT mysql_source_snapshot_intents_state_check;
ALTER TABLE mysql_source_snapshot_intents ADD CONSTRAINT mysql_source_snapshot_intents_state_check
 CHECK (state IN ('quiesce-intended','stop-intended','stop-proved','lock-intended','lock-proved','stage-intended','stage-proved'));
ALTER TABLE mysql_source_snapshot_intents ADD CONSTRAINT mysql_source_snapshot_artifact_pair_check
 CHECK ((state='stage-proved' AND artifact_path IS NOT NULL AND artifact IS NOT NULL AND stage_proved_at IS NOT NULL
         AND artifact_receipt_canonical IS NOT NULL AND artifact_receipt_sha256 IS NOT NULL
         AND artifact_receipt_signing_algorithm IS NOT NULL AND artifact_receipt_signing_key_id IS NOT NULL AND artifact_receipt_signature IS NOT NULL)
    OR (state<>'stage-proved' AND artifact_path IS NULL AND artifact IS NULL AND stage_proved_at IS NULL
         AND artifact_receipt_canonical IS NULL AND artifact_receipt_sha256 IS NULL
         AND artifact_receipt_signing_algorithm IS NULL AND artifact_receipt_signing_key_id IS NULL AND artifact_receipt_signature IS NULL));
`

func mysqlSourceSnapshotArtifactMigration() SchemaMigration {
	return SchemaMigration{Version: 32, Name: "mysql-source-snapshot-artifact-receipt", SQL: mysqlSourceSnapshotArtifactMigrationSQL,
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion, MinimumWriterVersion: MySQLSourceArtifactWriterVersion}
}
