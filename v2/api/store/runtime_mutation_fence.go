package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// RuntimeMutationFence is the durable, global admission fence for app runtime
// effects. Epoch is monotonic: a release must present the exact owner and epoch
// it acquired, so an old holder cannot release a replacement fence.
type RuntimeMutationFence struct {
	Epoch  int64
	Owner  string
	Reason string
	HeldAt time.Time
}

var ErrRuntimeMutationFenceHeld = errors.New("runtime mutation fence is already held")
var ErrRuntimeMutationFenceBusy = errors.New("runtime mutation operations are still running")
var ErrRuntimeMutationFenceOwnershipLost = errors.New("runtime mutation fence ownership lost")

// RuntimeMutationFenceActive reads only the singleton state used by claim
// admission. A missing singleton is an error so callers can fail closed.
func (db *DB) RuntimeMutationFenceActive(ctx context.Context) (bool, error) {
	if db == nil || db.Pool == nil {
		return false, fmt.Errorf("runtime mutation fence database is unavailable")
	}
	var active bool
	err := db.Pool.QueryRow(ctx, `SELECT active FROM runtime_mutation_fence WHERE singleton=true`).Scan(&active)
	return active, err
}

// AcquireRuntimeMutationFence installs the next durable epoch. Empty owner or
// reason is rejected so an active stop is always attributable and explainable.
func (db *DB) AcquireRuntimeMutationFence(ctx context.Context, owner, reason string) (RuntimeMutationFence, error) {
	if db == nil || db.Pool == nil || strings.TrimSpace(owner) == "" || strings.TrimSpace(reason) == "" {
		return RuntimeMutationFence{}, fmt.Errorf("runtime mutation fence owner and reason are required")
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RuntimeMutationFence{}, err
	}
	defer tx.Rollback(context.Background())
	var fence RuntimeMutationFence
	err = tx.QueryRow(ctx, `
		UPDATE runtime_mutation_fence
		SET epoch=epoch+1, active=true, owner=$1, reason=$2, held_at=clock_timestamp(), released_at=NULL
		WHERE singleton=true AND active=false
		RETURNING epoch, owner, reason, held_at`, owner, reason).Scan(&fence.Epoch, &fence.Owner, &fence.Reason, &fence.HeldAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeMutationFence{}, ErrRuntimeMutationFenceHeld
	}
	if err != nil {
		return RuntimeMutationFence{}, err
	}
	var running bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM operations WHERE status='running' AND kind IN
		('app.deploy','app.rollback','app.restart','app.scale','app.canary-promote',
		 'app.cron-pause','app.cron-resume','app.cron-schedule','app.cron-trigger',
		 'app.cron-trigger-reconcile','app.function-invoke','host.assure'))`).Scan(&running); err != nil {
		return RuntimeMutationFence{}, err
	}
	if running {
		return RuntimeMutationFence{}, ErrRuntimeMutationFenceBusy
	}
	if err := tx.Commit(ctx); err != nil {
		return RuntimeMutationFence{}, err
	}
	return fence, nil
}

// ReleaseRuntimeMutationFence only clears the precise epoch acquired above.
func (db *DB) ReleaseRuntimeMutationFence(ctx context.Context, fence RuntimeMutationFence) error {
	if db == nil || db.Pool == nil || fence.Epoch <= 0 || strings.TrimSpace(fence.Owner) == "" {
		return ErrRuntimeMutationFenceOwnershipLost
	}
	var released bool
	err := db.Pool.QueryRow(ctx, `
		UPDATE runtime_mutation_fence
		SET active=false, owner='', reason='', released_at=clock_timestamp()
		WHERE singleton=true AND active=true AND epoch=$1 AND owner=$2
          AND NOT EXISTS (SELECT 1 FROM mysql_source_snapshot_intents
             WHERE runtime_fence_epoch=$1 AND runtime_fence_owner=$2)
          AND NOT EXISTS (SELECT 1 FROM mysql_restore_maintenance_fences
             WHERE runtime_fence_epoch=$1 AND ('mysql-restore:' || operation_id)=$2)
		RETURNING true`, fence.Epoch, fence.Owner).Scan(&released)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRuntimeMutationFenceOwnershipLost
	}
	return err
}
