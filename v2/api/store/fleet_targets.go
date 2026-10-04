package store

// Lock order (PG; M7). Every path that touches the fence domain follows this
// order, so it can never deadlock against another path that does too,
// including the legacy heartbeat/advance path that takes no lock at all:
//
//  1. plan advisory lock, where the path already takes one (unchanged by
//     this file);
//  2. fleet_target_registry;
//  3. fleet_authority_epoch;
//  4. fleet_target_fences;
//  5. fleet_github_dispatches;
//  6. fleet_runner_attempts.
//
// Rows 2-3 are FOR SHARE, except in registration (row 2) and epoch advance
// (row 3), which take FOR UPDATE. Row 4 is FOR UPDATE only for acquire,
// bind, release and abandon; every other reader takes FOR SHARE.
//
// Legacy heartbeat and advance (store/fleet_runner_attempts.go) take no lock
// at all today and must not start taking one: each is a single UPDATE, and
// WP8a adds the epoch check as an "AND NOT EXISTS (… fleet_target_fences
// joined to fleet_authority_epoch with authority_epoch <> current …)"
// predicate inside that existing UPDATE. A predicate costs no lock, so it
// cannot invert against a path that locks fences before attempts (such as
// enforceFleetReconciliationAdmission, which must take the fence and epoch
// FOR SHARE immediately after its advisory lock and before the attempt FOR
// UPDATE, i.e. position 4 before position 6 above).
//
// GetFleetTargetFence and FleetAuthorityEpoch below accept the caller's own
// tx so a multi-row admission (fence, then dispatch, then attempt) can hold
// every lock in this one order inside a single transaction (T1).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet/lifecycle"
)

// FleetTarget is the durable identity of one registered target plus its
// immutable aliases.
type FleetTarget struct {
	TargetID                string
	Provider                string
	ProviderAccount         string
	StateBackend            string
	CreatedAt               time.Time
	RegistrationOperationID string
	Aliases                 []string
}

// ErrFleetAuthorityEpochConflict is returned by AdvanceFleetAuthorityEpoch
// when expected no longer matches the stored epoch (a lost CAS race).
var ErrFleetAuthorityEpochConflict = fmt.Errorf("fleet authority epoch changed")

// fleetTargetInFlightSQL reports whether any plan whose cluster or
// environment alias would resolve to the new target already has a
// non-terminal, non-abandoned dispatch (M1): "in flight" means
// fleet_github_dispatches.dispatch_state is submitting, dispatched or
// rerun_submitting, the plan is not in fleet_target_abandoned_plans, and the
// plan's latest runner attempt (if any) is not succeeded.
const fleetTargetInFlightSQL = `
	SELECT EXISTS (
		SELECT 1
		FROM fleet_github_dispatches d
		JOIN operations p ON p.id = d.plan_id
		WHERE d.dispatch_state IN ('submitting', 'dispatched', 'rerun_submitting')
		  AND (p.payload->>'cluster' = ANY($1::text[]) OR d.fleet_environment = ANY($2::text[]))
		  AND NOT EXISTS (SELECT 1 FROM fleet_target_abandoned_plans a WHERE a.plan_id = d.plan_id)
		  AND COALESCE(
		        (SELECT r.status FROM fleet_runner_attempts r WHERE r.plan_id = d.plan_id ORDER BY r.attempt DESC LIMIT 1),
		        'none'
		      ) <> 'succeeded'
	)`

// RegisterFleetTarget opens its own transaction and registers identity with
// aliases, attributed to operationID. It is idempotent: replaying the same
// identity (and any alias already bound to it) returns the existing target
// without bumping the registry generation again. See registerFleetTargetTx
// for the tx-internal core WP9a's signed-operation guard reuses directly so
// registration participates in that larger transaction instead of its own.
func (db *DB) RegisterFleetTarget(ctx context.Context, identity lifecycle.TargetIdentity, aliases []string, operationID string) (*FleetTarget, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	target, err := registerFleetTargetTx(ctx, tx, identity, aliases, operationID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return target, nil
}

// registerFleetTargetTx is the lock-order position-2 core: it takes
// fleet_target_registry FOR UPDATE (the one exception to the FOR SHARE rule,
// since this is registration), runs the M1 in-flight scan and the alias
// conflict checks under that same lock, and only then writes the target,
// its new aliases and the generation bump.
func registerFleetTargetTx(ctx context.Context, tx pgx.Tx, identity lifecycle.TargetIdentity, aliases []string, operationID string, now time.Time) (*FleetTarget, error) {
	canon, targetID, err := lifecycle.CanonicalTarget(identity)
	if err != nil {
		return nil, err
	}
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT generation FROM fleet_target_registry WHERE singleton FOR UPDATE`).Scan(&generation); err != nil {
		return nil, err
	}
	var existing bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fleet_targets WHERE target_id=$1)`, targetID).Scan(&existing); err != nil {
		return nil, err
	}

	clusterNames := make([]string, 0, len(aliases))
	environments := make([]string, 0, len(aliases))
	seen := make(map[string]bool, len(aliases))
	unique := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		kind, value, ok := lifecycle.ParseAlias(alias)
		if !ok {
			return nil, fmt.Errorf("fleet target alias %q is not a recognized kind", alias)
		}
		if seen[alias] {
			continue
		}
		seen[alias] = true
		unique = append(unique, alias)
		switch kind {
		case lifecycle.AliasKindCluster:
			clusterNames = append(clusterNames, value)
		case lifecycle.AliasKindEnvironment:
			environments = append(environments, value)
		}
	}

	// Resolve every alias's current owner before writing anything, so a
	// conflict partway through never leaves a half-registered target.
	newAliases := make([]string, 0, len(unique))
	for _, alias := range unique {
		var owner string
		err := tx.QueryRow(ctx, `SELECT target_id FROM fleet_target_aliases WHERE alias=$1`, alias).Scan(&owner)
		switch {
		case err == pgx.ErrNoRows:
			newAliases = append(newAliases, alias)
		case err != nil:
			return nil, err
		case owner != targetID:
			return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetAliasConflict}
		default:
			// owner == targetID: already bound, immutable, nothing to do.
		}
	}

	if !existing || len(newAliases) > 0 {
		var inFlight bool
		if err := tx.QueryRow(ctx, fleetTargetInFlightSQL, clusterNames, environments).Scan(&inFlight); err != nil {
			return nil, err
		}
		if inFlight {
			return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetRegistrationInFlight}
		}
	}

	if !existing {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fleet_targets (target_id, provider, provider_account, state_backend, created_at, registration_operation_id)
			VALUES ($1,$2,$3,$4,$5,$6)
		`, targetID, canon.Provider, canon.ProviderAccount, canon.StateBackend, now, operationID); err != nil {
			return nil, err
		}
		// The free fence row is created with the target, so every later
		// acquire locks an existing row FOR UPDATE instead of racing an
		// INSERT (two acquirers both reading "no row" would otherwise both
		// decide the fence is free).
		if _, err := tx.Exec(ctx, `INSERT INTO fleet_target_fences (target_id) VALUES ($1)`, targetID); err != nil {
			return nil, err
		}
	}
	for _, alias := range newAliases {
		if _, err := tx.Exec(ctx, `INSERT INTO fleet_target_aliases (alias, target_id) VALUES ($1,$2)`, alias, targetID); err != nil {
			return nil, err
		}
	}
	if !existing || len(newAliases) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE fleet_target_registry SET generation = generation + 1 WHERE singleton`); err != nil {
			return nil, err
		}
	}
	return readFleetTargetTx(ctx, tx, targetID)
}

func readFleetTargetTx(ctx context.Context, tx pgx.Tx, targetID string) (*FleetTarget, error) {
	target := &FleetTarget{TargetID: targetID}
	if err := tx.QueryRow(ctx, `
		SELECT provider, provider_account, state_backend, created_at, registration_operation_id
		FROM fleet_targets WHERE target_id=$1
	`, targetID).Scan(&target.Provider, &target.ProviderAccount, &target.StateBackend, &target.CreatedAt, &target.RegistrationOperationID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT alias FROM fleet_target_aliases WHERE target_id=$1 ORDER BY alias`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, err
		}
		target.Aliases = append(target.Aliases, alias)
	}
	return target, rows.Err()
}

// GetFleetTarget returns targetID's identity and aliases, or nil if it has
// never been registered.
func (db *DB) GetFleetTarget(ctx context.Context, targetID string) (*FleetTarget, error) {
	var target FleetTarget
	target.TargetID = targetID
	err := db.Pool.QueryRow(ctx, `
		SELECT provider, provider_account, state_backend, created_at, registration_operation_id
		FROM fleet_targets WHERE target_id=$1
	`, targetID).Scan(&target.Provider, &target.ProviderAccount, &target.StateBackend, &target.CreatedAt, &target.RegistrationOperationID)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := db.Pool.Query(ctx, `SELECT alias FROM fleet_target_aliases WHERE target_id=$1 ORDER BY alias`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, err
		}
		target.Aliases = append(target.Aliases, alias)
	}
	return &target, rows.Err()
}

// ResolveFleetTargetForPlan resolves cluster's alias and, if environment is
// non-empty, cross-checks the environment alias for the dispatch lane
// (plan.md §2.2's two-step resolution). It runs inside tx, taking the
// registry FOR SHARE as lock-order position 2, so the registry-empty
// decision and alias resolution are read in the same transaction as the
// mutation they gate (M1): a plan can never be admitted "as if unregistered"
// against a target that is registering concurrently, because registration
// holds the same row FOR UPDATE for the whole of its own transaction.
//
// registryEmpty=true means the registry has never had a target registered;
// callers must then behave exactly as they did before this change, and
// targetID is always "". A non-empty registry with no matching cluster
// alias returns a *lifecycle.FenceError with Code
// lifecycle.CodeFleetTargetUnregistered. A cluster alias and an environment
// alias that resolve to different targets return
// lifecycle.CodeFleetTargetAliasConflict.
func ResolveFleetTargetForPlan(ctx context.Context, tx pgx.Tx, cluster, environment string) (targetID string, registryEmpty bool, err error) {
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT generation FROM fleet_target_registry WHERE singleton FOR SHARE`).Scan(&generation); err != nil {
		return "", false, err
	}
	if generation == 0 {
		return "", true, nil
	}
	var clusterTarget string
	err = tx.QueryRow(ctx, `SELECT target_id FROM fleet_target_aliases WHERE alias=$1`, "cluster:"+cluster).Scan(&clusterTarget)
	switch {
	case err == pgx.ErrNoRows:
		return "", false, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetUnregistered}
	case err != nil:
		return "", false, err
	}
	if environment != "" {
		var envTarget string
		err = tx.QueryRow(ctx, `SELECT target_id FROM fleet_target_aliases WHERE alias=$1`, "environment:"+environment).Scan(&envTarget)
		switch {
		case err == pgx.ErrNoRows:
			// No environment alias registered; the cluster alias alone resolves.
		case err != nil:
			return "", false, err
		case envTarget != clusterTarget:
			return "", false, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetAliasConflict}
		}
	}
	return clusterTarget, false, nil
}

// GetFleetTargetFence reads one target's fence row inside tx, at lock-order
// position 4. forUpdate selects FOR UPDATE (acquire, bind, release, abandon)
// or FOR SHARE (every other reader, including evidence-write epoch checks).
// Registration creates the row with the target, so a missing row means the
// target is not registered and is an error (fail closed): returning a free
// zero value here would let two acquirers race an INSERT.
func GetFleetTargetFence(ctx context.Context, tx pgx.Tx, targetID string, forUpdate bool) (lifecycle.FenceFacts, error) {
	lockClause := "FOR SHARE"
	if forUpdate {
		lockClause = "FOR UPDATE"
	}
	row := tx.QueryRow(ctx, `
		SELECT generation, held, holder_plan_id, holder_nonce_sha256, authority_epoch,
		       last_release_plan_id, last_release_reason, last_release_at, revision
		FROM fleet_target_fences WHERE target_id=$1 `+lockClause, targetID)
	var f lifecycle.FenceFacts
	var lastReleasePlan, lastReleaseReason string
	var lastReleaseAt *time.Time
	err := row.Scan(&f.Generation, &f.Held, &f.HolderPlanID, &f.HolderNonceSHA256, &f.AuthorityEpoch,
		&lastReleasePlan, &lastReleaseReason, &lastReleaseAt, &f.Revision)
	if err == pgx.ErrNoRows {
		return lifecycle.FenceFacts{}, fmt.Errorf("fleet target %q has no fence row", targetID)
	}
	if err != nil {
		return lifecycle.FenceFacts{}, err
	}
	f.TargetID = targetID
	if lastReleaseReason != "" {
		at := time.Time{}
		if lastReleaseAt != nil {
			at = *lastReleaseAt
		}
		f.LastRelease = &lifecycle.Release{PlanID: lastReleasePlan, Reason: lastReleaseReason, At: at}
	}
	return f, nil
}

// FleetAuthorityEpoch reads the singleton authority epoch inside tx, FOR
// SHARE, at lock-order position 3.
func FleetAuthorityEpoch(ctx context.Context, tx pgx.Tx) (int64, error) {
	var epoch int64
	err := tx.QueryRow(ctx, `SELECT epoch FROM fleet_authority_epoch WHERE singleton FOR SHARE`).Scan(&epoch)
	return epoch, err
}

// AdvanceFleetAuthorityEpoch CASes the singleton epoch forward from expected,
// recording reason and the server clock. It touches no fence or attempt
// row: old-epoch fences become Uncertain/AuthoritySuperseded until they are
// re-bound or released (plan.md §2.2), not rewritten here.
func (db *DB) AdvanceFleetAuthorityEpoch(ctx context.Context, expected int64, reason string) (int64, error) {
	var epoch int64
	err := db.Pool.QueryRow(ctx, `
		UPDATE fleet_authority_epoch SET epoch = epoch + 1, activated_at = now(), reason = $2
		WHERE singleton AND epoch = $1
		RETURNING epoch
	`, expected, reason).Scan(&epoch)
	if err == pgx.ErrNoRows {
		return 0, ErrFleetAuthorityEpochConflict
	}
	return epoch, err
}

// --- WP8a: fence wiring for the PG legacy dispatch and attempt paths ---
//
// Everything below is read or written inside a caller-supplied tx (or a tx
// this file opens itself for a single call), always in the M7 lock order
// documented at the top of this file: registry/epoch (2-3) before fences
// (4) before dispatches (5) before attempts (6). Every acquire, rerun-
// acquire and bind call also checks fleet_target_abandoned_plans before
// touching the fence, per the WP3 review note WP8a must honor.

// PutFleetTargetFence durably writes next as its target's fence row, inside
// tx. The caller must already hold that row FOR UPDATE in the same
// transaction (GetFleetTargetFence/LockFleetTargetFenceForPlan with
// forUpdate=true), which every Decide* transition in this file does.
func PutFleetTargetFence(ctx context.Context, tx pgx.Tx, next lifecycle.FenceFacts) error {
	var lastPlan, lastReason string
	var lastAt *time.Time
	if next.LastRelease != nil {
		lastPlan, lastReason = next.LastRelease.PlanID, next.LastRelease.Reason
		at := next.LastRelease.At
		lastAt = &at
	}
	_, err := tx.Exec(ctx, `
		UPDATE fleet_target_fences
		SET generation=$2, held=$3, holder_plan_id=$4, holder_nonce_sha256=$5, authority_epoch=$6,
		    last_release_plan_id=$7, last_release_reason=$8, last_release_at=$9, revision=$10
		WHERE target_id=$1
	`, next.TargetID, next.Generation, next.Held, next.HolderPlanID, next.HolderNonceSHA256, next.AuthorityEpoch,
		lastPlan, lastReason, lastAt, next.Revision)
	return err
}

// LockFleetTargetFenceForPlan resolves cluster/environment to a target
// (lock-order position 2) and, when the registry is non-empty, reads the
// current authority epoch (position 3) and that target's fence row
// (position 4, FOR UPDATE or FOR SHARE per forUpdate), inside tx.
// registryEmpty=true means the caller must behave exactly as it did before
// this change; every other return value is then zero. A non-empty registry
// with an unresolvable cluster or a cluster/environment alias conflict
// returns the *lifecycle.FenceError ResolveFleetTargetForPlan reports.
func LockFleetTargetFenceForPlan(ctx context.Context, tx pgx.Tx, cluster, environment string, forUpdate bool) (targetID string, fence lifecycle.FenceFacts, epoch int64, registryEmpty bool, err error) {
	targetID, registryEmpty, err = ResolveFleetTargetForPlan(ctx, tx, cluster, environment)
	if err != nil || registryEmpty {
		return targetID, lifecycle.FenceFacts{}, 0, registryEmpty, err
	}
	if epoch, err = FleetAuthorityEpoch(ctx, tx); err != nil {
		return targetID, lifecycle.FenceFacts{}, 0, false, err
	}
	fence, err = GetFleetTargetFence(ctx, tx, targetID, forUpdate)
	return targetID, fence, epoch, false, err
}

// LockHeldFleetTargetFenceForPlan finds the target whose fence is currently
// held by planID (if any) and locks it, inside tx. It is only for the
// plan-keyed break-glass abandon (H7), which must work whether or not the
// plan's cluster is registered. Every execution path (bind, evidence writes,
// both automatic releases) resolves through the plan's cluster alias with
// lockFleetTargetFenceForPlanID instead, the same resolution acquire and the
// heartbeat/advance predicate use, so a plan whose registered target is not
// held by it is refused rather than treated as unfenced. targetID=="" means
// no fence is held by planID. The unlocked lookup can race a release, so
// callers must re-check the locked fence's holder.
func LockHeldFleetTargetFenceForPlan(ctx context.Context, tx pgx.Tx, planID string, forUpdate bool) (targetID string, fence lifecycle.FenceFacts, epoch int64, err error) {
	err = tx.QueryRow(ctx, `SELECT target_id FROM fleet_target_fences WHERE held AND holder_plan_id=$1`, planID).Scan(&targetID)
	if err == pgx.ErrNoRows {
		return "", lifecycle.FenceFacts{}, 0, nil
	}
	if err != nil {
		return "", lifecycle.FenceFacts{}, 0, err
	}
	if epoch, err = FleetAuthorityEpoch(ctx, tx); err != nil {
		return targetID, lifecycle.FenceFacts{}, 0, err
	}
	fence, err = GetFleetTargetFence(ctx, tx, targetID, forUpdate)
	return targetID, fence, epoch, err
}

// lockFleetTargetFenceForPlanID resolves planID's own cluster (and, when a
// dispatch row exists, its fleet environment lane) to a target exactly as
// acquire does, then reads the epoch and locks that target's fence, inside
// tx, in lock order 2-3-4. It also returns planID's recorded dispatch nonce
// hash ("" before any dispatch row exists).
//
// strict=true (bind) propagates an unregistered-cluster or alias-conflict
// refusal when the registry is non-empty (Q1). strict=false (evidence writes
// and the automatic releases, mirroring the etcd backend) downgrades that
// refusal to "no target", so an attempt admitted before an unrelated
// registration is never newly blocked by it. targetID=="" means the caller
// must behave exactly as it did before the fence existed.
func lockFleetTargetFenceForPlanID(ctx context.Context, tx pgx.Tx, planID string, forUpdate, strict bool) (targetID string, fence lifecycle.FenceFacts, epoch int64, nonceHash string, err error) {
	cluster, environment, nonceHash, err := fleetTargetBindInputs(ctx, tx, planID)
	if err != nil {
		return "", lifecycle.FenceFacts{}, 0, "", err
	}
	targetID, fence, epoch, registryEmpty, err := LockFleetTargetFenceForPlan(ctx, tx, cluster, environment, forUpdate)
	if err != nil {
		var fenceErr *lifecycle.FenceError
		if !strict && errors.As(err, &fenceErr) {
			return "", lifecycle.FenceFacts{}, 0, nonceHash, nil
		}
		return "", lifecycle.FenceFacts{}, 0, "", err
	}
	if registryEmpty {
		return "", lifecycle.FenceFacts{}, 0, nonceHash, nil
	}
	return targetID, fence, epoch, nonceHash, nil
}

// fleetTargetBindInputs reads the cluster named by planID's capacity-plan
// payload and, if a protected GitHub dispatch binding already exists for
// planID, its fleet environment lane and dispatch nonce hash. A plan that has
// no dispatch row yet (first call before SeedDispatch-equivalent state
// exists) returns environment="" and nonceHash="" rather than an error: the
// cluster alone is enough for callers that only need the registry-empty
// decision.
func fleetTargetBindInputs(ctx context.Context, tx pgx.Tx, planID string) (cluster, environment, nonceHash string, err error) {
	err = tx.QueryRow(ctx, `SELECT COALESCE(payload->>'cluster', '') FROM operations WHERE id=$1`, planID).Scan(&cluster)
	if err != nil && err != pgx.ErrNoRows {
		return "", "", "", err
	}
	// A missing plan row resolves as cluster "": with an empty registry that
	// is the legacy no-fence path; otherwise it is fleet_target_unregistered.
	err = tx.QueryRow(ctx, `SELECT fleet_environment, dispatch_nonce_sha256 FROM fleet_github_dispatches WHERE plan_id=$1`, planID).Scan(&environment, &nonceHash)
	if err == pgx.ErrNoRows {
		return cluster, "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	return cluster, environment, nonceHash, nil
}

// IsFleetPlanAbandoned reports whether planID has a permanent break-glass
// abandon record (B1/H7). Every acquire, rerun-acquire, bind and checkpoint
// path checks this before touching the fence (WP3 review note): once a plan
// is abandoned, it refuses forever, in the same transaction as the check.
func IsFleetPlanAbandoned(ctx context.Context, tx pgx.Tx, planID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fleet_target_abandoned_plans WHERE plan_id=$1)`, planID).Scan(&exists)
	return exists, err
}

// AbandonFleetPlan writes planID's permanent break-glass abandon record
// (B1/H7), inside tx. targetID is the plan's resolved target, or "" when its
// cluster was never registered (H7): the row then has a NULL target_id and
// records the plan's cluster name instead (the table's CHECK refuses a row
// with neither), so it never points at a fabricated target.
func AbandonFleetPlan(ctx context.Context, tx pgx.Tx, planID, nonceSHA256, targetID, operationID string, now time.Time) error {
	var fkTarget *string
	if targetID != "" {
		fkTarget = &targetID
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO fleet_target_abandoned_plans (plan_id, nonce_sha256, target_id, cluster, operation_id, abandoned_at)
		VALUES ($1, $2, $3, COALESCE((SELECT payload->>'cluster' FROM operations WHERE id=$1), ''), $4, $5)
		ON CONFLICT (plan_id) DO NOTHING
	`, planID, nonceSHA256, fkTarget, operationID, now)
	return err
}

// FleetTargetHolderFacts reads planID's dispatch and runner-attempt rows
// inside tx (lock-order positions 5 and 6) and projects them to
// lifecycle.HolderFacts, for Outcome derivation or DecideRelease. It is a
// plain read: it takes no lock beyond the caller's own transaction isolation.
func FleetTargetHolderFacts(ctx context.Context, tx pgx.Tx, planID string) (lifecycle.HolderFacts, error) {
	var h lifecycle.HolderFacts
	var createdAt time.Time
	var submissionStartedAt *time.Time
	err := tx.QueryRow(ctx, `SELECT dispatch_state, created_at, submission_started_at FROM fleet_github_dispatches WHERE plan_id=$1`, planID).
		Scan(&h.DispatchState, &createdAt, &submissionStartedAt)
	switch {
	case err == pgx.ErrNoRows:
		// No dispatch row yet; DispatchState stays "".
	case err != nil:
		return h, err
	default:
		h.DispatchCreatedAt = createdAt
		if submissionStartedAt != nil {
			h.SubmissionStartedAt = *submissionStartedAt
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
		       commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
		       heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
		       heartbeat_at, updated_at, finished_at, last_error, metadata
		FROM fleet_runner_attempts WHERE plan_id=$1
	`, planID)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	for rows.Next() {
		attempt, err := scanFleetRunnerAttempt(rows)
		if err != nil {
			return h, err
		}
		h.Attempts = append(h.Attempts, lifecycle.FromLegacy(attempt))
	}
	if err := rows.Err(); err != nil {
		return h, err
	}
	h.Abandoned, err = IsFleetPlanAbandoned(ctx, tx, planID)
	return h, err
}

// IsFleetPlanAbandonedForPlan is the read-only wrapper handlers use to
// distinguish an abandoned-plan refusal (fleet_target_holder_abandoned, 409)
// from an ordinary failure after a lock-free UPDATE such as
// FinishFleetGitHubDispatch returns no row.
func (db *DB) IsFleetPlanAbandonedForPlan(ctx context.Context, planID string) (bool, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return IsFleetPlanAbandoned(ctx, tx, planID)
}

// ResolveFleetTargetForCluster is the read-only wrapper handlers use for a
// diagnostic or pre-flight resolve outside any mutating transaction (the Q11
// stop-check gate and the m11 non-locking occupancy pre-check both need to
// know only whether the registry is non-empty before doing anything else).
func (db *DB) ResolveFleetTargetForCluster(ctx context.Context, cluster, environment string) (targetID string, registryEmpty bool, err error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return ResolveFleetTargetForPlan(ctx, tx, cluster, environment)
}

// FleetTargetEpochSupersededForPlan is a diagnostic-only read used after a
// heartbeat or advance UPDATE's epoch predicate (store/fleet_runner_attempts.go)
// has already refused the write, so the handler can choose the specific
// fleet_target_authority_superseded code (Q10) instead of the generic
// staleness code the predicate's miss otherwise produces. It evaluates the
// exact predicate the UPDATE used (fleetTargetEpochRefusalSQL), never gates
// the write itself and takes no lock.
func (db *DB) FleetTargetEpochSupersededForPlan(ctx context.Context, planID string) (bool, error) {
	var predicateHolds bool
	err := db.Pool.QueryRow(ctx, `SELECT true WHERE true `+fleetTargetEpochRefusalSQL("$1::text"), planID).Scan(&predicateHolds)
	if err == pgx.ErrNoRows {
		// The NOT EXISTS predicate failed, i.e. the epoch refused the write.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

// CheckFleetTargetAcquirePreflight is the m11 non-locking pre-check: it
// re-runs DecideAcquire's refusal logic under FOR SHARE (never FOR UPDATE),
// before the caller reserves a signed operation, so a request that the
// authoritative FOR UPDATE acquire would refuse anyway never leaves a queued
// reservation behind it. The locked acquire call remains authoritative; a
// nil return here is advisory, not a guarantee.
func (db *DB) CheckFleetTargetAcquirePreflight(ctx context.Context, cluster, environment, planID, nonceHash string, planStartedAt time.Time) error {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	abandoned, err := IsFleetPlanAbandoned(ctx, tx, planID)
	if err != nil {
		return err
	}
	if abandoned {
		return &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetHolderAbandoned}
	}
	_, fence, epoch, registryEmpty, err := LockFleetTargetFenceForPlan(ctx, tx, cluster, environment, false)
	if err != nil || registryEmpty {
		return err
	}
	_, decErr := lifecycle.DecideAcquire(fence, planID, nonceHash, planStartedAt, epoch)
	return decErr
}

// Release: succeeded (plan.md §2.2) is no longer a separate call: it is
// committed atomically inside AdvanceFleetRunnerAttempt's terminal branch
// (store/fleet_runner_attempts.go), in the same transaction as the UPDATE
// that marks the attempt succeeded, so a crash between the two can never
// happen and no release error is ever silently discarded.
