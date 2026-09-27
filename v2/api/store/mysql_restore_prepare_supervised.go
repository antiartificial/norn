package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"norn/v2/api/artifactstore"
	"norn/v2/api/database"
)

// PrepareClaimedMySQLRestoreFromRetainedSupervised keeps an exact claim alive
// while the retained object is materialized and the target is preflighted.
// The caller must hand the same claim to MySQLRestoreRunner after this returns.
func (db *DB) PrepareClaimedMySQLRestoreFromRetainedSupervised(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLRestoreRequest, secrets database.SecretSource, objects artifactstore.Store, directory string, lease time.Duration) (intent MySQLRestoreIntent, runErr error) {
	if db == nil || db.Pool == nil || acceptance == nil || secrets == nil || objects == nil {
		return MySQLRestoreIntent{}, ErrMySQLRestoreFence
	}
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	supervisor, err := newMySQLRestoreClaimSupervisor(ctx, lease, func(renewCtx context.Context, duration time.Duration) error {
		return db.RenewOperationClaim(renewCtx, claim, duration)
	})
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	if err := supervisor.Start(); err != nil {
		return MySQLRestoreIntent{}, err
	}
	defer func() {
		if err := supervisor.Stop(); err != nil {
			intent = MySQLRestoreIntent{}
			runErr = errors.Join(runErr, fmt.Errorf("MySQL restore preparation claim renewal failed: %w", err))
		}
	}()
	intent, err = db.PrepareClaimedMySQLRestoreFromRetained(supervisor.Context(), acceptance, claim, request, secrets, objects, directory)
	if err != nil {
		return MySQLRestoreIntent{}, err
	}
	if err := supervisor.Failure(); err != nil {
		return MySQLRestoreIntent{}, err
	}
	return intent, nil
}
