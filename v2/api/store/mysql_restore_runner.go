package store

import (
	"context"
	"fmt"
	"time"

	"norn/v2/api/database"
)

// MySQLRestoreRunner is a private worker primitive. No HTTP route or public
// capability calls it; the caller must supply a live claimed operation that
// was already accepted and prepared through the signed durable-intent path.
type MySQLRestoreRunner struct {
	Control    *DB
	Acceptance *PGOperationStore
	Secrets    database.SecretSource
	Tool       database.MySQLRestoreTool
	ClaimLease time.Duration
}

func (r MySQLRestoreRunner) RunClaimed(ctx context.Context, claim OperationClaim) (runErr error) {
	if r.Control == nil || r.Acceptance == nil || r.Secrets == nil {
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
			runErr = fmt.Errorf("MySQL restore claim renewal failed: %w", err)
		}
	}()
	runCtx := supervisor.Context()
	intent, err := r.Control.BeginClaimedMySQLRestore(runCtx, r.Acceptance, claim, r.Secrets)
	if err != nil {
		return err
	}
	// Once Begin committed, every outcome is terminal and inspection-safe. If
	// this worker dies before it can call Finish, the retained executing row is
	// itself the fail-closed crash record and cannot be claimed for replay.
	catalog, err := r.Control.DatabaseCatalogRevision(runCtx, intent.Request.CatalogRevision)
	if err == nil {
		resolver, resolveErr := database.NewResolver(catalog.Catalog)
		if resolveErr != nil {
			err = resolveErr
		} else {
			resolved, resolveErr := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: intent.Request.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: intent.Request.LogicalID, Expected: &intent.Request.Target})
			if resolveErr != nil {
				err = resolveErr
			} else {
				err = database.RestoreMySQLSQLArtifact(runCtx, resolved, r.Secrets, intent.Request.ArtifactPath, intent.Request.Artifact, r.Tool)
				if err == nil {
					err = database.VerifyMySQLRestoreTarget(runCtx, resolved, r.Secrets, intent.Request.Artifact.Expectation)
				}
			}
		}
	}
	if err != nil {
		finishErr := r.Control.FinishClaimedMySQLRestore(context.Background(), r.Acceptance, claim, false, "MySQL restore requires inspection")
		if finishErr != nil {
			return fmt.Errorf("MySQL restore failed (%v) and could not record inspection state: %w", err, finishErr)
		}
		return err
	}
	if err := supervisor.Failure(); err != nil {
		return fmt.Errorf("MySQL restore claim renewal failed before completion: %w", err)
	}
	if err := runCtx.Err(); err != nil {
		return fmt.Errorf("MySQL restore context ended before completion: %w", err)
	}
	if err := r.Control.FinishClaimedMySQLRestore(runCtx, r.Acceptance, claim, true, "MySQL restore completed"); err != nil {
		// The SQL stream may have completed even though the terminal receipt was
		// lost. Do not retry it; leaving executing is the required crash fence.
		return fmt.Errorf("MySQL restore completion receipt is uncertain: %w", err)
	}
	terminal = true
	return nil
}
