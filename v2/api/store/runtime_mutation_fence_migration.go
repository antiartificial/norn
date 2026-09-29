package store

const RuntimeMutationFenceWriterVersion int64 = 20

const runtimeMutationFenceMigrationSQL = `
CREATE TABLE runtime_mutation_fence (
 singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
 epoch BIGINT NOT NULL DEFAULT 0 CHECK (epoch >= 0),
 active BOOLEAN NOT NULL DEFAULT FALSE,
 owner TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT '',
 held_at TIMESTAMPTZ,
 released_at TIMESTAMPTZ,
 CHECK ((active AND epoch > 0 AND owner <> '' AND reason <> '' AND held_at IS NOT NULL AND released_at IS NULL) OR
        (NOT active AND owner = '' AND reason = '' AND released_at IS NOT NULL))
);
INSERT INTO runtime_mutation_fence(singleton, epoch, active, owner, reason, released_at)
VALUES(TRUE, 0, FALSE, '', '', clock_timestamp())
ON CONFLICT (singleton) DO NOTHING;
`

func runtimeMutationFenceMigration() SchemaMigration {
	return SchemaMigration{Version: 28, Name: "runtime-mutation-fence", SQL: runtimeMutationFenceMigrationSQL, MinimumReaderVersion: FunctionInvocationArchiveReaderVersion, MinimumWriterVersion: RuntimeMutationFenceWriterVersion}
}
