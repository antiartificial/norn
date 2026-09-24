package store

// Migration 14 adds transactional logical-byte reservations for the exact
// request, envelope and signature payloads persisted by signed acceptance.
// PostgreSQL heap, index, TOAST and WAL overhead, plus later operation/effect/
// event growth, are outside this deliberately narrow counter.
const signedAcceptanceByteReserveMigrationSQL = `
	ALTER TABLE evidence_reserve
		ADD COLUMN max_signed_acceptance_bytes BIGINT NOT NULL DEFAULT 0 CHECK (max_signed_acceptance_bytes >= 0);

	CREATE TABLE signed_acceptance_byte_reservations (
		operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
		acceptance_intent_id TEXT NOT NULL UNIQUE REFERENCES operation_acceptance_intents(id) ON DELETE RESTRICT,
		reserved_bytes BIGINT NOT NULL CHECK (reserved_bytes > 0),
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);

	INSERT INTO signed_acceptance_byte_reservations
		(operation_id, acceptance_intent_id, reserved_bytes, created_at)
	SELECT i.operation_id, i.id,
		GREATEST(1, octet_length(i.request_canonical_bytes) + octet_length(i.canonical_bytes) + octet_length(convert_to(i.signature, 'UTF8'))),
		i.accepted_at
	FROM operation_acceptance_intents i;
`

// SignedAcceptanceByteReserveWriterVersion is the first writer that can
// serialize signed acceptance against the optional exact-payload byte budget.
const SignedAcceptanceByteReserveWriterVersion int64 = 11

func signedAcceptanceByteReserveMigration() SchemaMigration {
	return SchemaMigration{
		Version:              14,
		Name:                 "signed-acceptance-byte-reserve",
		SQL:                  signedAcceptanceByteReserveMigrationSQL,
		MinimumReaderVersion: EvidenceArchiveReaderVersion,
		MinimumWriterVersion: SignedAcceptanceByteReserveWriterVersion,
	}
}
