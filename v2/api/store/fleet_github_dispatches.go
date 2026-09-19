package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// FleetGitHubDispatch is internal durable dispatch state. The raw nonce is
// retained only here so an interrupted server can recover an already-submitted
// GitHub dispatch without exposing the correlator through operation responses.
type FleetGitHubDispatch struct {
	PlanID              string
	PlanRunID           int64
	PlanSHA256          string
	ApprovedHeadSHA     string
	FleetEnvironment    string
	AllowDestructive    bool
	DispatchNonce       string
	DispatchNonceSHA256 string
	RunID               int64
	WorkflowURL         string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

const fleetGitHubDispatchColumns = `plan_id, plan_run_id, plan_sha256, approved_head_sha, fleet_environment, allow_destructive, dispatch_nonce, dispatch_nonce_sha256, run_id, workflow_url, created_at, updated_at`

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
	if _, err := db.Pool.Exec(ctx, `INSERT INTO fleet_github_dispatches (`+fleetGitHubDispatchColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now(),now())`,
		item.PlanID, item.PlanRunID, item.PlanSHA256, item.ApprovedHeadSHA, item.FleetEnvironment, item.AllowDestructive, item.DispatchNonce, item.DispatchNonceSHA256, item.RunID, item.WorkflowURL); err != nil {
		return nil, err
	}
	return db.GetFleetGitHubDispatch(ctx, item.PlanID)
}

func (db *DB) FinishFleetGitHubDispatch(ctx context.Context, planID, nonceHash string, runID int64, workflowURL string) (*FleetGitHubDispatch, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("fleet GitHub dispatch store is unavailable")
	}
	return scanFleetGitHubDispatch(db.Pool.QueryRow(ctx, `UPDATE fleet_github_dispatches
		SET run_id=$3, workflow_url=$4, updated_at=now()
		WHERE plan_id=$1 AND dispatch_nonce_sha256=$2
		RETURNING `+fleetGitHubDispatchColumns, planID, nonceHash, runID, workflowURL))
}

func scanFleetGitHubDispatch(row pgx.Row) (*FleetGitHubDispatch, error) {
	var item FleetGitHubDispatch
	if err := row.Scan(&item.PlanID, &item.PlanRunID, &item.PlanSHA256, &item.ApprovedHeadSHA, &item.FleetEnvironment, &item.AllowDestructive, &item.DispatchNonce, &item.DispatchNonceSHA256, &item.RunID, &item.WorkflowURL, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	return &item, nil
}
