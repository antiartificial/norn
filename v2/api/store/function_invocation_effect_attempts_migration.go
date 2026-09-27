package store

// FunctionInvocationEffectAttemptWriterVersion is the first writer that
// persists the public, pre-Nomad attempt fence for function invocations.
const FunctionInvocationEffectAttemptWriterVersion int64 = 16

const functionInvocationEffectAttemptsMigrationSQL = `
CREATE TABLE function_invocation_effect_attempts (
 operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE RESTRICT,
 stage TEXT NOT NULL CHECK(stage IN ('variable', 'job')),
 target TEXT NOT NULL CHECK(length(target) > 0 AND length(target) <= 512),
 input_digest TEXT NOT NULL CHECK(input_digest ~ '^sha256:[0-9a-f]{64}$'),
 claim_generation BIGINT NOT NULL CHECK(claim_generation > 0),
 state TEXT NOT NULL CHECK(state IN ('recorded', 'attempted')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 attempted_at TIMESTAMPTZ,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(operation_id, stage),
 CHECK((state = 'recorded' AND attempted_at IS NULL) OR (state = 'attempted' AND attempted_at IS NOT NULL))
);
`

func functionInvocationEffectAttemptsMigration() SchemaMigration {
	return SchemaMigration{Version: 19, Name: "function-invocation-effect-attempts", SQL: functionInvocationEffectAttemptsMigrationSQL, MinimumReaderVersion: OperationAcceptanceRetirementReaderVersion, MinimumWriterVersion: FunctionInvocationEffectAttemptWriterVersion}
}
