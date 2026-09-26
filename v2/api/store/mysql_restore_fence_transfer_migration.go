package store

// Ownership moves from a source snapshot to exactly one restore without
// clearing the global claim gate. The source row keeps its original epoch and
// owner as immutable lineage; the restore fence records the transferred epoch.
const MySQLRestoreFenceTransferWriterVersion int64 = 26

const mysqlRestoreFenceTransferMigrationSQL = `
ALTER TABLE mysql_restore_maintenance_fences
 ADD COLUMN runtime_fence_epoch BIGINT,
 ADD COLUMN runtime_fence_claim_owner TEXT,
 ADD COLUMN runtime_fence_claim_generation BIGINT,
 ADD COLUMN runtime_fence_transferred_at TIMESTAMPTZ;
ALTER TABLE mysql_restore_maintenance_fences ADD CONSTRAINT mysql_restore_fence_transfer_pair CHECK (
 (runtime_fence_epoch IS NULL AND runtime_fence_claim_owner IS NULL AND runtime_fence_claim_generation IS NULL AND runtime_fence_transferred_at IS NULL)
 OR (runtime_fence_epoch > 0 AND runtime_fence_claim_owner <> '' AND runtime_fence_claim_generation > 0 AND runtime_fence_transferred_at IS NOT NULL
     AND source_artifact_operation_id IS NOT NULL AND source_artifact_receipt_sha256 IS NOT NULL)
);
CREATE UNIQUE INDEX mysql_restore_one_transfer_per_source
 ON mysql_restore_maintenance_fences(source_artifact_operation_id)
 WHERE runtime_fence_transferred_at IS NOT NULL;
`

func mysqlRestoreFenceTransferMigration() SchemaMigration {
	return SchemaMigration{Version: 34, Name: "mysql-restore-runtime-fence-transfer", SQL: mysqlRestoreFenceTransferMigrationSQL,
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion, MinimumWriterVersion: MySQLRestoreFenceTransferWriterVersion}
}
