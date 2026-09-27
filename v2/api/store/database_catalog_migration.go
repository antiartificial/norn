package store

// Migration 5 stores database catalog revisions and permanent retirement
// history in one transaction per activation. Retired service and binding IDs
// are recorded independently of any single catalog document, so a later
// revision cannot recreate an ID (and reset its generation) that accepted
// work may still name. Writers older than contract 5 execute data operations
// through ambient libpq routing instead of recorded targets, so the writer
// floor is raised.
const databaseCatalogMigrationSQL = `
	CREATE TABLE database_catalog_revisions (
		revision BIGINT PRIMARY KEY CHECK (revision > 0),
		previous_revision BIGINT REFERENCES database_catalog_revisions(revision) ON DELETE RESTRICT,
		catalog BYTEA NOT NULL CHECK (octet_length(catalog) BETWEEN 2 AND 1048576),
		catalog_digest TEXT NOT NULL CHECK (catalog_digest ~ '^sha256:[0-9a-f]{64}$'),
		activated_by TEXT NOT NULL CHECK (length(activated_by) > 0),
		activated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		CHECK ((revision = 1 AND previous_revision IS NULL) OR previous_revision = revision - 1),
		UNIQUE (previous_revision)
	);
	CREATE TABLE database_catalog_retirements (
		kind TEXT NOT NULL CHECK (kind IN ('service', 'binding')),
		id TEXT NOT NULL CHECK (length(id) > 0),
		retired_revision BIGINT NOT NULL REFERENCES database_catalog_revisions(revision) ON DELETE RESTRICT,
		PRIMARY KEY (kind, id)
	);
`

func databaseCatalogMigration() SchemaMigration {
	return SchemaMigration{
		Version:              5,
		Name:                 "database-catalog-revisions",
		SQL:                  databaseCatalogMigrationSQL,
		MinimumReaderVersion: 1,
		MinimumWriterVersion: 5,
	}
}
