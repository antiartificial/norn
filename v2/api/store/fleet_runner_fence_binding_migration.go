package store

// FleetRunnerFenceBindingWriterVersion retires writers that can create a
// protected runner attempt without its server-owned target fence binding.
const FleetRunnerFenceBindingWriterVersion int64 = 33

// The binding is immutable attempt provenance. Historical unfenced attempts
// retain NULL; a protected contraction must never accept that legacy value.
const fleetRunnerFenceBindingMigrationSQL = `
    ALTER TABLE fleet_runner_attempts
    ADD COLUMN fence_binding JSONB;
`

func fleetRunnerFenceBindingMigration() SchemaMigration {
	return SchemaMigration{Version: 51, Name: "fleet-runner-attempt-fence-binding", SQL: fleetRunnerFenceBindingMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: FleetRunnerFenceBindingWriterVersion}
}
