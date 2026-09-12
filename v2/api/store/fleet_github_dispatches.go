package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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
	return scanFleetGitHubDispatch(db.Pool.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET run_id=$3, run_attempt=CASE WHEN $4 > 0 THEN $4 ELSE run_attempt END, workflow_url=$5, dispatch_state='dispatched', updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND run_id IN (0, $3)
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

// ResetFleetGitHubDispatchPreSubmit returns only a client-proven pre-submit
// execution failure to prepared. The approval and nonce hash remain immutable,
// so execute can retry the same reviewed authority without creating another
// dispatch candidate. Any post-submission uncertainty stays submitting.
func (db *DB) ResetFleetGitHubDispatchPreSubmit(ctx context.Context, planID, nonceHash string) (*FleetGitHubDispatch, error) {
	return scanFleetGitHubDispatch(db.Pool.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET dispatch_state='prepared', submission_started_at=NULL, updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND dispatch_state='submitting' AND run_id=0
		RETURNING `+fleetGitHubDispatchColumns, planID, nonceHash))
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
