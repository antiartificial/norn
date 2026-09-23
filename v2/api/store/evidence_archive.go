package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// EvidenceIntent is one row of the evidence archive outbox/index.
type EvidenceIntent struct {
	ID              string
	SubjectKind     string
	SubjectID       string
	App             string
	OperationID     string
	Sequence        int
	State           string
	EventIDs        []string
	EventCount      int
	CutoffTimestamp *time.Time
	ObjectKey       string
	ObjectSHA256    string
	ObjectBytes     int64
	Attempts        int
	LastError       string
	CreatedAt       time.Time
	VerifiedAt      *time.Time
	PrunedAt        *time.Time
	PrunedEvents    int
}

const evidenceIntentColumns = `id, subject_kind, subject_id, app, operation_id, sequence, state, event_ids, event_count, cutoff_timestamp,
	object_key, object_sha256, object_bytes, attempts, last_error, created_at, verified_at, pruned_at, pruned_events`

func scanEvidenceIntent(row pgx.Row) (EvidenceIntent, error) {
	var intent EvidenceIntent
	var ids []byte
	err := row.Scan(&intent.ID, &intent.SubjectKind, &intent.SubjectID, &intent.App, &intent.OperationID, &intent.Sequence, &intent.State, &ids,
		&intent.EventCount, &intent.CutoffTimestamp, &intent.ObjectKey, &intent.ObjectSHA256, &intent.ObjectBytes, &intent.Attempts, &intent.LastError,
		&intent.CreatedAt, &intent.VerifiedAt, &intent.PrunedAt, &intent.PrunedEvents)
	if err != nil {
		return EvidenceIntent{}, err
	}
	if err := json.Unmarshal(ids, &intent.EventIDs); err != nil {
		return EvidenceIntent{}, fmt.Errorf("evidence intent %s has malformed event ids", intent.ID)
	}
	return intent, nil
}

// terminalStatuses are the statuses whose operations are eligible evidence.
const terminalStatusSQL = `('succeeded', 'failed', 'canceled')`

// BackfillEvidenceIntents creates pending sequence-1 intents for terminal
// operations with a saga that finished before the grace period and have no
// intent yet (terminal paths other than FinishClaimedOperation, older
// writers, pre-existing history). It seals nothing.
func (db *DB) BackfillEvidenceIntents(ctx context.Context, finishedBefore time.Duration, limit int) (int64, error) {
	result, err := db.Pool.Exec(ctx, `
		INSERT INTO evidence_archive_intents (id, subject_kind, subject_id, app, operation_id, sequence, state)
		SELECT 'ei-' || gen_random_uuid()::text, 'saga', o.saga_id, o.app, o.id, 1, 'pending'
		FROM operations o
		WHERE o.status IN `+terminalStatusSQL+` AND o.saga_id <> '' AND o.finished_at < now() - $1::interval
		  AND NOT EXISTS (SELECT 1 FROM evidence_archive_intents i WHERE i.subject_kind = 'saga' AND i.subject_id = o.saga_id)
		ORDER BY o.finished_at
		LIMIT $2
		ON CONFLICT (subject_kind, subject_id, sequence) DO NOTHING`, finishedBefore.String(), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// EnsureSupplementaryEvidenceIntents creates the next-sequence pending
// intent for sagas that already have bundles but gained events outside every
// bundle (late publication after terminalization), once they are quiet.
func (db *DB) EnsureSupplementaryEvidenceIntents(ctx context.Context, quiet time.Duration) (int64, error) {
	result, err := db.Pool.Exec(ctx, `
		INSERT INTO evidence_archive_intents (id, subject_kind, subject_id, app, operation_id, sequence, state)
		SELECT 'ei-' || gen_random_uuid()::text, 'saga', latest.subject_id, latest.app, latest.operation_id, latest.sequence + 1, 'pending'
		FROM (
			SELECT DISTINCT ON (subject_id) subject_id, app, operation_id, sequence, state
			FROM evidence_archive_intents WHERE subject_kind = 'saga'
			ORDER BY subject_id, sequence DESC
		) latest
		WHERE latest.state IN ('verified', 'pruned')
		  AND EXISTS (
			SELECT 1 FROM saga_events e WHERE e.saga_id = latest.subject_id
			  AND NOT EXISTS (SELECT 1 FROM evidence_archive_intents j WHERE j.subject_kind = 'saga' AND j.subject_id = e.saga_id AND j.event_ids ? e.id))
		  AND NOT EXISTS (SELECT 1 FROM saga_events e WHERE e.saga_id = latest.subject_id AND e.timestamp > now() - $1::interval)
		ON CONFLICT (subject_kind, subject_id, sequence) DO NOTHING`, quiet.String())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// EvidenceSource is the hot evidence a bundle is built from, read inside
// the claiming transaction.
type EvidenceSource struct {
	OperationJSON json.RawMessage
	OperationKind string
	// EffectsJSON preserves terminal external-effect outcome and output
	// references exactly as held by PostgreSQL at archive cutoff.
	EffectsJSON  json.RawMessage
	Acceptance   *AcceptanceEvidenceRow
	DeploymentID string
	Events       []EvidenceEvent
}

// AcceptanceEvidenceRow is the persisted signed acceptance, byte-exact.
type AcceptanceEvidenceRow struct {
	IntentID              string
	RequestIdentityID     string
	RequestReceiptID      string
	FingerprintVersion    string
	FingerprintDigest     string
	RequestCanonicalBytes []byte
	CanonicalBytes        []byte
	CanonicalDigest       string
	SigningAlgorithm      string
	SigningKeyID          string
	Signature             string
}

// EvidenceEvent mirrors a saga_events row.
type EvidenceEvent struct {
	ID        string
	SagaID    string
	Timestamp time.Time
	Source    string
	App       string
	Category  string
	Action    string
	Message   string
	Metadata  map[string]string
}

// ErrNoEvidenceWork means no pending intent is ready.
var ErrNoEvidenceWork = errors.New("no evidence archive work is ready")

// ProcessPendingEvidenceIntent claims one pending intent whose operation is
// terminal and whose saga has been quiet for the given period (FOR UPDATE
// SKIP LOCKED), loads its hot evidence (events not in any earlier bundle)
// and calls publish inside the same transaction. publish must upload,
// read back and verify the object and return its exact identity; the intent
// is then acknowledged (verified) in the same transaction. A publish error
// is recorded on the intent, which stays pending (the evidence stays hot).
func (db *DB) ProcessPendingEvidenceIntent(ctx context.Context, quiet time.Duration, publish func(context.Context, EvidenceIntent, EvidenceSource) (EvidencePublication, error)) (EvidenceIntent, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return EvidenceIntent{}, err
	}
	defer tx.Rollback(ctx)
	intent, err := scanEvidenceIntent(tx.QueryRow(ctx, `
		SELECT `+evidenceIntentColumns+` FROM evidence_archive_intents i
		WHERE i.state = 'pending' AND i.subject_kind = 'saga'
		  AND NOT EXISTS (SELECT 1 FROM operations o WHERE o.saga_id = i.subject_id AND o.status NOT IN `+terminalStatusSQL+`)
		  AND NOT EXISTS (SELECT 1 FROM saga_events e WHERE e.saga_id = i.subject_id AND e.timestamp > now() - $1::interval)
		ORDER BY i.created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, quiet.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return EvidenceIntent{}, ErrNoEvidenceWork
	}
	if err != nil {
		return EvidenceIntent{}, err
	}
	source, err := loadEvidenceSource(ctx, tx, intent)
	if err != nil {
		return intent, err
	}
	publication, publishErr := publish(ctx, intent, source)
	if publishErr != nil {
		if _, err := tx.Exec(ctx, `UPDATE evidence_archive_intents SET attempts = attempts + 1, last_error = $2, updated_at = now() WHERE id = $1`, intent.ID, truncateError(publishErr.Error())); err != nil {
			return intent, err
		}
		if err := tx.Commit(ctx); err != nil {
			return intent, err
		}
		return intent, publishErr
	}
	ids, err := json.Marshal(publication.EventIDs)
	if err != nil {
		return intent, err
	}
	result, err := tx.Exec(ctx, `
		UPDATE evidence_archive_intents
		SET state = 'verified', event_ids = $2, event_count = $3, cutoff_timestamp = $4, object_key = $5, object_sha256 = $6, object_bytes = $7,
		    attempts = attempts + 1, last_error = '', verified_at = now(), updated_at = now()
		WHERE id = $1 AND state = 'pending'`, intent.ID, ids, len(publication.EventIDs), publication.CutoffTimestamp, publication.ObjectKey, publication.ObjectSHA256, publication.ObjectBytes)
	if err != nil {
		return intent, err
	}
	if result.RowsAffected() != 1 {
		return intent, fmt.Errorf("evidence intent %s changed during publication", intent.ID)
	}
	if err := tx.Commit(ctx); err != nil {
		return intent, err
	}
	return db.EvidenceIntent(ctx, intent.ID)
}

// EvidencePublication is the verified object identity and exact cutoff.
type EvidencePublication struct {
	EventIDs        []string
	CutoffTimestamp *time.Time
	ObjectKey       string
	ObjectSHA256    string
	ObjectBytes     int64
}

func truncateError(message string) string {
	if len(message) > 1000 {
		return message[:1000]
	}
	return message
}

func loadEvidenceSource(ctx context.Context, tx pgx.Tx, intent EvidenceIntent) (EvidenceSource, error) {
	var source EvidenceSource
	if intent.OperationID != "" {
		if err := tx.QueryRow(ctx, `SELECT row_to_json(o)::text::jsonb, o.kind FROM operations o WHERE o.id = $1`, intent.OperationID).Scan(&source.OperationJSON, &source.OperationKind); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return source, err
		}
		var acceptance AcceptanceEvidenceRow
		var receipt *string
		err := tx.QueryRow(ctx, `SELECT id, request_identity_id, request_receipt_id, fingerprint_version, fingerprint_digest, request_canonical_bytes, canonical_bytes,
			canonical_digest, signing_algorithm, signing_key_id, signature FROM operation_acceptance_intents WHERE operation_id = $1`, intent.OperationID).Scan(
			&acceptance.IntentID, &acceptance.RequestIdentityID, &receipt, &acceptance.FingerprintVersion, &acceptance.FingerprintDigest,
			&acceptance.RequestCanonicalBytes, &acceptance.CanonicalBytes, &acceptance.CanonicalDigest, &acceptance.SigningAlgorithm, &acceptance.SigningKeyID, &acceptance.Signature)
		switch {
		case err == nil:
			if receipt != nil {
				acceptance.RequestReceiptID = *receipt
			}
			source.Acceptance = &acceptance
		case !errors.Is(err, pgx.ErrNoRows):
			return source, err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(row_to_json(f) ORDER BY f.created_at, f.id), '[]'::jsonb)::text::jsonb
			FROM operation_effects f WHERE f.operation_id = $1`, intent.OperationID).Scan(&source.EffectsJSON); err != nil {
			return source, err
		}
	}
	_ = tx.QueryRow(ctx, `SELECT id FROM deployments WHERE saga_id = $1 ORDER BY started_at DESC LIMIT 1`, intent.SubjectID).Scan(&source.DeploymentID)
	rows, err := tx.Query(ctx, `
		SELECT e.id, e.saga_id, e.timestamp, e.source, e.app, e.category, e.action, e.message, e.metadata
		FROM saga_events e
		WHERE e.saga_id = $1
		  AND NOT EXISTS (SELECT 1 FROM evidence_archive_intents j WHERE j.subject_kind = 'saga' AND j.subject_id = e.saga_id AND j.id <> $2 AND j.event_ids ? e.id)
		ORDER BY e.timestamp, e.id`, intent.SubjectID, intent.ID)
	if err != nil {
		return source, err
	}
	defer rows.Close()
	for rows.Next() {
		var event EvidenceEvent
		var metadata []byte
		if err := rows.Scan(&event.ID, &event.SagaID, &event.Timestamp, &event.Source, &event.App, &event.Category, &event.Action, &event.Message, &metadata); err != nil {
			return source, err
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &event.Metadata); err != nil {
				return source, fmt.Errorf("saga event %s has malformed metadata", event.ID)
			}
		}
		source.Events = append(source.Events, event)
	}
	return source, rows.Err()
}

// EvidenceIntent reads one intent.
func (db *DB) EvidenceIntent(ctx context.Context, id string) (EvidenceIntent, error) {
	return scanEvidenceIntent(db.Pool.QueryRow(ctx, `SELECT `+evidenceIntentColumns+` FROM evidence_archive_intents WHERE id = $1`, id))
}

// EvidenceIntentsForSubject lists a subject's intents by sequence.
func (db *DB) EvidenceIntentsForSubject(ctx context.Context, kind, subjectID string) ([]EvidenceIntent, error) {
	rows, err := db.Pool.Query(ctx, `SELECT `+evidenceIntentColumns+` FROM evidence_archive_intents WHERE subject_kind = $1 AND subject_id = $2 ORDER BY sequence`, kind, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceIntent
	for rows.Next() {
		intent, err := scanEvidenceIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, intent)
	}
	return out, rows.Err()
}

// EvidenceHolds returns the reasons a verified bundle's events must stay
// hot. An empty result permits pruning. Holds (retention handoff):
//   - the saga's operation is non-terminal, needs manual recovery, awaits
//     external effect recovery, or has unresolved effects;
//   - the saga's deployment is non-terminal, or is one of the app's two
//     latest deployed deployments (current and rollback candidate);
//   - the operation finished less than minAge ago (replay/incident margin);
//   - the schema still admits hot-only (pre-archive) readers.
func evidenceHolds(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, intent EvidenceIntent, minAge time.Duration) ([]string, error) {
	var holds []string
	check := func(reason, query string, args ...any) error {
		var held bool
		if err := q.QueryRow(ctx, query, args...).Scan(&held); err != nil {
			return err
		}
		if held {
			holds = append(holds, reason)
		}
		return nil
	}
	checks := []struct {
		reason, query string
	}{
		{"operation-active", `SELECT EXISTS (SELECT 1 FROM operations WHERE saga_id = $1 AND status NOT IN ` + terminalStatusSQL + `)`},
		{"manual-recovery", `SELECT EXISTS (SELECT 1 FROM operations WHERE saga_id = $1 AND (metadata->>'manualRecoveryRequired' = 'true' OR metadata->>'externalEffectRecoveryPending' = 'true'))`},
		{"unresolved-effect", `SELECT EXISTS (SELECT 1 FROM operation_effects f JOIN operations o ON o.id = f.operation_id WHERE o.saga_id = $1 AND f.lifecycle <> 'resolved')`},
		{"deployment-active", `SELECT EXISTS (SELECT 1 FROM deployments WHERE saga_id = $1 AND status NOT IN ('deployed', 'failed'))`},
		{"deployment-current-or-rollback", `SELECT EXISTS (SELECT 1 FROM deployments d WHERE d.saga_id = $1 AND d.status = 'deployed' AND d.id IN (
			SELECT id FROM deployments x WHERE x.app = d.app AND x.status = 'deployed' ORDER BY x.started_at DESC LIMIT 2))`},
	}
	for _, c := range checks {
		if err := check(c.reason, c.query, intent.SubjectID); err != nil {
			return nil, err
		}
	}
	if err := check("retention-age", `SELECT EXISTS (SELECT 1 FROM operations WHERE saga_id = $1 AND (finished_at IS NULL OR finished_at > now() - $2::interval))`, intent.SubjectID, minAge.String()); err != nil {
		return nil, err
	}
	// Hot-only (contract-1) readers must be retired before history leaves
	// the hot table; migration 7 raises the floor, this re-proves it.
	if err := check("legacy-readers-admitted", `SELECT NOT EXISTS (SELECT 1 FROM norn_schema_compatibility WHERE singleton AND minimum_reader_version >= $1)`, EvidenceArchiveReaderVersion); err != nil {
		return nil, err
	}
	// The floor only stops old binaries from starting. Already-running ones
	// are visible as sessions of the control role on this database that do
	// not declare an archive-aware reader contract (pre-archive binaries set
	// no declaration at all); any such session holds pruning.
	if err := check("unretired-readers-connected", unretiredReaderSessionsSQL, EvidenceArchiveReaderVersion); err != nil {
		return nil, err
	}
	return holds, nil
}

const unretiredReaderSessionsSQL = `SELECT EXISTS (
	SELECT 1 FROM pg_stat_activity
	WHERE datname = current_database() AND usename = current_user AND backend_type = 'client backend' AND pid <> pg_backend_pid()
	  AND coalesce(substring(application_name from '^norn/reader=([0-9]+)/')::int, 0) < $1)`

// UnretiredReaderSessions lists the application names of control-role
// sessions that hold pruning because they declare no archive-aware reader
// contract.
func (db *DB) UnretiredReaderSessions(ctx context.Context) ([]string, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT coalesce(nullif(application_name, ''), '(unnamed)') FROM pg_stat_activity
		WHERE datname = current_database() AND usename = current_user AND backend_type = 'client backend' AND pid <> pg_backend_pid()
		  AND coalesce(substring(application_name from '^norn/reader=([0-9]+)/')::int, 0) < $1
		ORDER BY 1`, EvidenceArchiveReaderVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// EvidenceHolds reports the current holds on an intent.
func (db *DB) EvidenceHolds(ctx context.Context, intent EvidenceIntent, minAge time.Duration) ([]string, error) {
	return evidenceHolds(ctx, db.Pool, intent, minAge)
}

// PruneVerifiedEvidence deletes exactly the saga events recorded in a
// verified bundle and records the pruned watermark. It never holds locks
// across external I/O:
//  1. holds are checked without locks (cheap early refusal);
//  2. verify re-proves the archived object's recorded identity (external);
//  3. a short deletion transaction locks the intent, the saga's operation
//     and deployment rows and the app's deployed rows (so writers that would
//     create a hold — a recovery flag, a new effect row through its foreign
//     key, a status change — are serialized with the deletion), re-checks
//     every hold including the reader floor and connected readers, and only
//     then deletes. A hold committed during verification is therefore seen.
func (db *DB) PruneVerifiedEvidence(ctx context.Context, intentID string, minAge time.Duration, verify func(context.Context, EvidenceIntent) error) (int, []string, error) {
	intent, err := db.EvidenceIntent(ctx, intentID)
	if err != nil {
		return 0, nil, err
	}
	if intent.State != "verified" {
		return 0, nil, fmt.Errorf("evidence intent %s is %s, not verified", intent.ID, intent.State)
	}
	if holds, err := evidenceHolds(ctx, db.Pool, intent, minAge); err != nil || len(holds) > 0 {
		return 0, holds, err
	}
	if err := verify(ctx, intent); err != nil {
		return 0, nil, fmt.Errorf("archived object failed verification before pruning: %w", err)
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	locked, err := scanEvidenceIntent(tx.QueryRow(ctx, `SELECT `+evidenceIntentColumns+` FROM evidence_archive_intents WHERE id = $1 FOR UPDATE`, intentID))
	if err != nil {
		return 0, nil, err
	}
	if locked.State != "verified" || locked.ObjectSHA256 != intent.ObjectSHA256 || !equalStrings(locked.EventIDs, intent.EventIDs) {
		return 0, nil, fmt.Errorf("evidence intent %s changed during verification", intent.ID)
	}
	for _, lock := range []string{
		`SELECT 1 FROM operations WHERE saga_id = $1 FOR UPDATE`,
		`SELECT 1 FROM deployments WHERE saga_id = $1 FOR UPDATE`,
		`SELECT 1 FROM deployments WHERE status = 'deployed' AND app IN (SELECT app FROM deployments WHERE saga_id = $1) FOR SHARE`,
	} {
		if _, err := tx.Exec(ctx, lock, intent.SubjectID); err != nil {
			return 0, nil, err
		}
	}
	holds, err := evidenceHolds(ctx, tx, locked, minAge)
	if err != nil {
		return 0, nil, err
	}
	if len(holds) > 0 {
		return 0, holds, nil
	}
	ids, _ := json.Marshal(intent.EventIDs)
	result, err := tx.Exec(ctx, `DELETE FROM saga_events WHERE saga_id = $1 AND id IN (SELECT jsonb_array_elements_text($2::jsonb))`, intent.SubjectID, ids)
	if err != nil {
		return 0, nil, err
	}
	pruned := int(result.RowsAffected())
	if _, err := tx.Exec(ctx, `UPDATE evidence_archive_intents SET state = 'pruned', pruned_at = now(), pruned_events = $2, updated_at = now() WHERE id = $1`, intent.ID, pruned); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return pruned, nil, nil
}

// PrunedEvidenceIntents pages through pruned saga bundles holding events,
// newest cutoff first, optionally for one app ("" for all apps).
func (db *DB) PrunedEvidenceIntents(ctx context.Context, app string, offset, limit int) ([]EvidenceIntent, error) {
	rows, err := db.Pool.Query(ctx, `SELECT `+evidenceIntentColumns+` FROM evidence_archive_intents
		WHERE subject_kind = 'saga' AND state = 'pruned' AND event_count > 0 AND ($1 = '' OR app = $1)
		ORDER BY cutoff_timestamp DESC NULLS LAST, id DESC OFFSET $2 LIMIT $3`, app, offset, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceIntent
	for rows.Next() {
		intent, err := scanEvidenceIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, intent)
	}
	return out, rows.Err()
}

// VerifiedEvidenceIntents lists verified (not yet pruned) intents, oldest
// first.
func (db *DB) VerifiedEvidenceIntents(ctx context.Context, limit int) ([]EvidenceIntent, error) {
	rows, err := db.Pool.Query(ctx, `SELECT `+evidenceIntentColumns+` FROM evidence_archive_intents WHERE state = 'verified' ORDER BY verified_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceIntent
	for rows.Next() {
		intent, err := scanEvidenceIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, intent)
	}
	return out, rows.Err()
}

// RestoreEvidenceIntent records a pruned bundle found in the archive (index
// recovery without historical PostgreSQL). It never overwrites an existing
// row for the same subject and sequence.
func (db *DB) RestoreEvidenceIntent(ctx context.Context, intent EvidenceIntent) (bool, error) {
	ids, err := json.Marshal(intent.EventIDs)
	if err != nil {
		return false, err
	}
	result, err := db.Pool.Exec(ctx, `
		INSERT INTO evidence_archive_intents (id, subject_kind, subject_id, app, operation_id, sequence, state, event_ids, event_count, cutoff_timestamp,
			object_key, object_sha256, object_bytes, verified_at, pruned_at, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, 'pruned', $7, $8, $9, $10, $11, $12, now(), now(), 'restored from archive index')
		ON CONFLICT (subject_kind, subject_id, sequence) DO NOTHING`,
		intent.ID, intent.SubjectKind, intent.SubjectID, intent.App, intent.OperationID, intent.Sequence, ids, len(intent.EventIDs), intent.CutoffTimestamp,
		intent.ObjectKey, intent.ObjectSHA256, intent.ObjectBytes)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}
