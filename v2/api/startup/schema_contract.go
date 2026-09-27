package startup

import "norn/v2/api/store"

// SchemaContract is compiled into each binary and can be inspected without
// configuration, credentials, telemetry, or a database connection. Schema
// migration versions and binary reader/writer contract versions are separate
// version domains and must not be compared to one another.
type SchemaContract struct {
	ReaderVersion               int64 `json:"readerVersion"`
	WriterVersion               int64 `json:"writerVersion"`
	CatalogMigrationVersion     int64 `json:"catalogMigrationVersion"`
	CatalogMinimumReaderVersion int64 `json:"catalogMinimumReaderVersion"`
	CatalogMinimumWriterVersion int64 `json:"catalogMinimumWriterVersion"`
}

func CurrentSchemaContract() SchemaContract {
	migrations := store.ControlSchemaMigrations()
	target := migrations[len(migrations)-1]
	return SchemaContract{
		ReaderVersion:               store.ControlSchemaReaderVersion,
		WriterVersion:               store.ControlSchemaWriterVersion,
		CatalogMigrationVersion:     target.Version,
		CatalogMinimumReaderVersion: target.MinimumReaderVersion,
		CatalogMinimumWriterVersion: target.MinimumWriterVersion,
	}
}
