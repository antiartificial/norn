package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
)

// ErrMySQLRuntimeLaunchFence means a runtime launch cannot be proved separate
// from a signed MySQL restore. Callers must reconcile the durable record before
// retrying; this primitive deliberately has no expiry-based release.
var ErrMySQLRuntimeLaunchFence = errors.New("MySQL runtime launch reservation rejected")

// MySQLRuntimeLaunchReservation is a durable, bounded launch admission record.
// Identities must include every MySQL identity from the signed request that the
// runtime could write. For a signed restore that means both Artifact.Source and
// Target. This store primitive cannot fence an identity that its caller omits.
type MySQLRuntimeLaunchReservation struct {
	ID         string
	Identities []database.TargetIdentity
	State      string
	Replayed   bool
}

// MySQLRuntimeLaunchStopProof is the durable evidence required before a
// launched runtime stops blocking a restore. It is intentionally supplied by
// a supervisor/reconciler, never inferred from elapsed time.
type MySQLRuntimeLaunchStopProof struct {
	RuntimeInstanceID string    `json:"runtimeInstanceId"`
	ObservedAt        time.Time `json:"observedAt"`
	Method            string    `json:"method"`
	EvidenceSHA256    string    `json:"evidenceSha256"`
}

// MySQLRuntimeLaunchNoStartProof is evidence from the runtime supervisor that
// a reserved launch never obtained an instance. It is required to release a
// reservation; elapsed time and a caller assertion are never sufficient.
type MySQLRuntimeLaunchNoStartProof struct {
	ObservedAt     time.Time `json:"observedAt"`
	Method         string    `json:"method"`
	EvidenceSHA256 string    `json:"evidenceSha256"`
}

// MySQLRuntimeLaunchReconciliationProof is independent supervisor evidence for
// resolving an ambiguous launch. Outcome is "stopped" when the recorded
// runtime instance was observed absent, or "never-started" when no instance
// was ever recorded. ObservationSource and ObservationExecutionID identify the
// supervisor observation rather than the original caller.
type MySQLRuntimeLaunchReconciliationProof struct {
	Outcome                string    `json:"outcome"`
	RuntimeInstanceID      string    `json:"runtimeInstanceId"`
	ObservationSource      string    `json:"observationSource"`
	ObservationExecutionID string    `json:"observationExecutionId"`
	ObservedAt             time.Time `json:"observedAt"`
	EvidenceSHA256         string    `json:"evidenceSha256"`
}

// ReserveMySQLRuntimeLaunch transactionally reserves the exact MySQL
// identities before a runtime is started. It uses the same catalog transaction
// lock as restore maintenance-fence acquisition, so either ordering observes
// the other durable gate. Reservations remain blocking after a caller loses
// certainty about launch; only ReleaseMySQLRuntimeLaunchNeverStarted reopens a
// gate, and only from the pre-launch state. This is a private store primitive:
// it does not itself bind reservationID to a signed claimed operation. Its
// caller must retain that mapping and invoke it only from the signed executor.
// Its one-active-row-per-physical-database model admits only cold starts; it is
// not wired into rolling deploy or overlap-capable runtime paths.
func (db *DB) ReserveMySQLRuntimeLaunch(ctx context.Context, reservationID string, identities []database.TargetIdentity) (MySQLRuntimeLaunchReservation, error) {
	if db == nil || db.Pool == nil || strings.TrimSpace(reservationID) == "" {
		return MySQLRuntimeLaunchReservation{}, ErrMySQLRuntimeLaunchFence
	}
	identities, err := canonicalMySQLLaunchIdentities(identities)
	if err != nil {
		return MySQLRuntimeLaunchReservation{}, ErrMySQLRuntimeLaunchFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MySQLRuntimeLaunchReservation{}, err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return MySQLRuntimeLaunchReservation{}, err
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil {
		return MySQLRuntimeLaunchReservation{}, err
	}
	if err := rejectMySQLRestoreMaintenanceFence(ctx, tx, identities); err != nil {
		return MySQLRuntimeLaunchReservation{}, err
	}
	rows, err := tx.Query(ctx, `SELECT target, state FROM mysql_runtime_launch_reservations WHERE reservation_id=$1 FOR UPDATE`, reservationID)
	if err != nil {
		return MySQLRuntimeLaunchReservation{}, err
	}
	var existing []database.TargetIdentity
	for rows.Next() {
		var encoded []byte
		var state string
		if err := rows.Scan(&encoded, &state); err != nil || state != "reserved" {
			rows.Close()
			return MySQLRuntimeLaunchReservation{}, ErrMySQLRuntimeLaunchFence
		}
		var identity database.TargetIdentity
		if json.Unmarshal(encoded, &identity) != nil {
			rows.Close()
			return MySQLRuntimeLaunchReservation{}, ErrMySQLRuntimeLaunchFence
		}
		existing = append(existing, identity)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return MySQLRuntimeLaunchReservation{}, err
	}
	rows.Close()
	if len(existing) > 0 {
		existing, err = canonicalMySQLLaunchIdentities(existing)
		if err != nil || !sameMySQLLaunchIdentities(existing, identities) {
			return MySQLRuntimeLaunchReservation{}, ErrMySQLRuntimeLaunchFence
		}
		if err := tx.Commit(ctx); err != nil {
			return MySQLRuntimeLaunchReservation{}, err
		}
		return MySQLRuntimeLaunchReservation{ID: reservationID, Identities: identities, State: "reserved", Replayed: true}, nil
	}
	for _, identity := range identities {
		encoded, _ := json.Marshal(identity)
		result, err := tx.Exec(ctx, `INSERT INTO mysql_runtime_launch_reservations
			(reservation_id, target_key, target, state) VALUES ($1,$2,$3,'reserved')
			ON CONFLICT (target_key) WHERE state IN ('reserved','launched','needs-inspection') DO NOTHING`, reservationID, mysqlRuntimePhysicalKeyForCatalog(active.Catalog, identity), encoded)
		if err != nil {
			return MySQLRuntimeLaunchReservation{}, err
		}
		if result.RowsAffected() != 1 {
			return MySQLRuntimeLaunchReservation{}, ErrMySQLRuntimeLaunchFence
		}
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM mysql_runtime_launch_reservations WHERE reservation_id=$1 AND state='reserved'`, reservationID).Scan(&count); err != nil || count != len(identities) {
		return MySQLRuntimeLaunchReservation{}, ErrMySQLRuntimeLaunchFence
	}
	if err := tx.Commit(ctx); err != nil {
		return MySQLRuntimeLaunchReservation{}, err
	}
	return MySQLRuntimeLaunchReservation{ID: reservationID, Identities: identities, State: "reserved"}, nil
}

// MarkMySQLRuntimeLaunchLaunched crosses the one-way boundary immediately
// after the runtime supervisor returns its instance identity. A crash before
// this receipt is resolved is still an unresolved reservation and blocks
// restore.
func (db *DB) MarkMySQLRuntimeLaunchLaunched(ctx context.Context, reservationID, runtimeInstanceID string) error {
	if strings.TrimSpace(runtimeInstanceID) == "" {
		return ErrMySQLRuntimeLaunchFence
	}
	return db.transitionMySQLRuntimeLaunchWithProof(ctx, reservationID, "reserved", "launched", nil, nil, runtimeInstanceID)
}

// ContainMySQLRuntimeLaunchForInspection records an ambiguous runtime launch.
func (db *DB) ContainMySQLRuntimeLaunchForInspection(ctx context.Context, reservationID string) error {
	return db.containMySQLRuntimeLaunch(ctx, reservationID)
}

// StopMySQLRuntimeLaunch records proof that a previously launched runtime is
// no longer able to write. A restore may proceed only after this receipt is
// durable; ambiguous launches remain needs-inspection.
func (db *DB) StopMySQLRuntimeLaunch(ctx context.Context, reservationID string, proof MySQLRuntimeLaunchStopProof) error {
	if db == nil || db.Pool == nil || strings.TrimSpace(reservationID) == "" || !validMySQLRuntimeLaunchStopProof(proof) {
		return ErrMySQLRuntimeLaunchFence
	}
	encoded, _ := json.Marshal(proof)
	return db.transitionMySQLRuntimeLaunchWithProof(ctx, reservationID, "launched", "stopped", encoded, nil, proof.RuntimeInstanceID)
}

// ReleaseMySQLRuntimeLaunchNeverStarted releases only a durable reservation
// that has never been recorded as launched and has a retained no-start proof.
// It never performs timeout cleanup.
func (db *DB) ReleaseMySQLRuntimeLaunchNeverStarted(ctx context.Context, reservationID string, proof MySQLRuntimeLaunchNoStartProof) error {
	if db == nil || db.Pool == nil || strings.TrimSpace(reservationID) == "" || !validMySQLRuntimeLaunchNoStartProof(proof) {
		return ErrMySQLRuntimeLaunchFence
	}
	encoded, _ := json.Marshal(proof)
	return db.transitionMySQLRuntimeLaunchWithProof(ctx, reservationID, "reserved", "released", nil, encoded, "")
}

// ReconcileMySQLRuntimeLaunch is the only evidence-bound exit from
// needs-inspection. It never uses elapsed time. A recorded runtime identity
// requires a stopped outcome with that exact identity; an identity-less
// reservation can only be resolved as never-started.
func (db *DB) ReconcileMySQLRuntimeLaunch(ctx context.Context, reservationID string, proof MySQLRuntimeLaunchReconciliationProof) error {
	if db == nil || db.Pool == nil || strings.TrimSpace(reservationID) == "" || !validMySQLRuntimeLaunchReconciliationProof(proof) {
		return ErrMySQLRuntimeLaunchFence
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, `SELECT runtime_instance_id FROM mysql_runtime_launch_reservations WHERE reservation_id=$1 AND state='needs-inspection' FOR UPDATE`, reservationID)
	if err != nil {
		return err
	}
	total := 0
	runtimeID := ""
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if total == 0 {
			runtimeID = id
		} else if runtimeID != id {
			rows.Close()
			return ErrMySQLRuntimeLaunchFence
		}
		total++
	}
	if err := rows.Err(); err != nil || total == 0 {
		rows.Close()
		return ErrMySQLRuntimeLaunchFence
	}
	rows.Close()
	encoded, _ := json.Marshal(proof)
	state, stopProof, resolutionProof := "", []byte(nil), []byte(nil)
	switch {
	case runtimeID != "" && proof.Outcome == "stopped" && proof.RuntimeInstanceID == runtimeID:
		state, stopProof = "stopped", encoded
	case runtimeID == "" && proof.Outcome == "never-started" && proof.RuntimeInstanceID == "":
		state, resolutionProof = "released", encoded
	default:
		return ErrMySQLRuntimeLaunchFence
	}
	result, err := tx.Exec(ctx, `UPDATE mysql_runtime_launch_reservations SET state=$1, stop_proof=$2, resolution_proof=$3, updated_at=clock_timestamp()
		WHERE reservation_id=$4 AND state='needs-inspection'`, state, stopProof, resolutionProof, reservationID)
	if err != nil || result.RowsAffected() != int64(total) {
		return ErrMySQLRuntimeLaunchFence
	}
	return tx.Commit(ctx)
}

func (db *DB) transitionMySQLRuntimeLaunch(ctx context.Context, reservationID, from, to string) error {
	return db.transitionMySQLRuntimeLaunchWithProof(ctx, reservationID, from, to, nil, nil, "")
}

func (db *DB) transitionMySQLRuntimeLaunchWithProof(ctx context.Context, reservationID, from, to string, stopProof, resolutionProof []byte, runtimeInstanceID string) error {
	if db == nil || db.Pool == nil || strings.TrimSpace(reservationID) == "" {
		return ErrMySQLRuntimeLaunchFence
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, `SELECT state FROM mysql_runtime_launch_reservations WHERE reservation_id=$1 FOR UPDATE`, reservationID)
	if err != nil {
		return err
	}
	total := 0
	for rows.Next() {
		total++
	}
	if err := rows.Err(); err != nil || total == 0 {
		rows.Close()
		return ErrMySQLRuntimeLaunchFence
	}
	rows.Close()
	result, err := tx.Exec(ctx, `UPDATE mysql_runtime_launch_reservations SET state=$1, stop_proof=$2, resolution_proof=$3,
		runtime_instance_id=CASE WHEN $6<>'' THEN $6 ELSE runtime_instance_id END, updated_at=clock_timestamp()
		WHERE reservation_id=$4 AND state=$5 AND ($5 <> 'launched' OR runtime_instance_id=$6)`, to, stopProof, resolutionProof, reservationID, from, runtimeInstanceID)
	if err != nil || result.RowsAffected() != int64(total) {
		return ErrMySQLRuntimeLaunchFence
	}
	return tx.Commit(ctx)
}

func (db *DB) containMySQLRuntimeLaunch(ctx context.Context, reservationID string) error {
	if db == nil || db.Pool == nil || strings.TrimSpace(reservationID) == "" {
		return ErrMySQLRuntimeLaunchFence
	}
	result, err := db.Pool.Exec(ctx, `UPDATE mysql_runtime_launch_reservations SET state='needs-inspection', updated_at=clock_timestamp()
		WHERE reservation_id=$1 AND state IN ('reserved','launched')`, reservationID)
	if err != nil || result.RowsAffected() == 0 {
		return ErrMySQLRuntimeLaunchFence
	}
	return nil
}

func canonicalMySQLLaunchIdentities(identities []database.TargetIdentity) ([]database.TargetIdentity, error) {
	if len(identities) == 0 {
		return nil, fmt.Errorf("missing MySQL target identity")
	}
	byKey := make(map[string]database.TargetIdentity, len(identities))
	for _, identity := range identities {
		if identity.Engine != database.EngineMySQL || strings.TrimSpace(identity.ServiceID) == "" || identity.ServiceGeneration <= 0 || strings.TrimSpace(identity.BindingID) == "" || identity.BindingGeneration <= 0 || strings.TrimSpace(identity.Database) == "" || strings.TrimSpace(identity.Role) == "" {
			return nil, fmt.Errorf("incomplete MySQL target identity")
		}
		key := mysqlRuntimePhysicalKey(identity)
		if existing, ok := byKey[key]; ok && existing != identity {
			return nil, fmt.Errorf("multiple signed identities alias one physical MySQL database")
		}
		byKey[key] = identity
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]database.TargetIdentity, 0, len(keys))
	for _, key := range keys {
		result = append(result, byKey[key])
	}
	return result, nil
}

func sameMySQLLaunchIdentities(left, right []database.TargetIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func mysqlRuntimePhysicalKey(identity database.TargetIdentity) string {
	// Roles and bindings are credentials/routing names. They can alias the same
	// physical MySQL database, so exclusion must be at service generation plus
	// database scope while the full identity remains in the durable receipt.
	physical := struct {
		Engine            database.Engine `json:"engine"`
		ServiceID         string          `json:"serviceId"`
		ServiceGeneration uint64          `json:"serviceGeneration"`
		Database          string          `json:"database"`
	}{identity.Engine, identity.ServiceID, identity.ServiceGeneration, identity.Database}
	encoded, _ := json.Marshal(physical)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// mysqlRuntimePhysicalKeyForCatalog binds exclusion to the catalog's physical
// provider identity. Service and binding generations describe immutable signed
// routing revisions, but a generation rotation can continue to address the
// same provider database. Multiple catalog services can also intentionally
// alias one provider. ProviderRef is the stable physical identity in both
// cases. The legacy identity key remains a fail-closed fallback for synthetic
// store callers whose service is not present in the active catalog.
func mysqlRuntimePhysicalKeyForCatalog(catalog database.Catalog, identity database.TargetIdentity) string {
	for _, service := range catalog.Services {
		if service.ID != identity.ServiceID || service.Engine != identity.Engine {
			continue
		}
		physical := struct {
			Engine      database.Engine `json:"engine"`
			ProviderRef string          `json:"providerRef"`
			Database    string          `json:"database"`
		}{identity.Engine, service.ProviderRef, identity.Database}
		encoded, _ := json.Marshal(physical)
		digest := sha256.Sum256(encoded)
		return hex.EncodeToString(digest[:])
	}
	return mysqlRuntimePhysicalKey(identity)
}

func validMySQLRuntimeLaunchStopProof(proof MySQLRuntimeLaunchStopProof) bool {
	if strings.TrimSpace(proof.RuntimeInstanceID) == "" || proof.ObservedAt.IsZero() || strings.TrimSpace(proof.Method) == "" || len(proof.Method) > 200 || len(proof.EvidenceSHA256) != 64 || strings.ToLower(proof.EvidenceSHA256) != proof.EvidenceSHA256 {
		return false
	}
	_, err := hex.DecodeString(proof.EvidenceSHA256)
	return err == nil
}

func validMySQLRuntimeLaunchNoStartProof(proof MySQLRuntimeLaunchNoStartProof) bool {
	if proof.ObservedAt.IsZero() || strings.TrimSpace(proof.Method) == "" || len(proof.Method) > 200 || len(proof.EvidenceSHA256) != 64 || strings.ToLower(proof.EvidenceSHA256) != proof.EvidenceSHA256 {
		return false
	}
	_, err := hex.DecodeString(proof.EvidenceSHA256)
	return err == nil
}

func validMySQLRuntimeLaunchReconciliationProof(proof MySQLRuntimeLaunchReconciliationProof) bool {
	if (proof.Outcome != "stopped" && proof.Outcome != "never-started") || strings.TrimSpace(proof.ObservationSource) == "" || strings.TrimSpace(proof.ObservationExecutionID) == "" || proof.ObservedAt.IsZero() || len(proof.EvidenceSHA256) != 64 || strings.ToLower(proof.EvidenceSHA256) != proof.EvidenceSHA256 {
		return false
	}
	_, err := hex.DecodeString(proof.EvidenceSHA256)
	return err == nil
}

func lockMySQLCatalogGate(ctx context.Context, tx pgx.Tx) error {
	// lock_timeout bounds transaction-level advisory-lock acquisition under a
	// stalled catalog or restore transaction. The caller context provides any
	// tighter end-to-end bound.
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('norn:database-catalog', 0))`)
	return err
}

func rejectMySQLRestoreMaintenanceFence(ctx context.Context, tx pgx.Tx, identities []database.TargetIdentity, exceptOperationID ...string) error {
	except := ""
	if len(exceptOperationID) == 1 {
		except = exceptOperationID[0]
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT i.target, f.source_quiescence->'source'
		FROM mysql_restore_maintenance_fences f JOIN mysql_restore_intents i ON i.operation_id=f.operation_id`)
	if err != nil {
		return err
	}
	var restoreIdentities []database.TargetIdentity
	for rows.Next() {
		var targetJSON, sourceJSON []byte
		if err := rows.Scan(&targetJSON, &sourceJSON); err != nil {
			rows.Close()
			return err
		}
		var target, source database.TargetIdentity
		if json.Unmarshal(targetJSON, &target) != nil || json.Unmarshal(sourceJSON, &source) != nil {
			rows.Close()
			return ErrMySQLRuntimeLaunchFence
		}
		restoreIdentities = append(restoreIdentities, target, source)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, identity := range identities {
		physicalKey := mysqlRuntimePhysicalKeyForCatalog(active.Catalog, identity)
		var snapshotBlocked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mysql_source_snapshot_intents WHERE source_key=$1 AND operation_id<>$2)`, physicalKey, except).Scan(&snapshotBlocked); err != nil {
			return err
		}
		if snapshotBlocked {
			return ErrMySQLRuntimeLaunchFence
		}
		for _, restoreIdentity := range restoreIdentities {
			if mysqlRuntimePhysicalKeyForCatalog(active.Catalog, restoreIdentity) == physicalKey {
				return ErrMySQLRuntimeLaunchFence
			}
		}
	}
	return nil
}

func rejectMySQLRuntimeLaunchReservations(ctx context.Context, tx pgx.Tx, identities []database.TargetIdentity) error {
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil {
		return err
	}
	for _, identity := range identities {
		var blocked bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mysql_runtime_launch_reservations
		WHERE target_key=$1 AND state IN ('reserved','launched','needs-inspection'))`, mysqlRuntimePhysicalKeyForCatalog(active.Catalog, identity)).Scan(&blocked)
		if err != nil {
			return err
		}
		if blocked {
			return ErrMySQLRestoreFence
		}
	}
	return nil
}
