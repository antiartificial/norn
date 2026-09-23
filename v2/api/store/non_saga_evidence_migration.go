package store

// Migration 10 extends the evidence outbox with a deliberately narrow
// operation subject. Fleet GitHub receipts are terminal signed acceptances
// created after GitHub has created or recovered the corresponding protected
// action. They have no saga, so migration 6's saga-only outbox could neither
// reserve archive capacity nor retain their original signed bytes. Operation
// subjects are immutable, single-sequence receipt bundles; they are archived
// and verified but never pruned by the saga-history pruner because replay and
// protected-action recovery still resolve the hot acceptance records.
const nonSagaEvidenceMigrationSQL = `
	ALTER TABLE evidence_archive_intents
		DROP CONSTRAINT evidence_archive_intents_subject_kind_check;
	ALTER TABLE evidence_archive_intents
		ADD CONSTRAINT evidence_archive_intents_subject_kind_check
		CHECK (subject_kind IN ('saga', 'operation'));
`

// NonSagaEvidenceWriterVersion is the first writer that reserves terminal
// Fleet GitHub receipts as archive work during signed acceptance.
const NonSagaEvidenceWriterVersion int64 = 7

func nonSagaEvidenceMigration() SchemaMigration {
	return SchemaMigration{
		Version:              10,
		Name:                 "non-saga-signed-receipt-evidence",
		SQL:                  nonSagaEvidenceMigrationSQL,
		MinimumReaderVersion: EvidenceArchiveReaderVersion,
		MinimumWriterVersion: NonSagaEvidenceWriterVersion,
	}
}
