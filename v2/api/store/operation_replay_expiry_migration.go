package store

// Migration 15 adds an opt-in, versioned replay-expiry contract. Existing
// identities keep empty metadata and therefore retain indefinite replay.
// Expired identities remain durable tombstones: the namespace and original
// fingerprint are never released by this migration.
const operationReplayExpiryMigrationSQL = `
	ALTER TABLE operation_request_identities
		ADD COLUMN replay_contract_version TEXT NOT NULL DEFAULT '',
		ADD COLUMN replay_expires_at TIMESTAMPTZ,
		ADD COLUMN replay_expired_at TIMESTAMPTZ,
		ADD CONSTRAINT operation_request_identity_replay_contract CHECK (
			(replay_contract_version = '' AND replay_expires_at IS NULL AND replay_expired_at IS NULL)
			OR
			(replay_contract_version = 'norn.operation-replay/v1' AND replay_expires_at IS NOT NULL
				AND (replay_expired_at IS NULL OR replay_expired_at >= replay_expires_at))
		);

	CREATE INDEX idx_operation_request_identity_replay_expiry
		ON operation_request_identities(replay_expires_at)
		WHERE replay_contract_version = 'norn.operation-replay/v1' AND replay_expired_at IS NULL;
`

// OperationReplayExpiryWriterVersion is the first writer that persists and
// enforces versioned replay-expiry metadata.
const OperationReplayExpiryWriterVersion int64 = 12

func operationReplayExpiryMigration() SchemaMigration {
	return SchemaMigration{
		Version:              15,
		Name:                 "operation-replay-expiry",
		SQL:                  operationReplayExpiryMigrationSQL,
		MinimumReaderVersion: EvidenceArchiveReaderVersion,
		MinimumWriterVersion: OperationReplayExpiryWriterVersion,
	}
}
