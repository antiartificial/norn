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
	PlanID              string
	PlanRunID           int64
	PlanSHA256          string
	ApprovedHeadSHA     string
	PilotRunID          string
	FleetEnvironment    string
	AllowDestructive    bool
	DispatchNonceSHA256 string
	DispatchState       string
	SubmissionStartedAt *time.Time
	RunID               int64
	WorkflowURL         string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

const fleetGitHubDispatchColumns = `plan_id, plan_run_id, plan_sha256, approved_head_sha, pilot_run_id, fleet_environment, allow_destructive, dispatch_nonce_sha256, dispatch_state, submission_started_at, run_id, workflow_url, created_at, updated_at`

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
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now(),now())`,
		item.PlanID, item.PlanRunID, item.PlanSHA256, item.ApprovedHeadSHA, item.PilotRunID, item.FleetEnvironment, item.AllowDestructive, item.DispatchNonceSHA256, item.DispatchState, item.SubmissionStartedAt, item.RunID, item.WorkflowURL); err != nil {
		return nil, err
	}
	return db.GetFleetGitHubDispatch(ctx, item.PlanID)
}

func (db *DB) FinishFleetGitHubDispatch(ctx context.Context, planID, nonceHash string, runID int64, workflowURL string) (*FleetGitHubDispatch, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("fleet GitHub dispatch store is unavailable")
	}
	return scanFleetGitHubDispatch(db.Pool.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET run_id=$3, workflow_url=$4, dispatch_state='dispatched', updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND run_id IN (0, $3)
		RETURNING `+fleetGitHubDispatchColumns, planID, nonceHash, runID, workflowURL))
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

// DeletePreparedFleetGitHubDispatch removes only a confirmed pre-submit
// failure. The caller holds the per-plan lock and invokes it only when the
// client proves that no POST was attempted.
func (db *DB) DeletePreparedFleetGitHubDispatch(ctx context.Context, planID, nonceHash string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM fleet_github_dispatches WHERE plan_id=$1 AND dispatch_nonce_sha256=$2 AND dispatch_state IN ('prepared','submitting') AND run_id=0`, planID, nonceHash)
	return err
}

func scanFleetGitHubDispatch(row pgx.Row) (*FleetGitHubDispatch, error) {
	var item FleetGitHubDispatch
	if err := row.Scan(&item.PlanID, &item.PlanRunID, &item.PlanSHA256, &item.ApprovedHeadSHA, &item.PilotRunID, &item.FleetEnvironment, &item.AllowDestructive, &item.DispatchNonceSHA256, &item.DispatchState, &item.SubmissionStartedAt, &item.RunID, &item.WorkflowURL, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	return &item, nil
}
