package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

var ErrMySQLRestoreTargetUnlockIndeterminate = errors.New("MySQL restore target unlock may have executed; inspection required")

// MySQLRestoreRecoveryRunner performs the private destination unlock and
// separately supervised fence release. It never unlocks the stopped source.
type MySQLRestoreRecoveryRunner struct {
	Control    *DB
	Acceptance *PGOperationStore
	Observer   MySQLSourceStoppedObserver
	Secrets    database.SecretSource
	ClaimLease time.Duration
}

func (r MySQLRestoreRecoveryRunner) RunClaimedTargetUnlock(ctx context.Context, claim OperationClaim) (runErr error) {
	if r.Control == nil || r.Acceptance == nil || r.Observer == nil || r.Secrets == nil {
		return ErrMySQLRestoreFence
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
		return err
	}
	defer func() {
		if err := supervisor.Stop(); err != nil && runErr == nil {
			runErr = err
		}
	}()
	runCtx := supervisor.Context()
	intent, err := r.Control.IntendClaimedMySQLRestoreTargetUnlock(runCtx, r.Acceptance, claim, r.Observer, r.Secrets)
	if err != nil {
		return err
	}
	if err := supervisor.Failure(); err != nil {
		return errors.Join(ErrMySQLRestoreTargetUnlockIndeterminate, err)
	}
	ready, err := r.Control.AssessCompletedMySQLRestoreLiveSource(runCtx, r.Acceptance, intent.RestoreOperationID, r.Observer, r.Secrets)
	if err != nil {
		return errors.Join(ErrMySQLRestoreTargetUnlockIndeterminate, err)
	}
	checkedTarget, err := r.Control.AssessCompletedMySQLRestoreLiveRecovery(runCtx, r.Acceptance, intent.RestoreOperationID, r.Secrets)
	if err != nil || checkedTarget.Fence != ready.Fence || checkedTarget.Request != ready.Request {
		return ErrMySQLRestoreTargetUnlockIndeterminate
	}
	if err := supervisor.Failure(); err != nil {
		return errors.Join(ErrMySQLRestoreTargetUnlockIndeterminate, err)
	}
	resolved, err := r.Control.resolvedMySQLRecoveryTarget(runCtx, ready)
	if err != nil {
		return errors.Join(ErrMySQLRestoreTargetUnlockIndeterminate, err)
	}
	if err := database.UnfenceMySQLRuntimeAccountForRecovery(runCtx, resolved, ready.Request.Maintenance, r.Secrets); err != nil {
		return errors.Join(ErrMySQLRestoreTargetUnlockIndeterminate, err)
	}
	if err := supervisor.Failure(); err != nil {
		return errors.Join(ErrMySQLRestoreTargetUnlockIndeterminate, err)
	}
	if err := r.Control.ProveClaimedMySQLRestoreTargetUnlocked(runCtx, r.Acceptance, claim, r.Observer, r.Secrets); err != nil {
		return errors.Join(ErrMySQLRestoreTargetUnlockIndeterminate, err)
	}
	return nil
}

// RunClaimedFenceRelease resumes an already proved target unlock under the
// original claim. It renews ownership throughout the fresh observations and
// atomic terminal receipt; no external account mutation is repeated.
func (r MySQLRestoreRecoveryRunner) RunClaimedFenceRelease(ctx context.Context, claim OperationClaim) (runErr error) {
	if r.Control == nil || r.Acceptance == nil || r.Observer == nil || r.Secrets == nil {
		return ErrMySQLRestoreFence
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
		return err
	}
	terminal := false
	defer func() {
		if err := supervisor.Stop(); err != nil && !terminal && runErr == nil {
			runErr = err
		}
	}()
	if err := supervisor.Failure(); err != nil {
		return err
	}
	if err := r.Control.ReleaseClaimedMySQLRestoreRuntimeFence(supervisor.Context(), r.Acceptance, claim, r.Observer, r.Secrets); err != nil {
		return err
	}
	terminal = true
	return nil
}

func (db *DB) resolvedMySQLRecoveryTarget(ctx context.Context, ready MySQLRestoreRecoveryReadiness) (database.ResolvedBinding, error) {
	catalog, err := db.DatabaseCatalogRevision(ctx, ready.Request.CatalogRevision)
	if err != nil {
		return database.ResolvedBinding{}, err
	}
	resolver, err := database.NewResolver(catalog.Catalog)
	if err != nil {
		return database.ResolvedBinding{}, err
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: ready.Request.ProfileID,
		Purpose: database.PurposeApplication, LogicalResourceID: ready.Request.LogicalID, Expected: &ready.Request.Target})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != ready.Request.Maintenance {
		return database.ResolvedBinding{}, ErrMySQLRestoreFence
	}
	return resolved, nil
}

// ProveClaimedMySQLRestoreTargetUnlocked records an independently observed
// unlocked account, session absence, and unchanged target. It never repeats
// ALTER USER. Only the original live claim can advance the one-way intent.
func (db *DB) ProveClaimedMySQLRestoreTargetUnlocked(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, observer MySQLSourceStoppedObserver, secrets database.SecretSource) error {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || observer == nil || secrets == nil || validateOperationClaim(claim) != nil {
		return ErrMySQLRestoreFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	var signed MySQLRestoreRecoveryRequest
	if err != nil || accepted.Operation.Kind != MySQLRestoreRecoveryOperationKind || accepted.Operation.Status != model.OperationRunning ||
		decodeMySQLRestoreRecoveryPayload(accepted.Operation.Payload, &signed) != nil {
		return ErrMySQLRestoreFence
	}
	ready, err := db.AssessCompletedMySQLRestoreLiveSource(ctx, acceptance, signed.RestoreOperationID, observer, secrets)
	if err != nil || ready.Fence.Epoch != signed.RuntimeFenceEpoch || ready.Fence.Owner != signed.RuntimeFenceOwner ||
		ready.Request.CatalogRevision != signed.CatalogRevision || ready.Request.Target != signed.Target {
		return ErrMySQLRestoreFence
	}
	resolved, err := db.resolvedMySQLRecoveryTarget(ctx, ready)
	if err != nil {
		return err
	}
	if err := database.InspectMySQLRuntimeAccountUnlockedForRecovery(ctx, resolved, ready.Request.Maintenance, secrets); err != nil {
		return err
	}
	restore, err := database.MySQLRestoreBinding(resolved)
	if err != nil {
		return err
	}
	if err := database.VerifyMySQLRestoreTarget(ctx, restore, secrets, ready.Request.Artifact.Expectation); err != nil {
		return err
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return err
	}
	var held bool
	if err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND status='running'
		AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp() FOR UPDATE`,
		claim.OperationID(), MySQLRestoreRecoveryOperationKind, claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return ownershipLost(claim)
	}
	var state, intentID, restoreID, claimOwner, owner, liveOwner string
	var revision, epoch, generation, liveEpoch int64
	var liveActive bool
	err = tx.QueryRow(ctx, `SELECT r.state,r.acceptance_intent_id,r.restore_operation_id,r.catalog_revision,
		r.runtime_fence_epoch,r.runtime_fence_owner,r.claim_owner,r.claim_generation,
		f.active,f.epoch,f.owner
		FROM mysql_restore_recovery_intents r CROSS JOIN runtime_mutation_fence f
		WHERE r.operation_id=$1 AND f.singleton=true AND r.target_unlock_intended_at IS NOT NULL
		FOR UPDATE OF r,f`, claim.OperationID()).Scan(
		&state, &intentID, &restoreID, &revision, &epoch, &owner, &claimOwner, &generation,
		&liveActive, &liveEpoch, &liveOwner)
	if err != nil || state != "target-unlock-intended" || intentID != accepted.AcceptanceIntentID ||
		restoreID != signed.RestoreOperationID || revision != signed.CatalogRevision || epoch != signed.RuntimeFenceEpoch ||
		owner != signed.RuntimeFenceOwner || claimOwner != claim.OwnerID() || generation != claim.Generation() ||
		!liveActive || liveEpoch != epoch || liveOwner != owner {
		return ErrMySQLRestoreFence
	}
	updated, err := tx.Exec(ctx, `UPDATE mysql_restore_recovery_intents
		SET state='target-unlock-proved',target_unlock_proved_at=clock_timestamp()
		WHERE operation_id=$1 AND state='target-unlock-intended' AND target_unlock_proved_at IS NULL`, claim.OperationID())
	if err != nil || updated.RowsAffected() != 1 {
		return ErrMySQLRestoreFence
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("MySQL target unlock proof could not commit: %w", err)
	}
	return nil
}
