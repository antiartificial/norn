package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var snapshotExportDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SnapshotExportIntent fixes the remote destination and exact bytes before
// the first create-only object write. A later claimant may adopt the same
// intent but cannot change the destination or accepted content.
type SnapshotExportIntent struct {
	OperationID, Bucket, ObjectKey string
	DumpSHA256, ManifestSHA256     string
	DumpSize                       int64
}

func (i SnapshotExportIntent) valid() bool {
	return strings.TrimSpace(i.OperationID) != "" && strings.TrimSpace(i.Bucket) != "" &&
		strings.TrimSpace(i.ObjectKey) != "" && i.DumpSize > 0 &&
		snapshotExportDigest.MatchString(i.DumpSHA256) && snapshotExportDigest.MatchString(i.ManifestSHA256)
}

func (db *DB) PrepareSnapshotExportIntent(ctx context.Context, claim OperationClaim, want SnapshotExportIntent) error {
	if err := validateOperationClaim(claim); err != nil || !want.valid() || want.OperationID != claim.OperationID() {
		return fmt.Errorf("snapshot export intent is incomplete")
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var held bool
	err = tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND status='running' AND locked_by=$2 AND lock_generation=$3 AND locked_until > clock_timestamp() FOR UPDATE`,
		claim.OperationID(), claim.OwnerID(), claim.Generation()).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return ownershipLost(claim)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO snapshot_export_intents
		(operation_id, object_key, bucket, dump_sha256, dump_size, manifest_sha256, origin_claim_generation, state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'prepared') ON CONFLICT (operation_id, object_key) DO NOTHING`,
		want.OperationID, want.ObjectKey, want.Bucket, want.DumpSHA256, want.DumpSize, want.ManifestSHA256, claim.Generation())
	if err != nil {
		return err
	}
	var got SnapshotExportIntent
	err = tx.QueryRow(ctx, `SELECT operation_id, bucket, object_key, dump_sha256, dump_size, manifest_sha256
		FROM snapshot_export_intents WHERE operation_id=$1 AND object_key=$2`, want.OperationID, want.ObjectKey).
		Scan(&got.OperationID, &got.Bucket, &got.ObjectKey, &got.DumpSHA256, &got.DumpSize, &got.ManifestSHA256)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("snapshot export differs from durable operation intent")
	}
	return tx.Commit(ctx)
}

// RecordSnapshotExportReceipt is claim-independent because a remote write may
// complete just as its worker loses the claim. Call only after readback of
// both exact objects; the immutable intent remains for successor inspection.
func (db *DB) RecordSnapshotExportReceipt(ctx context.Context, want SnapshotExportIntent) error {
	if !want.valid() {
		return fmt.Errorf("snapshot export receipt is incomplete")
	}
	result, err := db.Pool.Exec(ctx, `UPDATE snapshot_export_intents SET state='published', published_at=COALESCE(published_at, now())
		WHERE operation_id=$1 AND object_key=$2 AND bucket=$3 AND dump_sha256=$4 AND dump_size=$5
		AND manifest_sha256=$6 AND state IN ('prepared','published')`,
		want.OperationID, want.ObjectKey, want.Bucket, want.DumpSHA256, want.DumpSize, want.ManifestSHA256)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("snapshot export receipt differs from durable intent")
	}
	return nil
}
