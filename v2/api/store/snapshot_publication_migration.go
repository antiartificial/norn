package store

const snapshotPublicationMigrationSQL = `
	CREATE TABLE snapshot_publication_intents (
		operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
		origin_claim_generation BIGINT NOT NULL CHECK (origin_claim_generation > 0),
		effect_id TEXT NOT NULL REFERENCES operation_effects(id) ON DELETE RESTRICT,
		input_digest TEXT NOT NULL CHECK (input_digest ~ '^sha256:[0-9a-f]{64}$'),
		supervisor_root_id TEXT NOT NULL,
		supervisor_execution_id TEXT NOT NULL,
		target JSONB NOT NULL,
		catalog_revision BIGINT NOT NULL CHECK (catalog_revision > 0),
		namespace TEXT NOT NULL,
		filename TEXT NOT NULL,
		sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
		size BIGINT NOT NULL CHECK (size > 0),
		state TEXT NOT NULL CHECK (state IN ('prepared','published','abandoned')),
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		published_at TIMESTAMPTZ,
		CHECK ((state = 'published' AND published_at IS NOT NULL) OR (state <> 'published'))
	);
`

const SnapshotPublicationWriterVersion int64 = 13

func snapshotPublicationMigration() SchemaMigration {
	return SchemaMigration{Version: 16, Name: "snapshot-publication-intents", SQL: snapshotPublicationMigrationSQL, MinimumReaderVersion: EvidenceArchiveReaderVersion, MinimumWriterVersion: SnapshotPublicationWriterVersion}
}
