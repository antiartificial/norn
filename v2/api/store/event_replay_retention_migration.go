package store

// Migration 9 records the highest compacted control-event cursor. The event
// table alone cannot communicate expiry after every retained event is pruned.
const eventReplayRetentionMigrationSQL = `
	CREATE TABLE control_event_retention (
		id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
		pruned_through_cursor BIGINT NOT NULL DEFAULT 0 CHECK (pruned_through_cursor >= 0),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);
	INSERT INTO control_event_retention (id) VALUES (true) ON CONFLICT (id) DO NOTHING;
`

func eventReplayRetentionMigration() SchemaMigration {
	return SchemaMigration{
		Version:              9,
		Name:                 "control-event-replay-retention",
		SQL:                  eventReplayRetentionMigrationSQL,
		MinimumReaderVersion: EvidenceArchiveReaderVersion,
		MinimumWriterVersion: EvidenceReserveWriterVersion,
	}
}
