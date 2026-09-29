package startup

import (
	"testing"

	"norn/v2/api/store"
)

func TestCurrentSchemaContractComesFromCompiledCatalog(t *testing.T) {
	migrations := store.ControlSchemaMigrations()
	target := migrations[len(migrations)-1]
	contract := CurrentSchemaContract()
	if contract.ReaderVersion != store.ControlSchemaReaderVersion || contract.WriterVersion != store.ControlSchemaWriterVersion {
		t.Fatalf("binary compatibility contract=%+v", contract)
	}
	if contract.CatalogMigrationVersion != target.Version ||
		contract.CatalogMinimumReaderVersion != target.MinimumReaderVersion ||
		contract.CatalogMinimumWriterVersion != target.MinimumWriterVersion {
		t.Fatalf("catalog contract=%+v target=%+v", contract, target)
	}
}
