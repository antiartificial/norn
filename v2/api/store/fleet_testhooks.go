package store

// TEST HOOKS ONLY. The exported methods in this file are unsigned mutators
// kept for fixtures in other packages (Go's export_test.go cannot cross
// packages). They bypass signed operations and the fence rules: no production
// package may call them (verify with
// `grep -rnE 'RegisterFleetTarget|AdvanceFleetAuthorityEpoch' --include='*.go' . | grep -v _test.go`, which must list only
// this file).

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet/lifecycle"
)

// RegisterFleetTarget opens its own transaction and registers identity with
// aliases, attributed to operationID. It is idempotent: replaying the same
// identity (and any alias already bound to it) returns the existing target
// without bumping the registry generation again. See registerFleetTargetTx
// for the tx-internal core WP9a's signed-operation guard reuses directly so
// registration participates in that larger transaction instead of its own.
func (db *DB) RegisterFleetTarget(ctx context.Context, identity lifecycle.TargetIdentity, aliases []string, operationID string) (*FleetTarget, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	target, err := registerFleetTargetTx(ctx, tx, identity, aliases, operationID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return target, nil
}

// AdvanceFleetAuthorityEpoch CASes the singleton epoch forward from expected,
// recording reason and the server clock. It touches no fence or attempt
// row: old-epoch fences become Uncertain/AuthoritySuperseded until they are
// re-bound or released (plan.md §2.2), not rewritten here.
func (db *DB) AdvanceFleetAuthorityEpoch(ctx context.Context, expected int64, reason string) (int64, error) {
	var epoch int64
	err := db.Pool.QueryRow(ctx, `
		UPDATE fleet_authority_epoch SET epoch = epoch + 1, activated_at = now(), reason = $2
		WHERE singleton AND epoch = $1
		RETURNING epoch
	`, expected, reason).Scan(&epoch)
	if err == pgx.ErrNoRows {
		return 0, ErrFleetAuthorityEpochConflict
	}
	return epoch, err
}
