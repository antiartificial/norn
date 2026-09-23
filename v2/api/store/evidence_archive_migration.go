package store

// Migration 6 adds the evidence archive outbox and index. An intent is
// created (pending) atomically with an operation's terminal transition, or by
// the archiver's backfill for other terminal paths; it seals nothing by
// itself. The archiver later fixes an explicit cutoff (the exact saga event
// IDs a bundle contains), publishes an immutable object, reads it back,
// verifies it and only then records the exact object identity (verified).
// Pruning deletes only the recorded event IDs of a verified bundle, under
// holds, and records the pruned watermark. Later saga events outside every
// bundle stay hot and produce a supplementary bundle (sequence + 1).
//
// The writer floor is not raised: an older writer finishing an operation
// omits the outbox row, which the backfill recreates. Readers older than
// contract 2 cannot read archived (pruned) saga history; migration 7 retires
// them, and pruning is held until the schema's reader floor proves it.
const evidenceArchiveMigrationSQL = `
	CREATE TABLE evidence_archive_intents (
		id TEXT PRIMARY KEY,
		subject_kind TEXT NOT NULL CHECK (subject_kind IN ('saga')),
		subject_id TEXT NOT NULL CHECK (length(subject_id) > 0),
		app TEXT NOT NULL DEFAULT '',
		operation_id TEXT NOT NULL DEFAULT '',
		sequence INTEGER NOT NULL CHECK (sequence >= 1),
		state TEXT NOT NULL CHECK (state IN ('pending', 'verified', 'pruned')),
		event_ids JSONB NOT NULL DEFAULT '[]',
		event_count INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0),
		cutoff_timestamp TIMESTAMPTZ,
		object_key TEXT NOT NULL DEFAULT '',
		object_sha256 TEXT NOT NULL DEFAULT '' CHECK (object_sha256 = '' OR object_sha256 ~ '^[0-9a-f]{64}$'),
		object_bytes BIGINT NOT NULL DEFAULT 0 CHECK (object_bytes >= 0),
		attempts INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '',
		pruned_events INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		verified_at TIMESTAMPTZ,
		pruned_at TIMESTAMPTZ,
		UNIQUE (subject_kind, subject_id, sequence),
		CHECK (state = 'pending' OR (object_key <> '' AND object_sha256 <> '' AND object_bytes > 0 AND verified_at IS NOT NULL)),
		CHECK (state <> 'pruned' OR pruned_at IS NOT NULL)
	);
	CREATE INDEX idx_evidence_archive_pending ON evidence_archive_intents (state, created_at) WHERE state = 'pending';
	CREATE INDEX idx_evidence_archive_subject ON evidence_archive_intents (subject_kind, subject_id, sequence);
`

func evidenceArchiveMigration() SchemaMigration {
	return SchemaMigration{
		Version:              6,
		Name:                 "evidence-archive-outbox",
		SQL:                  evidenceArchiveMigrationSQL,
		MinimumReaderVersion: 1,
		MinimumWriterVersion: 5,
	}
}

// Migration 7 retires hot-only saga history readers. Contract-1 readers read
// saga history from the hot table only and would serve pruned history as if
// complete, so the reader floor rises to EvidenceArchiveReaderVersion before
// any bundle may be pruned (pruning re-checks this floor as a hold).
func evidenceArchiveReaderMigration() SchemaMigration {
	return SchemaMigration{
		Version:              7,
		Name:                 "evidence-archive-reader-contract",
		SQL:                  `COMMENT ON TABLE evidence_archive_intents IS 'Evidence archive outbox and index; saga history readers must be archive-aware (reader contract 2).'`,
		MinimumReaderVersion: EvidenceArchiveReaderVersion,
		MinimumWriterVersion: 5,
	}
}
