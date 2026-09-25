package store

// This additive table is deliberately dormant while MySQL restore remains a
// closed public capability. A target identity is consumed permanently once a
// restore is prepared: an uncertain external SQL write must never be replayed
// against the same generation under another operation ID.
const mysqlRestoreIntentMigrationSQL = `
CREATE TABLE mysql_restore_intents (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
 acceptance_intent_id TEXT NOT NULL,
 catalog_revision BIGINT NOT NULL REFERENCES database_catalog_revisions(revision) ON DELETE RESTRICT,
 profile_id TEXT NOT NULL,
 logical_id TEXT NOT NULL,
 target_key TEXT NOT NULL UNIQUE,
 target JSONB NOT NULL,
 artifact JSONB NOT NULL,
 artifact_path TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('prepared', 'executing', 'needs-inspection', 'completed')),
 prepared_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 started_at TIMESTAMPTZ,
 completed_at TIMESTAMPTZ,
 CHECK ((state = 'prepared' AND started_at IS NULL AND completed_at IS NULL) OR
        (state IN ('executing', 'needs-inspection') AND started_at IS NOT NULL AND completed_at IS NULL) OR
        (state = 'completed' AND started_at IS NOT NULL AND completed_at IS NOT NULL))
);
`

func mysqlRestoreIntentMigration() SchemaMigration {
	return SchemaMigration{
		Version:              24,
		Name:                 "mysql-restore-durable-intents",
		SQL:                  mysqlRestoreIntentMigrationSQL,
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion,
		MinimumWriterVersion: FunctionDeploymentProvenanceWriterVersion,
	}
}

const mysqlRestoreMaintenanceFenceMigrationSQL = `
CREATE TABLE mysql_restore_maintenance_fences (
 operation_id TEXT PRIMARY KEY REFERENCES mysql_restore_intents(operation_id) ON DELETE RESTRICT,
 catalog_revision BIGINT NOT NULL REFERENCES database_catalog_revisions(revision) ON DELETE RESTRICT,
 source_quiescence JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

func mysqlRestoreMaintenanceFenceMigration() SchemaMigration {
	return SchemaMigration{
		Version:              25,
		Name:                 "mysql-restore-maintenance-fences",
		SQL:                  mysqlRestoreMaintenanceFenceMigrationSQL,
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion,
		MinimumWriterVersion: FunctionDeploymentProvenanceWriterVersion,
	}
}
