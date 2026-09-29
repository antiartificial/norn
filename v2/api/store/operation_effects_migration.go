package store

const operationEffectsMigrationSQL = `
	CREATE TABLE operation_effects (
		id TEXT PRIMARY KEY,
		generation BIGINT NOT NULL CHECK (generation > 0),
		authority UUID NOT NULL REFERENCES control_plane_identity(authority) ON DELETE RESTRICT,
		resource TEXT NOT NULL CHECK (length(resource) > 0),
		operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE RESTRICT,
		claim_owner TEXT NOT NULL CHECK (length(claim_owner) > 0),
		claim_generation BIGINT NOT NULL CHECK (claim_generation > 0),
		stage TEXT NOT NULL CHECK (length(stage) > 0),
		input_digest TEXT NOT NULL CHECK (input_digest ~ '^sha256:[0-9a-f]{64}$'),
		launch_payload JSONB NOT NULL,
		supervisor TEXT NOT NULL CHECK (length(supervisor) > 0),
		supervisor_execution_id TEXT NOT NULL CHECK (length(supervisor_execution_id) > 0),
		runtime_instance_id TEXT NOT NULL DEFAULT '',
		lifecycle TEXT NOT NULL CHECK (lifecycle IN ('reserved', 'launched', 'completed', 'resolved')),
		outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'succeeded', 'failed')),
		exit_code INT,
		result_digest TEXT NOT NULL DEFAULT '',
		result_reference TEXT NOT NULL DEFAULT '',
		evidence_source TEXT NOT NULL DEFAULT '',
		evidence_reference TEXT NOT NULL DEFAULT '',
		evidence_observed_at TIMESTAMPTZ,
		resolution_decision TEXT NOT NULL DEFAULT '' CHECK (resolution_decision IN ('', 'never-launched', 'stopped-repeat-safe')),
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		launched_at TIMESTAMPTZ,
		completed_at TIMESTAMPTZ,
		resolved_at TIMESTAMPTZ,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		CHECK (
			(lifecycle = 'reserved' AND runtime_instance_id = '' AND outcome = '' AND completed_at IS NULL AND resolved_at IS NULL)
			OR (lifecycle = 'launched' AND runtime_instance_id <> '' AND outcome = '' AND completed_at IS NULL AND resolved_at IS NULL)
			OR (lifecycle = 'completed' AND outcome <> '' AND completed_at IS NOT NULL AND resolved_at IS NULL)
			OR (lifecycle = 'resolved' AND outcome = '' AND resolution_decision <> '' AND resolved_at IS NOT NULL)
		),
		CHECK (outcome <> 'succeeded' OR (result_digest ~ '^sha256:[0-9a-f]{64}$' AND result_reference <> '')),
		UNIQUE (authority, supervisor, supervisor_execution_id)
	);
	CREATE UNIQUE INDEX idx_operation_effects_unresolved_resource
		ON operation_effects(authority, resource)
		WHERE lifecycle IN ('reserved', 'launched');
	CREATE UNIQUE INDEX idx_operation_effects_operation_input
		ON operation_effects(authority, operation_id, stage, input_digest)
		WHERE lifecycle <> 'resolved';
	CREATE INDEX idx_operation_effects_operation
		ON operation_effects(operation_id, created_at DESC);
	CREATE INDEX idx_operation_effects_recovery
		ON operation_effects(lifecycle, updated_at)
		WHERE lifecycle IN ('reserved', 'launched');
`

func operationEffectsMigration() SchemaMigration {
	return SchemaMigration{
		Version:              3,
		Name:                 "external-effect-recovery",
		SQL:                  operationEffectsMigrationSQL,
		MinimumReaderVersion: 1,
		MinimumWriterVersion: 3,
	}
}
