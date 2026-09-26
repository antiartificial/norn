package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
)

var (
	ErrDatabaseCatalogRevisionConflict = errors.New("database catalog revision changed")
	ErrDatabaseCatalogRetiredIdentity  = errors.New("database catalog reuses a durably retired identity")
)

// DatabaseCatalogRevision is one activated, digest-verified catalog.
type DatabaseCatalogRevision struct {
	Revision int64
	Digest   string
	Catalog  database.Catalog
}

// ActivateDatabaseCatalog appends revision expectedCurrent+1. It validates
// the catalog, the transition from the current revision, and the permanent
// retirement history; records every retirement in the same transaction; and
// fails with ErrDatabaseCatalogRevisionConflict if another activation won.
func (db *DB) ActivateDatabaseCatalog(ctx context.Context, expectedCurrent int64, next database.Catalog, actor string) (DatabaseCatalogRevision, error) {
	return db.activateDatabaseCatalog(ctx, expectedCurrent, next, actor, nil, nil)
}

// ActivateDatabaseCatalogClaimed is the operation executor's activation. In
// one transaction, after the catalog lock is held, it verifies the claim is
// current against the wall clock (owner, generation, unexpired lease; the
// row is locked, so the lease cannot be renewed or reclaimed meanwhile),
// applies the compare-and-set, and finishes the operation as succeeded with
// metadata plus the new revision. Activation and its operation receipt
// therefore commit together: there is no window in which routing changed but
// the operation's outcome was lost, and an executor without the live claim
// can never change routing or claim another writer's revision.
func (db *DB) ActivateDatabaseCatalogClaimed(ctx context.Context, claim OperationClaim, expectedCurrent int64, next database.Catalog, actor string, metadata map[string]interface{}) (DatabaseCatalogRevision, error) {
	if err := validateOperationClaim(claim); err != nil {
		return DatabaseCatalogRevision{}, err
	}
	return db.activateDatabaseCatalog(ctx, expectedCurrent, next, actor, func(tx pgx.Tx) error {
		var held bool
		err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id = $1 AND status = 'running' AND locked_by = $2
			AND lock_generation = $3 AND locked_until > clock_timestamp() FOR UPDATE`, claim.OperationID(), claim.OwnerID(), claim.Generation()).Scan(&held)
		if errors.Is(err, pgx.ErrNoRows) {
			return ownershipLost(claim)
		}
		return err
	}, func(tx pgx.Tx, activated DatabaseCatalogRevision) error {
		finished := map[string]interface{}{}
		for key, value := range metadata {
			finished[key] = value
		}
		finished["revision"], finished["storedDigest"] = activated.Revision, activated.Digest
		data, err := json.Marshal(finished)
		if err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `UPDATE operations
			SET status = 'succeeded', message = $1, metadata = metadata || $2::jsonb,
			    locked_by = '', locked_until = NULL, updated_at = now(), finished_at = now()
			WHERE id = $3 AND status = 'running' AND locked_by = $4 AND lock_generation = $5 AND locked_until > clock_timestamp()`,
			fmt.Sprintf("database catalog revision %d active", activated.Revision), data, claim.OperationID(), claim.OwnerID(), claim.Generation())
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ownershipLost(claim)
		}
		return nil
	})
}

func (db *DB) activateDatabaseCatalog(ctx context.Context, expectedCurrent int64, next database.Catalog, actor string, fence func(pgx.Tx) error, complete func(pgx.Tx, DatabaseCatalogRevision) error) (DatabaseCatalogRevision, error) {
	if strings.TrimSpace(actor) == "" || expectedCurrent < 0 {
		return DatabaseCatalogRevision{}, fmt.Errorf("database catalog activation requires an actor and expected revision")
	}
	if err := database.ValidateCatalog(next); err != nil {
		return DatabaseCatalogRevision{}, err
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return DatabaseCatalogRevision{}, err
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DatabaseCatalogRevision{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('norn:database-catalog', 0))`); err != nil {
		return DatabaseCatalogRevision{}, err
	}
	if fence != nil {
		if err := fence(tx); err != nil {
			return DatabaseCatalogRevision{}, err
		}
	}
	// Unfinished maintenance and source quiescence block catalog changes. A
	// completed signed recovery retains both rows for audit, but may permit
	// unrelated catalog changes if its source and target bindings stay exact.
	var maintenanceActive bool
	if err := tx.QueryRow(ctx, `WITH valid_release AS (
		SELECT m.operation_id,m.source_artifact_operation_id
		FROM mysql_restore_maintenance_fences m
		JOIN mysql_restore_recovery_intents r ON r.operation_id=m.recovery_operation_id
			AND r.restore_operation_id=m.operation_id AND r.state='runtime-released'
		JOIN operations o ON o.id=r.operation_id AND o.status='succeeded'
		WHERE m.recovery_released_at IS NOT NULL
		UNION
		SELECT m.operation_id,m.source_artifact_operation_id
		FROM mysql_restore_maintenance_fences m
		JOIN operations o ON o.id=m.recovery_operation_id AND o.status='succeeded'
			AND o.kind='database.mysql-restore-recovery' AND o.source='private-mysql-restore-reconciliation'
			AND o.metadata->>'mysqlRestoreRecoveryState'='reconciled-runtime-released'
		JOIN mysql_restore_recovery_intents r ON r.operation_id=o.metadata->>'priorRecoveryOperationId'
			AND r.restore_operation_id=m.operation_id AND r.state IN ('target-unlock-intended','target-unlock-proved')
		JOIN operations p ON p.id=r.operation_id AND p.status='failed'
			AND p.metadata->>'reconciledByOperationId'=o.id
		WHERE m.recovery_released_at IS NOT NULL
	)
	SELECT EXISTS (SELECT 1 FROM mysql_restore_maintenance_fences m WHERE NOT EXISTS (
		SELECT 1 FROM valid_release v WHERE v.operation_id=m.operation_id
	)) OR EXISTS (SELECT 1 FROM mysql_source_snapshot_intents s WHERE NOT EXISTS (
		SELECT 1 FROM valid_release v WHERE v.source_artifact_operation_id=s.operation_id
	))`).Scan(&maintenanceActive); err != nil {
		return DatabaseCatalogRevision{}, err
	}
	if maintenanceActive {
		return DatabaseCatalogRevision{}, ErrMySQLRestoreMaintenanceFence
	}
	current, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DatabaseCatalogRevision{}, err
	}
	currentRevision := int64(0)
	if err == nil {
		currentRevision = current.Revision
	}
	if currentRevision != expectedCurrent {
		return DatabaseCatalogRevision{}, ErrDatabaseCatalogRevisionConflict
	}
	if currentRevision > 0 {
		if err := database.ValidateTransition(current.Catalog, next); err != nil {
			return DatabaseCatalogRevision{}, err
		}
		if err := rejectMySQLRuntimeLaunchCatalogRetarget(ctx, tx, current.Catalog, next); err != nil {
			return DatabaseCatalogRevision{}, err
		}
		if err := rejectRecoveredMySQLCatalogRetarget(ctx, tx, next); err != nil {
			return DatabaseCatalogRevision{}, err
		}
	}
	if err := checkRetirementHistory(ctx, tx, next); err != nil {
		return DatabaseCatalogRevision{}, err
	}
	revision := currentRevision + 1
	var previous any
	if currentRevision > 0 {
		previous = currentRevision
	}
	digest := checkpointDigest(encoded)
	if _, err := tx.Exec(ctx, `INSERT INTO database_catalog_revisions (revision, previous_revision, catalog, catalog_digest, activated_by) VALUES ($1,$2,$3,$4,$5)`,
		revision, previous, encoded, digest, actor); err != nil {
		return DatabaseCatalogRevision{}, err
	}
	for _, tombstone := range next.Retired {
		if _, err := tx.Exec(ctx, `INSERT INTO database_catalog_retirements (kind, id, retired_revision) VALUES ($1,$2,$3) ON CONFLICT (kind, id) DO NOTHING`,
			string(tombstone.Kind), tombstone.ID, revision); err != nil {
			return DatabaseCatalogRevision{}, err
		}
	}
	activated := DatabaseCatalogRevision{Revision: revision, Digest: digest, Catalog: next}
	if complete != nil {
		if err := complete(tx, activated); err != nil {
			return DatabaseCatalogRevision{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return DatabaseCatalogRevision{}, err
	}
	return activated, nil
}

// Historical source and restore rows remain physical launch reservations
// after recovery. Keep their logical bindings and exact target generations
// resolvable so a catalog edit cannot strand or bypass those reservations.
func rejectRecoveredMySQLCatalogRetarget(ctx context.Context, tx pgx.Tx, next database.Catalog) error {
	rows, err := tx.Query(ctx, `SELECT s.profile_id,s.logical_id,s.source,i.profile_id,i.logical_id,i.target
		FROM mysql_restore_maintenance_fences m
		JOIN mysql_source_snapshot_intents s ON s.operation_id=m.source_artifact_operation_id
		JOIN mysql_restore_intents i ON i.operation_id=m.operation_id
		WHERE m.recovery_released_at IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	resolver, err := database.NewResolver(next)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sourceProfile, sourceLogical, targetProfile, targetLogical string
		var sourceJSON, targetJSON []byte
		if err := rows.Scan(&sourceProfile, &sourceLogical, &sourceJSON, &targetProfile, &targetLogical, &targetJSON); err != nil {
			return err
		}
		var source, target database.TargetIdentity
		if json.Unmarshal(sourceJSON, &source) != nil || json.Unmarshal(targetJSON, &target) != nil {
			return ErrMySQLRestoreMaintenanceFence
		}
		for _, binding := range []struct {
			profile, logical string
			identity         database.TargetIdentity
		}{{sourceProfile, sourceLogical, source}, {targetProfile, targetLogical, target}} {
			resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: binding.profile,
				Purpose: database.PurposeApplication, LogicalResourceID: binding.logical, Expected: &binding.identity})
			if err != nil || resolved.Target != binding.identity {
				return ErrMySQLRestoreMaintenanceFence
			}
		}
	}
	return rows.Err()
}

// checkRetirementHistory enforces the durable history independently of the
// previous catalog document: every recorded tombstone must still be listed
// and no live service, binding or legacy mapping may reuse a retired ID.
func checkRetirementHistory(ctx context.Context, tx pgx.Tx, next database.Catalog) error {
	rows, err := tx.Query(ctx, `SELECT kind, id FROM database_catalog_retirements`)
	if err != nil {
		return err
	}
	defer rows.Close()
	listed := map[database.RetiredResource]bool{}
	for _, tombstone := range next.Retired {
		listed[tombstone] = true
	}
	live := map[database.RetiredResource]bool{}
	for _, service := range next.Services {
		live[database.RetiredResource{Kind: database.RetiredService, ID: service.ID}] = true
	}
	for _, binding := range next.Bindings {
		live[database.RetiredResource{Kind: database.RetiredBinding, ID: binding.ID}] = true
	}
	for _, profile := range next.Profiles {
		if profile.LegacyPostgres != nil {
			live[database.RetiredResource{Kind: database.RetiredBinding, ID: profile.LegacyPostgres.MappingID}] = true
		}
	}
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			return err
		}
		tombstone := database.RetiredResource{Kind: database.RetiredKind(kind), ID: id}
		if live[tombstone] || !listed[tombstone] {
			return ErrDatabaseCatalogRetiredIdentity
		}
	}
	return rows.Err()
}

// ActiveDatabaseCatalog returns the latest revision, or ErrNoRows-wrapped
// (pgx.ErrNoRows) when no catalog has been activated.
func (db *DB) ActiveDatabaseCatalog(ctx context.Context) (DatabaseCatalogRevision, error) {
	return loadActiveDatabaseCatalog(ctx, db.Pool)
}

// DatabaseCatalogRevision returns the immutable catalog accepted by a durable
// operation. Executors use this rather than following a newer active catalog
// after an external-effect boundary has been committed.
func (db *DB) DatabaseCatalogRevision(ctx context.Context, revision int64) (DatabaseCatalogRevision, error) {
	if db == nil || db.Pool == nil || revision <= 0 {
		return DatabaseCatalogRevision{}, ErrDatabaseCatalogRevisionConflict
	}
	var result DatabaseCatalogRevision
	var encoded []byte
	err := db.Pool.QueryRow(ctx, `SELECT revision, catalog, catalog_digest FROM database_catalog_revisions WHERE revision=$1`, revision).
		Scan(&result.Revision, &encoded, &result.Digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return DatabaseCatalogRevision{}, ErrDatabaseCatalogRevisionConflict
	}
	if err != nil || json.Unmarshal(encoded, &result.Catalog) != nil {
		return DatabaseCatalogRevision{}, ErrDatabaseCatalogRevisionConflict
	}
	return result, nil
}

func loadActiveDatabaseCatalog(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (DatabaseCatalogRevision, error) {
	var revision DatabaseCatalogRevision
	var encoded []byte
	err := queryer.QueryRow(ctx, `SELECT revision, catalog, catalog_digest FROM database_catalog_revisions ORDER BY revision DESC LIMIT 1`).
		Scan(&revision.Revision, &encoded, &revision.Digest)
	if err != nil {
		return DatabaseCatalogRevision{}, err
	}
	if checkpointDigest(encoded) != revision.Digest {
		return DatabaseCatalogRevision{}, fmt.Errorf("database catalog revision %d failed integrity verification", revision.Revision)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&revision.Catalog); err != nil {
		return DatabaseCatalogRevision{}, fmt.Errorf("database catalog revision %d is malformed", revision.Revision)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return DatabaseCatalogRevision{}, fmt.Errorf("database catalog revision %d is malformed", revision.Revision)
	}
	if err := database.ValidateCatalog(revision.Catalog); err != nil {
		return DatabaseCatalogRevision{}, fmt.Errorf("database catalog revision %d is invalid: %w", revision.Revision, err)
	}
	return revision, nil
}
