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
-- Migration 24 could have left private intents during an upgrade. Preserve
-- them behind a catalog fence. The marker is deliberately explicit: legacy
-- rows do not gain a claim that source-quiescence evidence was accepted.
INSERT INTO mysql_restore_maintenance_fences (operation_id, catalog_revision, source_quiescence)
SELECT operation_id, catalog_revision,
 jsonb_build_object(
   'source', artifact->'source',
   'observedAt', prepared_at,
   'method', 'legacy intent without accepted source-quiescence evidence',
   'evidenceSha256', repeat('0', 64)
 )
FROM mysql_restore_intents
ON CONFLICT (operation_id) DO NOTHING;
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

// A restore cannot safely cross from durable intent into an external account
// lock without recording that intent first. The row is intentionally retained
// after success: restoring application authentication requires an explicit
// operator recovery action, never a worker cleanup path.
const mysqlRestoreRuntimeLockMigrationSQL = `
CREATE TABLE mysql_restore_runtime_locks (
 operation_id TEXT PRIMARY KEY REFERENCES mysql_restore_intents(operation_id) ON DELETE RESTRICT,
 acceptance_intent_id TEXT NOT NULL,
 catalog_revision BIGINT NOT NULL REFERENCES database_catalog_revisions(revision) ON DELETE RESTRICT,
 target JSONB NOT NULL,
 claim_owner TEXT NOT NULL,
 claim_generation BIGINT NOT NULL CHECK (claim_generation > 0),
 state TEXT NOT NULL CHECK (state IN ('lock-intended', 'verified-lock')),
 intended_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 verified_at TIMESTAMPTZ,
 CHECK ((state = 'lock-intended' AND verified_at IS NULL) OR
        (state = 'verified-lock' AND verified_at IS NOT NULL))
);
`

func mysqlRestoreRuntimeLockMigration() SchemaMigration {
	return SchemaMigration{
		Version:              27,
		Name:                 "mysql-restore-runtime-account-locks",
		SQL:                  mysqlRestoreRuntimeLockMigrationSQL,
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion,
		MinimumWriterVersion: FunctionDeploymentProvenanceWriterVersion,
	}
}
