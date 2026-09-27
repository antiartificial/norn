package store

const RestartEffectSourceWriterVersion int64 = 10

const restartEffectSourcesMigrationSQL = `
CREATE TABLE restart_effect_sources (
 operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
 allocation_id TEXT NOT NULL, job_id TEXT NOT NULL, namespace TEXT NOT NULL DEFAULT '', task_group TEXT NOT NULL DEFAULT '', create_index BIGINT NOT NULL CHECK(create_index>0),
 attempted_at TIMESTAMPTZ, acknowledged_at TIMESTAMPTZ, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(operation_id, allocation_id), CHECK(acknowledged_at IS NULL OR attempted_at IS NOT NULL)
);`

func restartEffectSourcesMigration() SchemaMigration {
	return SchemaMigration{Version: 13, Name: "durable-restart-effect-sources", SQL: restartEffectSourcesMigrationSQL, MinimumReaderVersion: EvidenceArchiveReaderVersion, MinimumWriterVersion: RestartEffectSourceWriterVersion}
}
