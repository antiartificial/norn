package store

const desiredReplicasMigrationSQL = `
	CREATE TABLE app_desired_replicas (
		app TEXT NOT NULL CHECK (length(app) > 0),
		process TEXT NOT NULL CHECK (length(process) > 0),
		desired_count INT NOT NULL CHECK (desired_count >= 0),
		revision BIGINT NOT NULL CHECK (revision > 0),
		operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE RESTRICT,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (app, process)
	);
`

const DesiredReplicaWriterVersion int64 = 8

func desiredReplicasMigration() SchemaMigration {
	return SchemaMigration{Version: 11, Name: "durable-app-desired-replicas", SQL: desiredReplicasMigrationSQL, MinimumReaderVersion: EvidenceArchiveReaderVersion, MinimumWriterVersion: DesiredReplicaWriterVersion}
}

const RegionalDesiredReplicaWriterVersion int64 = 9

func regionalDesiredReplicasMigration() SchemaMigration {
	return SchemaMigration{Version: 12, Name: "regional-durable-app-desired-replicas", SQL: `
		ALTER TABLE app_desired_replicas ADD COLUMN region TEXT NOT NULL DEFAULT '';
		ALTER TABLE app_desired_replicas DROP CONSTRAINT app_desired_replicas_pkey;
		ALTER TABLE app_desired_replicas ADD PRIMARY KEY (app, process, region);
	`, MinimumReaderVersion: EvidenceArchiveReaderVersion, MinimumWriterVersion: RegionalDesiredReplicaWriterVersion}
}
