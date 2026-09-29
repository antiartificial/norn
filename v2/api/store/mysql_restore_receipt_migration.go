package store

// Older operator-asserted source_quiescence values are retained only for
// audit. They never qualify a new restore. Every new maintenance fence binds
// an independently accepted, service-signed source artifact receipt.
const MySQLRestoreReceiptWriterVersion int64 = 25

const mysqlRestoreReceiptMigrationSQL = `
ALTER TABLE mysql_restore_maintenance_fences
 ADD COLUMN source_artifact_operation_id TEXT REFERENCES mysql_source_snapshot_intents(operation_id) ON DELETE RESTRICT,
 ADD COLUMN source_artifact_receipt_sha256 TEXT;
ALTER TABLE mysql_restore_maintenance_fences ALTER COLUMN source_quiescence DROP NOT NULL;
ALTER TABLE mysql_restore_maintenance_fences ADD CONSTRAINT mysql_restore_receipt_pair CHECK (
 (source_artifact_operation_id IS NULL AND source_artifact_receipt_sha256 IS NULL AND source_quiescence IS NOT NULL)
 OR (source_artifact_operation_id IS NOT NULL AND source_artifact_receipt_sha256 ~ '^[0-9a-f]{64}$' AND source_quiescence IS NULL)
);
`

func mysqlRestoreReceiptMigration() SchemaMigration {
	return SchemaMigration{Version: 33, Name: "mysql-restore-source-artifact-receipt", SQL: mysqlRestoreReceiptMigrationSQL,
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion, MinimumWriterVersion: MySQLRestoreReceiptWriterVersion}
}
