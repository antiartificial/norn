package store

const MySQLRestoreRecoveryReleaseWriterVersion int64 = 30

const mysqlRestoreRecoveryReleaseMigrationSQL = `
ALTER TABLE mysql_restore_maintenance_fences
 ADD COLUMN recovery_released_at TIMESTAMPTZ,
 ADD COLUMN recovery_operation_id TEXT REFERENCES operations(id) ON DELETE RESTRICT;
ALTER TABLE mysql_restore_maintenance_fences ADD CONSTRAINT mysql_restore_maintenance_release_pair CHECK (
 (recovery_released_at IS NULL AND recovery_operation_id IS NULL) OR
 (recovery_released_at IS NOT NULL AND recovery_operation_id IS NOT NULL)
);
ALTER TABLE mysql_restore_recovery_intents ADD COLUMN runtime_released_at TIMESTAMPTZ;
ALTER TABLE mysql_restore_recovery_intents DROP CONSTRAINT mysql_restore_recovery_intents_state_check;
ALTER TABLE mysql_restore_recovery_intents ADD CONSTRAINT mysql_restore_recovery_intents_state_check
 CHECK (state IN ('prepared','target-unlock-intended','target-unlock-proved','runtime-released'));
ALTER TABLE mysql_restore_recovery_intents DROP CONSTRAINT mysql_restore_recovery_target_unlock_pair;
ALTER TABLE mysql_restore_recovery_intents ADD CONSTRAINT mysql_restore_recovery_target_unlock_pair CHECK (
 (state='prepared' AND target_unlock_intended_at IS NULL AND target_unlock_proved_at IS NULL AND runtime_released_at IS NULL) OR
 (state='target-unlock-intended' AND target_unlock_intended_at IS NOT NULL AND target_unlock_proved_at IS NULL AND runtime_released_at IS NULL) OR
 (state='target-unlock-proved' AND target_unlock_intended_at IS NOT NULL AND target_unlock_proved_at IS NOT NULL AND runtime_released_at IS NULL) OR
 (state='runtime-released' AND target_unlock_intended_at IS NOT NULL AND target_unlock_proved_at IS NOT NULL AND runtime_released_at IS NOT NULL)
);
`

func mysqlRestoreRecoveryReleaseMigration() SchemaMigration {
	return SchemaMigration{Version: 38, Name: "mysql-restore-recovery-runtime-release", SQL: mysqlRestoreRecoveryReleaseMigrationSQL,
		MinimumReaderVersion: MySQLRetainedArtifactReaderVersion, MinimumWriterVersion: MySQLRestoreRecoveryReleaseWriterVersion}
}
