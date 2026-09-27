package store

// MySQLRetainedArtifactReaderVersion is the first reader that knows a local
// stage receipt is audit evidence only once a retention intent exists. Older
// readers could mistake stage-proved/local-path evidence for retained bytes.
const MySQLRetainedArtifactReaderVersion int64 = 5

const MySQLRetainedArtifactWriterVersion int64 = 27

const mysqlSourceSnapshotRetentionMigrationSQL = `
ALTER TABLE mysql_source_snapshot_intents
 ADD COLUMN artifact_publish_intended_at TIMESTAMPTZ,
 ADD COLUMN artifact_retained_proved_at TIMESTAMPTZ,
 ADD COLUMN retained_artifact JSONB,
 ADD COLUMN retention_receipt_canonical BYTEA,
 ADD COLUMN retention_receipt_sha256 TEXT,
 ADD COLUMN retention_receipt_signing_algorithm TEXT,
 ADD COLUMN retention_receipt_signing_key_id TEXT,
 ADD COLUMN retention_receipt_signature TEXT;
ALTER TABLE mysql_source_snapshot_intents DROP CONSTRAINT mysql_source_snapshot_intents_state_check;
ALTER TABLE mysql_source_snapshot_intents ADD CONSTRAINT mysql_source_snapshot_intents_state_check
 CHECK (state IN ('quiesce-intended','stop-intended','stop-proved','lock-intended','lock-proved','stage-intended','stage-proved','publish-intended','retained-proved'));
ALTER TABLE mysql_source_snapshot_intents DROP CONSTRAINT mysql_source_snapshot_artifact_pair_check;
ALTER TABLE mysql_source_snapshot_intents ADD CONSTRAINT mysql_source_snapshot_artifact_pair_check
 CHECK (
   (state IN ('stage-proved','publish-intended','retained-proved')
     AND artifact_path IS NOT NULL AND artifact IS NOT NULL AND stage_proved_at IS NOT NULL
     AND artifact_receipt_canonical IS NOT NULL AND artifact_receipt_sha256 IS NOT NULL
     AND artifact_receipt_signing_algorithm IS NOT NULL AND artifact_receipt_signing_key_id IS NOT NULL AND artifact_receipt_signature IS NOT NULL)
   OR
   (state NOT IN ('stage-proved','publish-intended','retained-proved')
     AND artifact_path IS NULL AND artifact IS NULL AND stage_proved_at IS NULL
     AND artifact_receipt_canonical IS NULL AND artifact_receipt_sha256 IS NULL
     AND artifact_receipt_signing_algorithm IS NULL AND artifact_receipt_signing_key_id IS NULL AND artifact_receipt_signature IS NULL)
 );
ALTER TABLE mysql_source_snapshot_intents ADD CONSTRAINT mysql_source_snapshot_retention_pair_check
 CHECK (
   (state='stage-proved'
     AND artifact_publish_intended_at IS NULL AND artifact_retained_proved_at IS NULL AND retained_artifact IS NULL
     AND retention_receipt_canonical IS NULL AND retention_receipt_sha256 IS NULL
     AND retention_receipt_signing_algorithm IS NULL AND retention_receipt_signing_key_id IS NULL AND retention_receipt_signature IS NULL)
   OR
   (state='publish-intended'
     AND artifact_publish_intended_at IS NOT NULL AND artifact_retained_proved_at IS NULL AND retained_artifact IS NOT NULL
     AND retention_receipt_canonical IS NULL AND retention_receipt_sha256 IS NULL
     AND retention_receipt_signing_algorithm IS NULL AND retention_receipt_signing_key_id IS NULL AND retention_receipt_signature IS NULL)
   OR
   (state='retained-proved'
     AND artifact_publish_intended_at IS NOT NULL AND artifact_retained_proved_at IS NOT NULL AND retained_artifact IS NOT NULL
     AND retention_receipt_canonical IS NOT NULL AND retention_receipt_sha256 ~ '^[0-9a-f]{64}$'
     AND retention_receipt_signing_algorithm IS NOT NULL AND retention_receipt_signing_key_id IS NOT NULL AND retention_receipt_signature IS NOT NULL)
   OR
   (state NOT IN ('stage-proved','publish-intended','retained-proved')
     AND artifact_publish_intended_at IS NULL AND artifact_retained_proved_at IS NULL AND retained_artifact IS NULL
     AND retention_receipt_canonical IS NULL AND retention_receipt_sha256 IS NULL
     AND retention_receipt_signing_algorithm IS NULL AND retention_receipt_signing_key_id IS NULL AND retention_receipt_signature IS NULL)
 );
`

func mysqlSourceSnapshotRetentionMigration() SchemaMigration {
	return SchemaMigration{Version: 35, Name: "mysql-source-snapshot-retained-artifact", SQL: mysqlSourceSnapshotRetentionMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: MySQLRetainedArtifactWriterVersion}
}
