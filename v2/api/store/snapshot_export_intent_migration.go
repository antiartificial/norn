package store

const SnapshotExportIntentWriterVersion int64 = 31

const snapshotExportIntentMigrationSQL = `
CREATE TABLE snapshot_export_intents (
 operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE RESTRICT,
 object_key TEXT NOT NULL,
 bucket TEXT NOT NULL,
 dump_sha256 TEXT NOT NULL CHECK (dump_sha256 ~ '^[0-9a-f]{64}$'),
 dump_size BIGINT NOT NULL CHECK (dump_size > 0),
 manifest_sha256 TEXT NOT NULL CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
 origin_claim_generation BIGINT NOT NULL CHECK (origin_claim_generation > 0),
 state TEXT NOT NULL CHECK (state IN ('prepared','published')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 published_at TIMESTAMPTZ,
 PRIMARY KEY (operation_id, object_key),
 CHECK ((state='published' AND published_at IS NOT NULL) OR (state='prepared' AND published_at IS NULL))
);
`

func snapshotExportIntentMigration() SchemaMigration {
	return SchemaMigration{Version: 39, Name: "snapshot-export-intents", SQL: snapshotExportIntentMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: SnapshotExportIntentWriterVersion}
}
