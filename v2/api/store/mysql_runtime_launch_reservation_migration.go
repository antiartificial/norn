package store

const mysqlRuntimeLaunchReservationMigrationSQL = `
CREATE TABLE mysql_runtime_launch_reservations (
 reservation_id TEXT NOT NULL,
 target_key TEXT NOT NULL,
 target JSONB NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('reserved', 'launched', 'needs-inspection', 'stopped', 'released')),
 runtime_instance_id TEXT NOT NULL DEFAULT '',
 stop_proof JSONB,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY (reservation_id, target_key),
 CHECK ((state = 'reserved' AND runtime_instance_id = '' AND stop_proof IS NULL) OR
        (state = 'launched' AND runtime_instance_id <> '' AND stop_proof IS NULL) OR
        (state = 'needs-inspection' AND stop_proof IS NULL) OR
        (state = 'stopped' AND runtime_instance_id <> '' AND stop_proof IS NOT NULL) OR
        (state = 'released' AND runtime_instance_id = '' AND stop_proof IS NULL))
);
CREATE UNIQUE INDEX mysql_runtime_launch_reservations_active_target_idx
 ON mysql_runtime_launch_reservations (target_key)
 WHERE state IN ('reserved', 'launched', 'needs-inspection');
`

func mysqlRuntimeLaunchReservationMigration() SchemaMigration {
	return SchemaMigration{
		Version: 26, Name: "mysql-runtime-launch-reservations", SQL: mysqlRuntimeLaunchReservationMigrationSQL,
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion,
		MinimumWriterVersion: FunctionDeploymentProvenanceWriterVersion,
	}
}
