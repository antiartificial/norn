package store

// Unlock intent is one-way. An interrupted ALTER USER cannot be replayed as
// untouched preparation; an operator must inspect the account and reconcile.
const MySQLRestoreRecoveryUnlockWriterVersion int64 = 29

const mysqlRestoreRecoveryUnlockMigrationSQL = `
ALTER TABLE mysql_restore_recovery_intents
 ADD COLUMN target_unlock_intended_at TIMESTAMPTZ,
 ADD COLUMN target_unlock_proved_at TIMESTAMPTZ;
ALTER TABLE mysql_restore_recovery_intents DROP CONSTRAINT mysql_restore_recovery_intents_state_check;
ALTER TABLE mysql_restore_recovery_intents ADD CONSTRAINT mysql_restore_recovery_intents_state_check
 CHECK (state IN ('prepared','target-unlock-intended','target-unlock-proved'));
ALTER TABLE mysql_restore_recovery_intents ADD CONSTRAINT mysql_restore_recovery_target_unlock_pair CHECK (
 (state='prepared' AND target_unlock_intended_at IS NULL AND target_unlock_proved_at IS NULL) OR
 (state='target-unlock-intended' AND target_unlock_intended_at IS NOT NULL AND target_unlock_proved_at IS NULL) OR
 (state='target-unlock-proved' AND target_unlock_intended_at IS NOT NULL AND target_unlock_proved_at IS NOT NULL)
);
`

func mysqlRestoreRecoveryUnlockMigration() SchemaMigration {
	return SchemaMigration{Version: 37, Name: "mysql-restore-recovery-target-unlock", SQL: mysqlRestoreRecoveryUnlockMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: MySQLRestoreRecoveryUnlockWriterVersion}
}
