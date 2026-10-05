package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet/lifecycle"
)

// FleetGitHubDispatch is durable protected-dispatch state. Only a SHA-256
// nonce verifier is retained; the raw workflow input is never persisted or
// returned by Norn.
type FleetGitHubDispatch struct {
	PlanID                 string
	PlanRunID              int64
	PlanSHA256             string
	ApprovedHeadSHA        string
	PilotRunID             string
	FleetEnvironment       string
	AllowDestructive       bool
	DispatchNonceSHA256    string
	ApprovalEnvelopeSHA256 string
	DispatchState          string
	SubmissionStartedAt    *time.Time
	RunID                  int64
	RunAttempt             int
	WorkflowURL            string
	RerunStartedAt         *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

const fleetGitHubDispatchColumns = `plan_id, plan_run_id, plan_sha256, approved_head_sha, pilot_run_id, fleet_environment, allow_destructive, dispatch_nonce_sha256, approval_envelope_sha256, dispatch_state, submission_started_at, run_id, run_attempt, workflow_url, rerun_started_at, created_at, updated_at`

func (db *DB) GetFleetGitHubDispatch(ctx context.Context, planID string) (*FleetGitHubDispatch, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("fleet GitHub dispatch store is unavailable")
	}
	return scanFleetGitHubDispatch(db.Pool.QueryRow(ctx, `SELECT `+fleetGitHubDispatchColumns+` FROM fleet_github_dispatches WHERE plan_id=$1`, planID))
}

func (db *DB) CreateFleetGitHubDispatch(ctx context.Context, item FleetGitHubDispatch) (*FleetGitHubDispatch, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("fleet GitHub dispatch store is unavailable")
	}
	if item.DispatchState == "" {
		item.DispatchState = "prepared"
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO fleet_github_dispatches (`+fleetGitHubDispatchColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,now(),now())`,
		item.PlanID, item.PlanRunID, item.PlanSHA256, item.ApprovedHeadSHA, item.PilotRunID, item.FleetEnvironment, item.AllowDestructive, item.DispatchNonceSHA256, item.ApprovalEnvelopeSHA256, item.DispatchState, item.SubmissionStartedAt, item.RunID, item.RunAttempt, item.WorkflowURL, item.RerunStartedAt); err != nil {
		return nil, err
	}
	return db.GetFleetGitHubDispatch(ctx, item.PlanID)
}

// BindFleetGitHubDispatchApproval binds the exact owner-signed envelope digest
// before the one permitted workflow POST.  It is intentionally immutable: a
// caller cannot substitute an approval while reusing a prepared nonce.
func (db *DB) BindFleetGitHubDispatchApproval(ctx context.Context, planID, nonceHash, approvalHash string) (*FleetGitHubDispatch, error) {
	return scanFleetGitHubDispatch(db.Pool.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET approval_envelope_sha256=$3, updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND dispatch_state='prepared' AND run_id=0
		AND approval_envelope_sha256=''
		RETURNING `+fleetGitHubDispatchColumns, planID, nonceHash, approvalHash))
}

func (db *DB) FinishFleetGitHubDispatch(ctx context.Context, planID, nonceHash string, runID int64, runAttempt int, workflowURL string) (*FleetGitHubDispatch, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("fleet GitHub dispatch store is unavailable")
	}
	// A plan that break-glass abandonment has permanently refused (B1/H7)
	// can never finish its dispatch: a run that appears later must not bind.
	// This predicate costs no new lock (plan.md §2.2 "What abandonment
	// refuses"; WP3 review note).
	return scanFleetGitHubDispatch(db.Pool.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET run_id=$3, run_attempt=CASE WHEN $4 > 0 THEN $4 ELSE run_attempt END, workflow_url=$5, dispatch_state='dispatched', updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND run_id IN (0, $3)
		  AND NOT EXISTS (SELECT 1 FROM fleet_target_abandoned_plans ap WHERE ap.plan_id = fleet_github_dispatches.plan_id)
		RETURNING `+fleetGitHubDispatchColumns, planID, nonceHash, runID, runAttempt, workflowURL))
}

// MarkFleetGitHubDispatchRerunSubmitting fences one observed workflow-run
// generation. Any transport failure after this transition is ambiguous;
// callers must only inspect the original GitHub run and never send a second
// rerun request for that generation.
func (db *DB) MarkFleetGitHubDispatchRerunSubmitting(ctx context.Context, planID, nonceHash string, runID int64, runAttempt int) (*FleetGitHubDispatch, error) {
	return scanFleetGitHubDispatch(db.Pool.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET dispatch_state='rerun_submitting', rerun_started_at=now(), updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND dispatch_state='dispatched'
		AND run_id=$3 AND run_attempt=$4 AND run_attempt > 0 AND approval_envelope_sha256<>''
		RETURNING `+fleetGitHubDispatchColumns, planID, nonceHash, runID, runAttempt))
}

// FinishFleetGitHubDispatchRerun records only an observed increment of the
// original GitHub run's attempt number. It returns to dispatched so a later,
// conclusively failed generation may receive its own independently fenced
// rerun, while never allowing a second POST for the current generation.
func (db *DB) FinishFleetGitHubDispatchRerun(ctx context.Context, planID, nonceHash string, runID int64, previousAttempt, rerunAttempt int) (*FleetGitHubDispatch, error) {
	return scanFleetGitHubDispatch(db.Pool.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET dispatch_state='dispatched', run_attempt=$5, updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND dispatch_state='rerun_submitting'
		AND run_id=$3 AND run_attempt=$4 AND $5=$4+1
		RETURNING `+fleetGitHubDispatchColumns, planID, nonceHash, runID, previousAttempt, rerunAttempt))
}

// MarkFleetGitHubDispatchSubmitting creates the durable fence immediately
// before the only allowed POST. A later caller can never send a second POST
// from an ambiguous state.
func (db *DB) MarkFleetGitHubDispatchSubmitting(ctx context.Context, planID, nonceHash string) (*FleetGitHubDispatch, error) {
	return scanFleetGitHubDispatch(db.Pool.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET dispatch_state='submitting', submission_started_at=now(), updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND dispatch_state='prepared' AND run_id=0
		RETURNING `+fleetGitHubDispatchColumns, planID, nonceHash))
}

// MarkFleetGitHubDispatchSubmittingFenced is MarkFleetGitHubDispatchSubmitting
// plus the target mutation fence's Acquire transition (plan.md §2.2): inside
// one transaction, it takes the plan advisory lock, (when the registry is
// non-empty) locks cluster/environment's fence FOR UPDATE, refuses an
// abandoned plan, and runs lifecycle.DecideAcquire before the dispatch row's
// own fence transitions to submitting. When the registry is empty its
// dispatch-row effect is identical to MarkFleetGitHubDispatchSubmitting (Q1).
func (db *DB) MarkFleetGitHubDispatchSubmittingFenced(ctx context.Context, planID, nonceHash, cluster, environment string, planStartedAt time.Time) (*FleetGitHubDispatch, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock order position 1: the plan advisory lock serializes acquire with
	// the plan-keyed abandon (abandonFleetPlanTx), which takes the same lock
	// but does not lock a fence its plan does not hold. Without it, an acquire
	// of a free fence could read "not abandoned" before a concurrent abandon
	// commits and leave the fence held by an abandoned plan.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+planID); err != nil {
		return nil, err
	}
	_, fence, epoch, registryEmpty, err := LockFleetTargetFenceForPlan(ctx, tx, cluster, environment, true)
	if err != nil {
		return nil, err
	}
	// The abandoned check follows the advisory lock and the fence FOR UPDATE
	// (lock order positions 1 and 4), so a concurrent abandon of this plan or
	// release of this fence has either committed (and is visible here) or not
	// started: the fence can never be left held by an abandoned plan.
	abandoned, err := IsFleetPlanAbandoned(ctx, tx, planID)
	if err != nil {
		return nil, err
	}
	if abandoned {
		return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetHolderAbandoned}
	}
	if !registryEmpty {
		next, decErr := lifecycle.DecideAcquire(fence, planID, nonceHash, planStartedAt, epoch)
		if decErr != nil {
			return nil, decErr
		}
		if err := PutFleetTargetFence(ctx, tx, next); err != nil {
			return nil, err
		}
	}
	dispatch, err := scanFleetGitHubDispatch(tx.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET dispatch_state='submitting', submission_started_at=now(), updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND dispatch_state='prepared' AND run_id=0
		RETURNING `+fleetGitHubDispatchColumns, planID, nonceHash))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return dispatch, nil
}

// MarkFleetGitHubDispatchRerunSubmittingFenced is
// MarkFleetGitHubDispatchRerunSubmitting plus M5's rerun fence rule: the
// fence must already be held by this exact plan and nonce at the current
// authority epoch (DecideAcquire's same-holder branch, which never silently
// adopts a newer epoch), and the plan must not be abandoned.
func (db *DB) MarkFleetGitHubDispatchRerunSubmittingFenced(ctx context.Context, planID, nonceHash string, runID int64, runAttempt int, cluster, environment string) (*FleetGitHubDispatch, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, fence, epoch, registryEmpty, err := LockFleetTargetFenceForPlan(ctx, tx, cluster, environment, true)
	if err != nil {
		return nil, err
	}
	// The abandoned check follows the fence FOR UPDATE (lock order position 4),
	// so a concurrent abandon that already holds or has committed this fence
	// is always visible: the fence can never be left held by an abandoned plan.
	abandoned, err := IsFleetPlanAbandoned(ctx, tx, planID)
	if err != nil {
		return nil, err
	}
	if abandoned {
		return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetHolderAbandoned}
	}
	if !registryEmpty {
		// M5: a rerun never acquires a free fence; it only re-asserts one
		// this exact plan and nonce already hold.
		if !fence.Held || fence.HolderPlanID != planID || fence.HolderNonceSHA256 != nonceHash {
			return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetExecutionOccupied}
		}
		next, decErr := lifecycle.DecideAcquire(fence, planID, nonceHash, time.Now().UTC(), epoch)
		if decErr != nil {
			return nil, decErr
		}
		if err := PutFleetTargetFence(ctx, tx, next); err != nil {
			return nil, err
		}
	}
	dispatch, err := scanFleetGitHubDispatch(tx.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET dispatch_state='rerun_submitting', rerun_started_at=now(), updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND dispatch_state='dispatched'
		AND run_id=$3 AND run_attempt=$4 AND run_attempt > 0 AND approval_envelope_sha256<>''
		RETURNING `+fleetGitHubDispatchColumns, planID, nonceHash, runID, runAttempt))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return dispatch, nil
}

// ResetFleetGitHubDispatchPreSubmit returns only a client-proven pre-submit
// execution failure to prepared. The approval and nonce hash remain immutable,
// so execute can retry the same reviewed authority without creating another
// dispatch candidate. Any post-submission uncertainty stays submitting.
//
// This is also the fence's "never submitted" release (plan.md §2.2): a plan
// that never registered an attempt was never really executing, so once the
// reset proves no POST happened, its target's fence (if any) is freed with
// reason dispatch_not_submitted in the same transaction. When the registry
// is empty this is byte-identical to the unfenced reset (Q1).
func (db *DB) ResetFleetGitHubDispatchPreSubmit(ctx context.Context, planID, nonceHash string) (*FleetGitHubDispatch, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The fence (lock-order position 4) is resolved through the plan's
	// cluster alias and locked before the dispatch row (position 5), so this
	// path never inverts against acquire, which locks fence then dispatch.
	targetID, fence, _, _, err := lockFleetTargetFenceForPlanID(ctx, tx, planID, true, false)
	if err != nil {
		return nil, err
	}
	dispatch, err := scanFleetGitHubDispatch(tx.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET dispatch_state='prepared', submission_started_at=NULL, updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND dispatch_state='submitting' AND run_id=0
		RETURNING `+fleetGitHubDispatchColumns, planID, nonceHash))
	if err != nil {
		return nil, err
	}
	// Release only a fence this exact plan and nonce hold; a fence that is
	// free or held by another plan is not this dispatch's to release.
	if targetID != "" && fence.Held && fence.HolderPlanID == planID && fence.HolderNonceSHA256 == nonceHash {
		holder, err := FleetTargetHolderFacts(ctx, tx, planID)
		if err != nil {
			return nil, err
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return nil, err
		}
		next, relErr := lifecycle.DecideRelease(fence, holder, lifecycle.ReleaseModeDispatchNotSubmitted, nil, now)
		if relErr != nil {
			return nil, relErr
		}
		if err := PutFleetTargetFence(ctx, tx, next); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return dispatch, nil
}

// DeletePreparedFleetGitHubDispatch removes only a confirmed pre-submit
// failure. The caller holds the per-plan lock and invokes it only when the
// client proves that no POST was attempted.
func (db *DB) DeletePreparedFleetGitHubDispatch(ctx context.Context, planID, nonceHash string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM fleet_github_dispatches WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND dispatch_state='prepared' AND run_id=0`, planID, nonceHash)
	return err
}

func scanFleetGitHubDispatch(row pgx.Row) (*FleetGitHubDispatch, error) {
	var item FleetGitHubDispatch
	if err := row.Scan(&item.PlanID, &item.PlanRunID, &item.PlanSHA256, &item.ApprovedHeadSHA, &item.PilotRunID, &item.FleetEnvironment, &item.AllowDestructive, &item.DispatchNonceSHA256, &item.ApprovalEnvelopeSHA256, &item.DispatchState, &item.SubmissionStartedAt, &item.RunID, &item.RunAttempt, &item.WorkflowURL, &item.RerunStartedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	return &item, nil
}
