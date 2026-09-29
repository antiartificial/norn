package store

// A committed stop attempt is never retried automatically: a lost response
// requires observation and a separate recovery decision before another write.
const MySQLSourceStopWriterVersion int64 = 22

const mysqlSourceSnapshotStopMigrationSQL = `
ALTER TABLE mysql_source_snapshot_intents
 ADD COLUMN stop_intended_at TIMESTAMPTZ,
 ADD COLUMN stop_proved_at TIMESTAMPTZ;
ALTER TABLE mysql_source_snapshot_intents DROP CONSTRAINT mysql_source_snapshot_intents_state_check;
ALTER TABLE mysql_source_snapshot_intents ADD CONSTRAINT mysql_source_snapshot_intents_state_check
 CHECK (state IN ('quiesce-intended','stop-intended','stop-proved'));
`

func mysqlSourceSnapshotStopMigration() SchemaMigration {
	return SchemaMigration{Version: 30, Name: "mysql-source-snapshot-stop-checkpoint", SQL: mysqlSourceSnapshotStopMigrationSQL,
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion, MinimumWriterVersion: MySQLSourceStopWriterVersion}
}
