package store

// Migration 4 records write-once execution checkpoints for an accepted
// operation: the resolved source identity and the build output produced from
// it. Later claims of the same operation reuse them instead of resolving new
// source or rebuilding, so recovery of a deferred effect never repeats the
// preceding external stages. Writers older than contract 4 do not record or
// honour checkpoints, so the writer floor is raised.
const operationCheckpointsMigrationSQL = `
	CREATE TABLE operation_checkpoints (
		operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE RESTRICT,
		stage TEXT NOT NULL CHECK (stage IN ('source', 'build')),
		claim_generation BIGINT NOT NULL CHECK (claim_generation > 0),
		outputs BYTEA NOT NULL CHECK (octet_length(outputs) BETWEEN 2 AND 65536),
		outputs_digest TEXT NOT NULL CHECK (outputs_digest ~ '^sha256:[0-9a-f]{64}$'),
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (operation_id, stage)
	);
`

func operationCheckpointsMigration() SchemaMigration {
	return SchemaMigration{
		Version:              4,
		Name:                 "operation-execution-checkpoints",
		SQL:                  operationCheckpointsMigrationSQL,
		MinimumReaderVersion: 1,
		MinimumWriterVersion: 4,
	}
}
