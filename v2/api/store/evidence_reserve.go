package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Migration 8 adds the durable evidence reserve (ADR 0001). Unarchived
// evidence stays hot when the archive is unavailable; the reserve bounds how
// much may accumulate before new audited mutations are refused, rather than
// letting the hot store fill or evidence be discarded. The policy and the
// archive's capacity observation are durable and shared by every process;
// the backlog is measured live from the outbox at admission time, so a
// stopped archiver cannot leave a stale "ok". Diagnostic log loss is never
// an input: dropping diagnostics under pressure does not free evidence
// reserve, and exhausted evidence reserve does not stop log collection.
//
// The writer floor rises to 6: a writer that does not enforce the reserve
// could keep admitting audited mutations past it.
const evidenceReserveMigrationSQL = `
	CREATE TABLE evidence_reserve (
		singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
		enabled BOOLEAN NOT NULL DEFAULT FALSE,
		max_pending INTEGER NOT NULL DEFAULT 10000 CHECK (max_pending > 0),
		max_pending_age_seconds INTEGER NOT NULL DEFAULT 86400 CHECK (max_pending_age_seconds > 0),
		archive_exhausted BOOLEAN NOT NULL DEFAULT FALSE,
		archive_detail TEXT NOT NULL DEFAULT '',
		archive_observed_at TIMESTAMPTZ,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);
	INSERT INTO evidence_reserve (singleton) VALUES (TRUE);
`

// EvidenceReserveWriterVersion is the writer contract that enforces the
// evidence reserve on audited mutations.
const EvidenceReserveWriterVersion int64 = 6

func evidenceReserveMigration() SchemaMigration {
	return SchemaMigration{
		Version:              8,
		Name:                 "evidence-reserve-admission",
		SQL:                  evidenceReserveMigrationSQL,
		MinimumReaderVersion: EvidenceArchiveReaderVersion,
		MinimumWriterVersion: EvidenceReserveWriterVersion,
	}
}

// EvidenceReservePolicy is the durable admission policy.
type EvidenceReservePolicy struct {
	Enabled       bool
	MaxPending    int
	MaxPendingAge time.Duration
}

// EvidenceReserveStatus is the admission decision input.
type EvidenceReserveStatus struct {
	Enabled          bool       `json:"enabled"`
	Exhausted        bool       `json:"exhausted"`
	Reasons          []string   `json:"reasons,omitempty"`
	Pending          int        `json:"pending"`
	OldestPendingAt  *time.Time `json:"oldestPendingAt,omitempty"`
	MaxPending       int        `json:"maxPending"`
	MaxPendingAge    string     `json:"maxPendingAge"`
	ArchiveExhausted bool       `json:"archiveExhausted"`
	ArchiveDetail    string     `json:"archiveDetail,omitempty"`
}

// EvidenceReserveExhaustedError refuses a new acceptance before it writes any
// domain state. Replays resolve before this check and remain available during
// an archive outage.
type EvidenceReserveExhaustedError struct{ Reasons []string }

func (e *EvidenceReserveExhaustedError) Error() string {
	return "evidence reserve exhausted: " + strings.Join(e.Reasons, "; ")
}

// reserveAcceptedEvidence creates the archive outbox reservation in the same
// transaction as signed operation acceptance. Locking the singleton policy
// row serializes the pending-count check with every new reservation. Saga
// operations retain their existing saga subject. Fleet GitHub actions reserve
// their operation subject before the external call. The operation becomes
// terminal only after GitHub responds, but the capacity decision must happen
// before that call so a full archive cannot create an unrecorded mutation.
func reserveAcceptedEvidence(ctx context.Context, tx pgx.Tx, acceptance OperationAcceptance) error {
	subjectKind, subjectID := "", ""
	switch {
	case acceptance.Operation.SagaID != "":
		subjectKind, subjectID = "saga", acceptance.Operation.SagaID
	case acceptance.Operation.Kind == "fleet.github.pull-request" || acceptance.Operation.Kind == "fleet.github.apply-dispatch":
		subjectKind, subjectID = "operation", acceptance.Operation.ID
	default:
		return nil
	}
	var enabled, archiveExhausted bool
	var maxPending, maxAgeSeconds int
	var archiveDetail string
	if err := tx.QueryRow(ctx, `SELECT enabled, max_pending, max_pending_age_seconds, archive_exhausted, archive_detail
		FROM evidence_reserve WHERE singleton FOR UPDATE`).Scan(&enabled, &maxPending, &maxAgeSeconds, &archiveExhausted, &archiveDetail); err != nil {
		return err
	}
	if enabled {
		var pending int
		var oldest *time.Time
		var databaseNow time.Time
		// clock_timestamp is evaluated after the policy-row lock is acquired.
		// Transaction-start now() could predate a long lock wait and admit work
		// whose oldest pending evidence has already crossed the age limit.
		if err := tx.QueryRow(ctx, `SELECT count(*), min(created_at), clock_timestamp() FROM evidence_archive_intents WHERE state = 'pending'`).Scan(&pending, &oldest, &databaseNow); err != nil {
			return err
		}
		reasons := []string{}
		if pending >= maxPending {
			reasons = append(reasons, fmt.Sprintf("%d unarchived evidence bundles reach the reserve limit of %d", pending, maxPending))
		}
		if oldest != nil && databaseNow.Sub(*oldest) >= time.Duration(maxAgeSeconds)*time.Second {
			reasons = append(reasons, fmt.Sprintf("evidence has waited for archiving longer than %s", time.Duration(maxAgeSeconds)*time.Second))
		}
		if archiveExhausted {
			reasons = append(reasons, "archive capacity is exhausted: "+archiveDetail)
		}
		if len(reasons) > 0 {
			return &EvidenceReserveExhaustedError{Reasons: reasons}
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO evidence_archive_intents (id, subject_kind, subject_id, app, operation_id, sequence, state)
		VALUES ($1, $2, $3, $4, $5, 1, 'pending')
		ON CONFLICT (subject_kind, subject_id, sequence) DO NOTHING`, "ei-"+uuid.NewString(), subjectKind, subjectID, acceptance.Operation.App, acceptance.Operation.ID)
	return err
}

// SetEvidenceReservePolicy records the policy. Enabling is done by
// processes with an archive configured; only an explicit operator setting
// disables it (removing archive configuration does not).
func (db *DB) SetEvidenceReservePolicy(ctx context.Context, policy EvidenceReservePolicy) error {
	if policy.MaxPending <= 0 || policy.MaxPendingAge <= 0 {
		return fmt.Errorf("evidence reserve limits must be positive")
	}
	_, err := db.Pool.Exec(ctx, `UPDATE evidence_reserve SET enabled = $1, max_pending = $2, max_pending_age_seconds = $3, updated_at = now() WHERE singleton`,
		policy.Enabled, policy.MaxPending, int(policy.MaxPendingAge/time.Second))
	return err
}

// RecordArchiveCapacity records the archiver's latest capacity observation.
func (db *DB) RecordArchiveCapacity(ctx context.Context, exhausted bool, detail string) error {
	_, err := db.Pool.Exec(ctx, `UPDATE evidence_reserve SET archive_exhausted = $1, archive_detail = $2, archive_observed_at = now(), updated_at = now() WHERE singleton`,
		exhausted, truncateError(detail))
	return err
}

// EvidenceReserve reads the policy and measures the live outbox backlog.
func (db *DB) EvidenceReserve(ctx context.Context) (EvidenceReserveStatus, error) {
	var status EvidenceReserveStatus
	var ageSeconds int
	err := db.Pool.QueryRow(ctx, `
		SELECT r.enabled, r.max_pending, r.max_pending_age_seconds, r.archive_exhausted, r.archive_detail,
		       (SELECT count(*) FROM evidence_archive_intents WHERE state = 'pending'),
		       (SELECT min(created_at) FROM evidence_archive_intents WHERE state = 'pending')
		FROM evidence_reserve r WHERE r.singleton`).Scan(&status.Enabled, &status.MaxPending, &ageSeconds, &status.ArchiveExhausted, &status.ArchiveDetail,
		&status.Pending, &status.OldestPendingAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return status, fmt.Errorf("evidence reserve state is missing")
	}
	if err != nil {
		return status, err
	}
	maxAge := time.Duration(ageSeconds) * time.Second
	status.MaxPendingAge = maxAge.String()
	if !status.Enabled {
		return status, nil
	}
	if status.Pending >= status.MaxPending {
		status.Reasons = append(status.Reasons, fmt.Sprintf("%d unarchived evidence bundles reach the reserve limit of %d", status.Pending, status.MaxPending))
	}
	if status.OldestPendingAt != nil && time.Since(*status.OldestPendingAt) >= maxAge {
		status.Reasons = append(status.Reasons, fmt.Sprintf("evidence has waited for archiving longer than %s", maxAge))
	}
	if status.ArchiveExhausted {
		status.Reasons = append(status.Reasons, "archive capacity is exhausted: "+status.ArchiveDetail)
	}
	status.Exhausted = len(status.Reasons) > 0
	return status, nil
}
