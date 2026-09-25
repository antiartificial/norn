package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"norn/v2/api/database"
)

// MySQLSourceSnapshotRunner is a private, claim-supervised quiescence step.
// It stops the signed Nomad job and locks/drains the catalog-bound runtime
// account. It does not stage, publish, or authorize a snapshot artifact.
type MySQLSourceSnapshotRunner struct {
	Control    *DB
	Acceptance *PGOperationStore
	Secrets    database.SecretSource
	Stopper    MySQLSourceJobStopper
	ClaimLease time.Duration
}

var ErrMySQLSourceClaimLost = errors.New("MySQL source snapshot claim renewal failed; source remains fenced for inspection")

func (r MySQLSourceSnapshotRunner) RunClaimed(ctx context.Context, claim OperationClaim, request MySQLSourceSnapshotRequest) error {
	if r.Control == nil || r.Acceptance == nil || r.Acceptance.db != r.Control || r.Secrets == nil || r.Stopper == nil || validateOperationClaim(claim) != nil {
		return ErrMySQLSourceSnapshotFence
	}
	lease := r.ClaimLease
	if lease == 0 {
		lease = 2 * time.Minute
	}
	return runClaimedMySQLSourceQuiescence(ctx, lease,
		func(renewCtx context.Context, duration time.Duration) error {
			return r.Control.RenewOperationClaim(renewCtx, claim, duration)
		},
		func(runCtx context.Context) error {
			return r.Control.StopClaimedMySQLSourceJob(runCtx, r.Acceptance, claim, request, r.Stopper)
		},
		func(runCtx context.Context) error {
			return r.Control.LockClaimedMySQLSourceAccountWithDatabase(runCtx, r.Acceptance, claim, request, r.Secrets)
		})
}

// The same bounded supervisor used by the private restore runner renews before
// any external effect and cancels the shared context on ownership loss.
func runClaimedMySQLSourceQuiescence(ctx context.Context, lease time.Duration, renew func(context.Context, time.Duration) error, stop, lock func(context.Context) error) (runErr error) {
	if stop == nil || lock == nil {
		return ErrMySQLSourceSnapshotFence
	}
	supervisor, err := newMySQLRestoreClaimSupervisor(ctx, lease, renew)
	if err != nil {
		return err
	}
	if err := supervisor.Start(); err != nil {
		return errors.Join(ErrMySQLSourceClaimLost, err)
	}
	defer func() {
		if err := supervisor.Stop(); err != nil {
			runErr = errors.Join(runErr, ErrMySQLSourceClaimLost, err)
		}
	}()
	runCtx := supervisor.Context()
	if err := sourceClaimSupervisorReady(supervisor, runCtx); err != nil {
		return err
	}
	if err := stop(runCtx); err != nil {
		return err
	}
	if err := sourceClaimSupervisorReady(supervisor, runCtx); err != nil {
		return err
	}
	if err := lock(runCtx); err != nil {
		return err
	}
	if err := sourceClaimSupervisorReady(supervisor, runCtx); err != nil {
		return err
	}
	return nil
}

func sourceClaimSupervisorReady(supervisor *mysqlRestoreClaimSupervisor, ctx context.Context) error {
	if err := supervisor.Failure(); err != nil {
		return errors.Join(ErrMySQLSourceClaimLost, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("MySQL source snapshot context ended before next effect: %w", err)
	}
	return nil
}
