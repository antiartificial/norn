package store

const workloadReleaseReceiptMigrationSQL = `
	CREATE TABLE workload_release_receipts (
		intent_id TEXT PRIMARY KEY CHECK (length(intent_id) > 0),
		operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE RESTRICT,
		receipt BYTEA NOT NULL CHECK (octet_length(receipt) BETWEEN 2 AND 262144),
		receipt_digest TEXT NOT NULL CHECK (receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
		claim_generation BIGINT NOT NULL CHECK (claim_generation > 0),
		revision BIGINT NOT NULL CHECK (revision > 0),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);
`

func workloadReleaseReceiptMigration() SchemaMigration {
	return SchemaMigration{
		Version: 52, Name: "workload-release-receipt-cas",
		SQL:                  workloadReleaseReceiptMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion,
		MinimumWriterVersion: FleetRunnerFenceBindingWriterVersion,
	}
}
