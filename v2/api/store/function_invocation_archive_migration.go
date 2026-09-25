package store

// FunctionInvocationArchiveWriterVersion is the first writer that reserves a
// signed operation evidence bundle with every private function acceptance.
// Older writers cannot share a writable schema because they can acknowledge
// a function invocation without reserving archive capacity.
const FunctionInvocationArchiveWriterVersion int64 = 18

// FunctionInvocationArchiveReaderVersion is the first reader that accepts v2
// evidence bundles while retaining v1 compatibility. Migration 21 excludes
// older readers before any v2 function bundle can be published.
const FunctionInvocationArchiveReaderVersion int64 = 4

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
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion,
		MinimumWriterVersion: FunctionInvocationArchiveWriterVersion,
	}
}
