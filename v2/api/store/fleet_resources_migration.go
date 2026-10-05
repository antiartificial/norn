package store

// Migration 49 adds the Fleet resource and its observation stream
// (docs/v3/fleet-controller/plan.md §2.3, WP10). It keeps the writer floor
// migration 48 already raised (FleetTargetFenceWriterVersion = 32): the
// resource and observation tables add no new writer-compatibility concern
// of their own, so this migration does not bump ControlSchemaWriterVersion
// again.
const fleetResourcesMigrationSQL = `
	-- document carries everything controller.Resource holds beyond the
	-- columns broken out below: SchemaVersion, Desired, DesiredHistory,
	-- Policy, Watermarks and Status. Status is a derived cache (plan.md
	-- §2.2's "lock, not a ledger" rule applies here too): it stores no
	-- second operation ledger, only IDs that already live in
	-- fleet_target_fences, fleet_github_dispatches and fleet_runner_attempts.
	CREATE TABLE fleet_resources (
		name                 TEXT PRIMARY KEY CHECK (name <> '' AND strpos(name, '/') = 0),
		target_id            TEXT NOT NULL REFERENCES fleet_targets(target_id) ON DELETE RESTRICT,
		document             JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(document) = 'object'),
		revision             BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
		authority_epoch      BIGINT NOT NULL DEFAULT 0 CHECK (authority_epoch >= 0),
		observation_sequence BIGINT NOT NULL DEFAULT 0 CHECK (observation_sequence >= 0),
		created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
	);
	CREATE INDEX idx_fleet_resources_target ON fleet_resources(target_id);

	-- Observations are monotonic per source (M10): AppendFleetObservation
	-- never lets an older observation replace or clear a newer one, and
	-- pruning never deletes the row the resource document's watermark for
	-- that source names. applied records whether this row ever advanced its
	-- source's watermark, so a read can tell a stored-but-superseded
	-- observation from the one actually driving derivation.
	CREATE TABLE fleet_observations (
		resource      TEXT NOT NULL REFERENCES fleet_resources(name) ON DELETE RESTRICT,
		sequence      BIGINT NOT NULL CHECK (sequence > 0),
		source        TEXT NOT NULL CHECK (source IN ('provider', 'state', 'runtime')),
		observed_at   TIMESTAMPTZ NOT NULL,
		received_at   TIMESTAMPTZ NOT NULL,
		applied       BOOLEAN NOT NULL,
		facts         JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(facts) = 'object'),
		evidence_refs JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(evidence_refs) = 'array'),
		reporter      TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (resource, sequence)
	);
	CREATE INDEX idx_fleet_observations_resource_source ON fleet_observations(resource, source, sequence DESC);
`

func fleetResourcesMigration() SchemaMigration {
	return SchemaMigration{Version: 49, Name: "fleet-resources-and-observations", SQL: fleetResourcesMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: FleetTargetFenceWriterVersion}
}
