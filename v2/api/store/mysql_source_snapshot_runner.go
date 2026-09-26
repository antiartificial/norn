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
	Observer   MySQLSourceStoppedObserver
	Inspector  MySQLSourceAccountLockInspector
	Objects    MySQLSourceArtifactVerifier
	ClaimLease time.Duration
}

// RunClaimedReconciliation keeps the successor claim renewed while observing
// external state and committing the source/fence transfer. It performs no
// stop or account-lock mutation; continuation is a separate explicit step.
func (r MySQLSourceSnapshotRunner) RunClaimedReconciliation(ctx context.Context, claim OperationClaim) (runErr error) {
	if r.Control == nil || r.Acceptance == nil || r.Acceptance.db != r.Control || r.Secrets == nil ||
		r.Observer == nil || validateOperationClaim(claim) != nil {
		return ErrMySQLSourceSnapshotFence
	}
	inspector := r.Inspector
	if inspector == nil {
		inspector = mysqlSourceDatabaseLockInspector{}
	}
	lease := r.ClaimLease
	if lease == 0 {
		lease = 2 * time.Minute
	}
	supervisor, err := newMySQLRestoreClaimSupervisor(ctx, lease, func(renewCtx context.Context, duration time.Duration) error {
		return r.Control.RenewOperationClaim(renewCtx, claim, duration)
	})
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
	if err := r.Control.ReconcileClaimedMySQLSourceSnapshot(runCtx, r.Acceptance, claim, r.Observer, inspector, r.Secrets, r.Objects); err != nil {
		return err
	}
	return sourceClaimSupervisorReady(supervisor, runCtx)
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

// LockClaimedAfterReconciliation advances only a transferred stop-proved
// successor. It never sends another Nomad stop after an ambiguous response.
func (r MySQLSourceSnapshotRunner) LockClaimedAfterReconciliation(ctx context.Context, claim OperationClaim,
	request MySQLSourceSnapshotRequest) (runErr error) {
	if r.Control == nil || r.Acceptance == nil || r.Acceptance.db != r.Control || r.Secrets == nil ||
		validateOperationClaim(claim) != nil {
		return ErrMySQLSourceSnapshotFence
	}
	lease := r.ClaimLease
	if lease == 0 {
		lease = 2 * time.Minute
	}
	supervisor, err := newMySQLRestoreClaimSupervisor(ctx, lease, func(renewCtx context.Context, duration time.Duration) error {
		return r.Control.RenewOperationClaim(renewCtx, claim, duration)
	})
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
	if err := r.Control.LockClaimedMySQLSourceAccountWithDatabase(runCtx, r.Acceptance, claim, request, r.Secrets); err != nil {
		return err
	}
	return sourceClaimSupervisorReady(supervisor, runCtx)
}

// StageClaimed keeps the same operation claim alive through the durable stage
// intent, external dump, and signed receipt. A renewal loss cancels the dump
// context and leaves its stage-intended row fenced for inspection.
func (r MySQLSourceSnapshotRunner) StageClaimed(ctx context.Context, claim OperationClaim, request MySQLSourceSnapshotRequest, dumpToolPath, privateDirectory string, stager MySQLSourceArtifactStager) (SignedMySQLSourceArtifactReceipt, error) {
	if r.Control == nil || r.Acceptance == nil || r.Acceptance.db != r.Control || r.Secrets == nil || stager == nil || validateOperationClaim(claim) != nil {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceSnapshotFence
	}
	lease := r.ClaimLease
	if lease == 0 {
		lease = 2 * time.Minute
	}
	return r.Control.stageClaimedMySQLSourceArtifactSupervised(ctx, r.Acceptance, claim, request, r.Secrets, dumpToolPath, privateDirectory, stager, lease)
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

// runClaimedMySQLSourceArtifactStage keeps a source claim alive through the
// only externally mutable part of staging. It checks the supervisor after the
// dump returns, before the caller can sign or persist a receipt.
func runClaimedMySQLSourceArtifactStage(ctx context.Context, lease time.Duration, renew func(context.Context, time.Duration) error, stage func(context.Context, func() error) (SignedMySQLSourceArtifactReceipt, error)) (signed SignedMySQLSourceArtifactReceipt, runErr error) {
	if stage == nil {
		return SignedMySQLSourceArtifactReceipt{}, ErrMySQLSourceSnapshotFence
	}
	supervisor, err := newMySQLRestoreClaimSupervisor(ctx, lease, renew)
	if err != nil {
		return SignedMySQLSourceArtifactReceipt{}, err
	}
	if err := supervisor.Start(); err != nil {
		return SignedMySQLSourceArtifactReceipt{}, errors.Join(ErrMySQLSourceClaimLost, err)
	}
	defer func() {
		if err := supervisor.Stop(); err != nil {
			signed = SignedMySQLSourceArtifactReceipt{}
			runErr = errors.Join(runErr, ErrMySQLSourceClaimLost, err)
		}
	}()
	runCtx := supervisor.Context()
	if err := sourceClaimSupervisorReady(supervisor, runCtx); err != nil {
		return SignedMySQLSourceArtifactReceipt{}, err
	}
	signed, err = stage(runCtx, func() error { return sourceClaimSupervisorReady(supervisor, runCtx) })
	if err != nil {
		return SignedMySQLSourceArtifactReceipt{}, err
	}
	if err := sourceClaimSupervisorReady(supervisor, runCtx); err != nil {
		return SignedMySQLSourceArtifactReceipt{}, err
	}
	return signed, nil
}

// runClaimedMySQLSourceArtifactRetention has the same ownership boundary as
// staging, but covers publication and the mandatory full-object Verify pass.
// A claim loss returns no receipt and leaves publish-intended for inspection.
func runClaimedMySQLSourceArtifactRetention(ctx context.Context, lease time.Duration, renew func(context.Context, time.Duration) error, retain func(context.Context, func() error) (SignedMySQLSourceArtifactRetentionReceipt, error)) (signed SignedMySQLSourceArtifactRetentionReceipt, runErr error) {
	if retain == nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, ErrMySQLSourceSnapshotFence
	}
	supervisor, err := newMySQLRestoreClaimSupervisor(ctx, lease, renew)
	if err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, err
	}
	if err := supervisor.Start(); err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, errors.Join(ErrMySQLSourceClaimLost, err)
	}
	defer func() {
		if err := supervisor.Stop(); err != nil {
			signed = SignedMySQLSourceArtifactRetentionReceipt{}
			runErr = errors.Join(runErr, ErrMySQLSourceClaimLost, err)
		}
	}()
	runCtx := supervisor.Context()
	if err := sourceClaimSupervisorReady(supervisor, runCtx); err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, err
	}
	signed, err = retain(runCtx, func() error { return sourceClaimSupervisorReady(supervisor, runCtx) })
	if err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, err
	}
	if err := sourceClaimSupervisorReady(supervisor, runCtx); err != nil {
		return SignedMySQLSourceArtifactRetentionReceipt{}, err
	}
	return signed, nil
}
