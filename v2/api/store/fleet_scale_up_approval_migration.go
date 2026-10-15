package store

// The approval run is immutable dispatch provenance. Zero preserves existing
// bindings, which cannot be upgraded in place to a scale-up approval.
const fleetScaleUpApprovalMigrationSQL = `
    ALTER TABLE fleet_github_dispatches
    ADD COLUMN scale_up_approval_run_id BIGINT NOT NULL DEFAULT 0
    CHECK (scale_up_approval_run_id >= 0);
`

func fleetScaleUpApprovalMigration() SchemaMigration {
	return SchemaMigration{Version: 50, Name: "fleet-scale-up-owner-approval-binding", SQL: fleetScaleUpApprovalMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: FleetTargetFenceWriterVersion}
}
