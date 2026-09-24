package store

// Migration 17 raises the compatibility floor for archive-backed retirement
// of expired signed acceptance payloads. Older readers resolve a replay by
// loading the hot acceptance row, so they must be drained before a writer can
// remove that row after its immutable archive receipt has been verified.
const OperationAcceptanceRetirementReaderVersion int64 = 3
const OperationAcceptanceRetirementWriterVersion int64 = 14

const operationAcceptanceRetirementMigrationSQL = `
	CREATE TABLE retired_operation_acceptances (
		operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
		request_identity_id TEXT NOT NULL UNIQUE REFERENCES operation_request_identities(id) ON DELETE RESTRICT,
		request_receipt_id TEXT REFERENCES mutation_audit_events(id) ON DELETE RESTRICT,
		archive_intent_id TEXT NOT NULL UNIQUE REFERENCES evidence_archive_intents(id) ON DELETE RESTRICT,
		retired_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);
`

func operationAcceptanceRetirementMigration() SchemaMigration {
	return SchemaMigration{
		Version:              17,
		Name:                 "archive-backed-operation-acceptance-retirement",
		SQL:                  operationAcceptanceRetirementMigrationSQL,
		MinimumReaderVersion: OperationAcceptanceRetirementReaderVersion,
		MinimumWriterVersion: OperationAcceptanceRetirementWriterVersion,
	}
}
