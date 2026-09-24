package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SnapshotPublicationIntent is the immutable description of the one public
// snapshot pair an operation may create.  It is committed before any sidecar
// or dump is visible, so a successor can finish exactly that publication after
// the original PostgreSQL session disappears.
type SnapshotPublicationIntent struct {
	OperationID           string
	OriginClaimGeneration int64
	EffectID              string
	InputDigest           string
	SupervisorRootID      string
	SupervisorExecutionID string
	Target                json.RawMessage
	CatalogRevision       int64
	Namespace             string
	Filename              string
	SHA256                string
	Size                  int64
}

func (i SnapshotPublicationIntent) valid() bool {
	return strings.TrimSpace(i.OperationID) != "" && i.OriginClaimGeneration > 0 &&
		strings.TrimSpace(i.EffectID) != "" && strings.HasPrefix(i.InputDigest, "sha256:") &&
		strings.TrimSpace(i.SupervisorRootID) != "" && strings.TrimSpace(i.SupervisorExecutionID) != "" &&
		json.Valid(i.Target) && i.CatalogRevision > 0 && strings.TrimSpace(i.Namespace) != "" &&
		strings.TrimSpace(i.Filename) != "" && len(i.SHA256) == 64 && i.Size > 0
}

func sameSnapshotPublicationIntent(a, b SnapshotPublicationIntent) bool {
	var left, right any
	if json.Unmarshal(a.Target, &left) != nil || json.Unmarshal(b.Target, &right) != nil {
		return false
	}
	return a.OperationID == b.OperationID && a.EffectID == b.EffectID && a.InputDigest == b.InputDigest &&
		a.SupervisorRootID == b.SupervisorRootID && a.SupervisorExecutionID == b.SupervisorExecutionID &&
		reflect.DeepEqual(left, right) && a.CatalogRevision == b.CatalogRevision && a.Namespace == b.Namespace &&
		a.Filename == b.Filename && a.SHA256 == b.SHA256 && a.Size == b.Size
}

// PrepareSnapshotPublicationIntent binds a claim to the immutable public
// effect before filesystem publication.  A later claimant may adopt the same
// operation intent, but a changed descriptor always fails closed.
func (db *DB) PrepareSnapshotPublicationIntent(ctx context.Context, claim OperationClaim, want SnapshotPublicationIntent) (SnapshotPublicationIntent, error) {
	want.OriginClaimGeneration = claim.Generation()
	if err := validateOperationClaim(claim); err != nil || !want.valid() || want.OperationID != claim.OperationID() {
		return SnapshotPublicationIntent{}, fmt.Errorf("snapshot publication intent is incomplete")
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return SnapshotPublicationIntent{}, err
	}
	defer tx.Rollback(context.Background())
	var held bool
	err = tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND status='running' AND locked_by=$2 AND lock_generation=$3 AND locked_until > clock_timestamp() FOR UPDATE`, claim.OperationID(), claim.OwnerID(), claim.Generation()).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotPublicationIntent{}, ownershipLost(claim)
	}
	if err != nil {
		return SnapshotPublicationIntent{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO snapshot_publication_intents
		(operation_id, origin_claim_generation, effect_id, input_digest, supervisor_root_id, supervisor_execution_id, target, catalog_revision, namespace, filename, sha256, size, state)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,$12,'prepared') ON CONFLICT (operation_id) DO NOTHING`,
		want.OperationID, want.OriginClaimGeneration, want.EffectID, want.InputDigest, want.SupervisorRootID, want.SupervisorExecutionID, []byte(want.Target), want.CatalogRevision, want.Namespace, want.Filename, want.SHA256, want.Size)
	if err != nil {
		return SnapshotPublicationIntent{}, err
	}
	got, err := snapshotPublicationIntent(ctx, tx, want.OperationID)
	if err != nil {
		return SnapshotPublicationIntent{}, err
	}
	if !sameSnapshotPublicationIntent(got, want) {
		return SnapshotPublicationIntent{}, fmt.Errorf("snapshot publication intent differs from durable operation intent")
	}
	if err := tx.Commit(ctx); err != nil {
		return SnapshotPublicationIntent{}, err
	}
	return got, nil
}

// RecordSnapshotPublicationReceipt records that the exact sidecar/dump pair
// has been read back. It deliberately has no claim predicate: a process can
// lose its claim connection after Link, and the successor must be able to
// retain that already verified immutable effect.
func (db *DB) RecordSnapshotPublicationReceipt(ctx context.Context, want SnapshotPublicationIntent) error {
	if !want.valid() {
		return fmt.Errorf("snapshot publication receipt is incomplete")
	}
	result, err := db.Pool.Exec(ctx, `UPDATE snapshot_publication_intents SET state='published', published_at=COALESCE(published_at, now())
		WHERE operation_id=$1 AND effect_id=$2 AND input_digest=$3 AND supervisor_root_id=$4 AND supervisor_execution_id=$5
		AND target=$6::jsonb AND catalog_revision=$7 AND namespace=$8 AND filename=$9 AND sha256=$10 AND size=$11 AND state IN ('prepared','published')`,
		want.OperationID, want.EffectID, want.InputDigest, want.SupervisorRootID, want.SupervisorExecutionID, []byte(want.Target), want.CatalogRevision, want.Namespace, want.Filename, want.SHA256, want.Size)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("snapshot publication receipt differs from durable intent")
	}
	return nil
}

func (db *DB) HasSnapshotPublicationReceipt(ctx context.Context, operationID string) (bool, error) {
	var published bool
	err := db.Pool.QueryRow(ctx, `SELECT true FROM snapshot_publication_intents WHERE operation_id=$1 AND state='published'`, operationID).Scan(&published)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return published, err
}

func snapshotPublicationIntent(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, operationID string) (SnapshotPublicationIntent, error) {
	var intent SnapshotPublicationIntent
	var target []byte
	var publishedAt *time.Time
	err := q.QueryRow(ctx, `SELECT operation_id, origin_claim_generation, effect_id, input_digest, supervisor_root_id, supervisor_execution_id, target, catalog_revision, namespace, filename, sha256, size, published_at FROM snapshot_publication_intents WHERE operation_id=$1`, operationID).
		Scan(&intent.OperationID, &intent.OriginClaimGeneration, &intent.EffectID, &intent.InputDigest, &intent.SupervisorRootID, &intent.SupervisorExecutionID, &target, &intent.CatalogRevision, &intent.Namespace, &intent.Filename, &intent.SHA256, &intent.Size, &publishedAt)
	intent.Target = append(intent.Target[:0], target...)
	return intent, err
}
