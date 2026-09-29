package store

// FunctionInvocationCleanupWriterVersion is the first writer that persists
// lease-fenced variable cleanup acknowledgements for completed invocations.
const FunctionInvocationCleanupWriterVersion int64 = 17

const functionInvocationCleanupMigrationSQL = `
CREATE TABLE function_invocation_cleanup_intents (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE RESTRICT,
 variable_path TEXT NOT NULL CHECK(variable_path ~ '^nomad/jobs/norn-fn-[0-9a-f]{40}/invoke$'),
 owner_marker TEXT NOT NULL CHECK(length(owner_marker) > 0 AND length(owner_marker) <= 512 AND position(chr(13) in owner_marker) = 0 AND position(chr(10) in owner_marker) = 0),
 state TEXT NOT NULL CHECK(state IN ('pending', 'claimed', 'completed')) DEFAULT 'pending',
 claim_token TEXT NOT NULL DEFAULT '',
 claimed_at TIMESTAMPTZ,
 lease_until TIMESTAMPTZ,
 completed_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK(
   (state = 'pending' AND claim_token = '' AND claimed_at IS NULL AND lease_until IS NULL AND completed_at IS NULL) OR
   (state = 'claimed' AND claim_token <> '' AND claimed_at IS NOT NULL AND lease_until IS NOT NULL AND completed_at IS NULL) OR
   (state = 'completed' AND claim_token = '' AND claimed_at IS NOT NULL AND lease_until IS NULL AND completed_at IS NOT NULL)
 )
);
CREATE INDEX idx_function_invocation_cleanup_claimable
 ON function_invocation_cleanup_intents (state, lease_until, created_at)
 WHERE state IN ('pending', 'claimed');
`

func functionInvocationCleanupMigration() SchemaMigration {
	return SchemaMigration{
		Version:              20,
		Name:                 "function-invocation-variable-cleanup",
		SQL:                  functionInvocationCleanupMigrationSQL,
		MinimumReaderVersion: OperationAcceptanceRetirementReaderVersion,
		MinimumWriterVersion: FunctionInvocationCleanupWriterVersion,
	}
}
