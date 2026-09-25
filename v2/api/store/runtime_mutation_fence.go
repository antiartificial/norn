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
var ErrRuntimeMutationFenceOwnershipLost = errors.New("runtime mutation fence ownership lost")

// AcquireRuntimeMutationFence installs the next durable epoch. Empty owner or
// reason is rejected so an active stop is always attributable and explainable.
func (db *DB) AcquireRuntimeMutationFence(ctx context.Context, owner, reason string) (RuntimeMutationFence, error) {
	if db == nil || db.Pool == nil || strings.TrimSpace(owner) == "" || strings.TrimSpace(reason) == "" {
		return RuntimeMutationFence{}, fmt.Errorf("runtime mutation fence owner and reason are required")
	}
	var fence RuntimeMutationFence
	err := db.Pool.QueryRow(ctx, `
		UPDATE runtime_mutation_fence
		SET epoch=epoch+1, active=true, owner=$1, reason=$2, held_at=clock_timestamp(), released_at=NULL
		WHERE singleton=true AND active=false
		RETURNING epoch, owner, reason, held_at`, owner, reason).Scan(&fence.Epoch, &fence.Owner, &fence.Reason, &fence.HeldAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeMutationFence{}, ErrRuntimeMutationFenceHeld
	}
	return fence, err
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
		RETURNING true`, fence.Epoch, fence.Owner).Scan(&released)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRuntimeMutationFenceOwnershipLost
	}
	return err
}
