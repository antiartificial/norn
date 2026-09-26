package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

// DeploymentCompletionRegion is the final public status for one accepted
// regional intent. The deployment and every region become deployed together.
type DeploymentCompletionRegion struct {
	Region       string
	EvalID       string
	ActiveWeight int
}

// CompleteDeploymentResult commits the deployment, every accepted region, the
// terminal operation receipt, and its archive intent under one live claim.
// A failed write leaves every row in its preceding state for recovery.
func (db *DB) CompleteDeploymentResult(ctx context.Context, claim OperationClaim, d *model.Deployment, regions []DeploymentCompletionRegion, message string, metadata map[string]interface{}) error {
	if db == nil || db.Pool == nil || !claim.valid() || d == nil || d.ID == "" || d.App == "" || d.Status != model.StatusDeployed || len(regions) == 0 {
		return fmt.Errorf("deployment completion is invalid")
	}
	changes, err := json.Marshal(d.SourceChanges)
	if err != nil {
		return fmt.Errorf("encode deployment source changes: %w", err)
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	receipt, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode deployment receipt metadata: %w", err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := deploymentCompletionClaim(ctx, tx, claim, d.App, d.ID); err != nil {
		return err
	}
	var app string
	var status model.DeployStatus
	if err := tx.QueryRow(ctx, `SELECT app,status FROM deployments WHERE id=$1 FOR UPDATE`, d.ID).Scan(&app, &status); err != nil {
		return err
	}
	if app != d.App || status == model.StatusFailed || status == model.StatusDeployed {
		return fmt.Errorf("deployment completion target is unavailable")
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM deployment_regions WHERE deployment_id=$1`, d.ID).Scan(&count); err != nil {
		return err
	}
	if count != len(regions) {
		return fmt.Errorf("deployment completion region set does not match accepted intent")
	}
	seen := make(map[string]bool, len(regions))
	for _, region := range regions {
		if region.Region == "" || seen[region.Region] || region.ActiveWeight < 0 || region.ActiveWeight > 100 {
			return fmt.Errorf("deployment completion region is invalid")
		}
		seen[region.Region] = true
		result, err := tx.Exec(ctx, `UPDATE deployment_regions
			SET status='deployed', eval_id=CASE WHEN $3='' THEN eval_id ELSE $3 END,
			last_error='', active_weight=$4, updated_at=now()
			WHERE deployment_id=$1 AND region=$2 AND status IN ('queued','submitting','healthy')`, d.ID, region.Region, region.EvalID, region.ActiveWeight)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("deployment completion region %s is unavailable", region.Region)
		}
	}
	result, err := tx.Exec(ctx, `UPDATE deployments
		SET status='deployed',commit_sha=$2,image_tag=$3,environment=$4,source_kind=$5,
		source_ref=$6,source_dirty=$7,source_changes=$8,finished_at=$9,spec_digest=$10
		WHERE id=$1 AND app=$11`, d.ID, d.CommitSHA, d.ImageTag, d.Environment,
		d.SourceKind, d.SourceRef, d.SourceDirty, changes, time.Now(), d.SpecDigest, d.App)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("deployment completion target disappeared")
	}
	if err := deploymentCompletionClaim(ctx, tx, claim, d.App, d.ID); err != nil {
		return err
	}
	var finished int
	if err := tx.QueryRow(ctx, `WITH finished AS (
		UPDATE operations SET status='succeeded',message=$1,metadata=metadata || $2::jsonb,
		locked_by='',locked_until=NULL,updated_at=now(),finished_at=now()
		WHERE id=$3 AND app=$4 AND kind IN ('app.deploy','app.rollback')
		AND payload->>'deploymentId'=$5 AND status='running' AND locked_by=$6
		AND lock_generation=$7 AND locked_until > clock_timestamp()
		RETURNING id,saga_id,app
	), outbox AS (
		INSERT INTO evidence_archive_intents (id,subject_kind,subject_id,app,operation_id,sequence,state)
		SELECT 'ei-' || gen_random_uuid()::text,'saga',saga_id,app,id,1,'pending'
		FROM finished WHERE saga_id <> ''
		ON CONFLICT (subject_kind,subject_id,sequence) DO NOTHING
	)
	SELECT count(*) FROM finished`, message, receipt, claim.OperationID(), d.App, d.ID, claim.OwnerID(), claim.Generation()).Scan(&finished); err != nil {
		return err
	}
	if finished != 1 {
		return ErrOperationOwnershipLost
	}
	return tx.Commit(ctx)
}

func deploymentCompletionClaim(ctx context.Context, tx pgx.Tx, claim OperationClaim, app, deploymentID string) error {
	var held bool
	err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND app=$2 AND kind IN ('app.deploy','app.rollback')
		AND payload->>'deploymentId'=$5 AND status='running' AND locked_by=$3 AND lock_generation=$4
		AND locked_until > clock_timestamp() FOR UPDATE`,
		claim.OperationID(), app, claim.OwnerID(), claim.Generation(), deploymentID).Scan(&held)
	if err != nil || !held {
		return ErrOperationOwnershipLost
	}
	return nil
}
