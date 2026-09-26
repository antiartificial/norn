package store

// FunctionInvocationArchiveReaderVersion is the first reader that accepts v2
// evidence bundles while retaining v1 compatibility. The floor is introduced
// in its own forward-only migration so migration 21's applied checksum stays
// immutable.
const FunctionInvocationArchiveReaderVersion int64 = 4

func functionInvocationReaderContractMigration() SchemaMigration {
	return SchemaMigration{
		Version:              23,
		Name:                 "function-invocation-evidence-reader-contract",
		SQL:                  "SELECT 1;",
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion,
		MinimumWriterVersion: FunctionDeploymentProvenanceWriterVersion,
	}
}
