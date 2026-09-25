package store

// FunctionInvocationArchiveWriterVersion is the first writer that reserves a
// signed operation evidence bundle with every private function acceptance.
// Older writers cannot share a writable schema because they can acknowledge
// a function invocation without reserving archive capacity.
const FunctionInvocationArchiveWriterVersion int64 = 18

const functionInvocationArchiveMigrationSQL = `
CREATE INDEX idx_function_invocation_archive_intents
 ON evidence_archive_intents (operation_id, state)
 WHERE subject_kind = 'operation';
`

func functionInvocationArchiveMigration() SchemaMigration {
	return SchemaMigration{
		Version:              21,
		Name:                 "function-invocation-operation-evidence",
		SQL:                  functionInvocationArchiveMigrationSQL,
		MinimumReaderVersion: OperationAcceptanceRetirementReaderVersion,
		MinimumWriterVersion: FunctionInvocationArchiveWriterVersion,
	}
}
