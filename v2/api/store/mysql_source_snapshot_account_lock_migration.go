package store

// Account locking and session termination are external effects. An uncertain
// response leaves the source reserved and requires explicit observation.
const MySQLSourceAccountLockWriterVersion int64 = 23

const mysqlSourceSnapshotAccountLockMigrationSQL = `
ALTER TABLE mysql_source_snapshot_intents
 ADD COLUMN runtime_fence_epoch BIGINT,
 ADD COLUMN runtime_fence_owner TEXT,
 ADD COLUMN lock_intended_at TIMESTAMPTZ,
 ADD COLUMN lock_proved_at TIMESTAMPTZ;
ALTER TABLE mysql_source_snapshot_intents DROP CONSTRAINT mysql_source_snapshot_intents_state_check;
ALTER TABLE mysql_source_snapshot_intents ADD CONSTRAINT mysql_source_snapshot_intents_state_check
 CHECK (state IN ('quiesce-intended','stop-intended','stop-proved','lock-intended','lock-proved'));
`

func mysqlSourceSnapshotAccountLockMigration() SchemaMigration {
	return SchemaMigration{Version: 31, Name: "mysql-source-snapshot-account-lock-checkpoint", SQL: mysqlSourceSnapshotAccountLockMigrationSQL,
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion, MinimumWriterVersion: MySQLSourceAccountLockWriterVersion}
}
