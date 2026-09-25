package store

import (
	"context"
	"fmt"

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
}

func (r MySQLRestoreRunner) RunClaimed(ctx context.Context, claim OperationClaim) error {
	if r.Control == nil || r.Acceptance == nil || r.Secrets == nil {
		return ErrMySQLRestoreFence
	}
	intent, err := r.Control.BeginClaimedMySQLRestore(ctx, r.Acceptance, claim, r.Secrets)
	if err != nil {
		return err
	}
	// Once Begin committed, every outcome is terminal and inspection-safe. If
	// this worker dies before it can call Finish, the retained executing row is
	// itself the fail-closed crash record and cannot be claimed for replay.
	catalog, err := r.Control.DatabaseCatalogRevision(ctx, intent.Request.CatalogRevision)
	if err == nil {
		resolver, resolveErr := database.NewResolver(catalog.Catalog)
		if resolveErr != nil {
			err = resolveErr
		} else {
			resolved, resolveErr := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: intent.Request.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: intent.Request.LogicalID, Expected: &intent.Request.Target})
			if resolveErr != nil {
				err = resolveErr
			} else {
				err = database.RestoreMySQLSQLArtifact(ctx, resolved, r.Secrets, intent.Request.ArtifactPath, intent.Request.Artifact, r.Tool)
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
	if err := r.Control.FinishClaimedMySQLRestore(ctx, r.Acceptance, claim, true, "MySQL restore completed"); err != nil {
		// The SQL stream may have completed even though the terminal receipt was
		// lost. Do not retry it; leaving executing is the required crash fence.
		return fmt.Errorf("MySQL restore completion receipt is uncertain: %w", err)
	}
	return nil
}
