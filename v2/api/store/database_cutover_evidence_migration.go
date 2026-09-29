package store

// Migration 47 retains the exact phase-evidence references beside the journal
// receipt hashes. The referenced external evidence still requires its own
// verified retention path before an effect can authorize activation.
const databaseCutoverEvidenceMigrationSQL = `
ALTER TABLE database_cutover_journals
  ADD COLUMN evidence_references JSONB NOT NULL DEFAULT '{}'::jsonb
  CHECK (jsonb_typeof(evidence_references) = 'object');
`

func databaseCutoverEvidenceMigration() SchemaMigration {
	return SchemaMigration{Version: 47, Name: "database-cutover-evidence-references", SQL: databaseCutoverEvidenceMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: SnapshotExportIntentWriterVersion}
}
