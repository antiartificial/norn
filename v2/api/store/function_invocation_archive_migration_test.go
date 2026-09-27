package store

import "testing"

func TestFunctionInvocationArchiveMigrationChecksumRemainsPublished(t *testing.T) {
	migration := functionInvocationArchiveMigration()
	if migration.Version != 21 || migration.MinimumReaderVersion != OperationAcceptanceRetirementReaderVersion || migration.MinimumWriterVersion != FunctionInvocationArchiveWriterVersion {
		t.Fatalf("migration 21 definition = %#v", migration)
	}
	const publishedChecksum = "d5697525fd96af9b2923f0e6a7a0ed9c471537d4d15980e6bb5532c5d44b672b"
	if got := MigrationChecksum(migration); got != publishedChecksum {
		t.Fatalf("migration 21 checksum = %s, want published %s", got, publishedChecksum)
	}
}

func TestFunctionInvocationReaderContractMigrationIsForwardOnly(t *testing.T) {
	migration := functionInvocationReaderContractMigration()
	if migration.Version != 23 || migration.MinimumReaderVersion != FunctionInvocationArchiveReaderVersion || migration.MinimumWriterVersion != FunctionDeploymentProvenanceWriterVersion {
		t.Fatalf("migration 23 definition = %#v", migration)
	}
}
