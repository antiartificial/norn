package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"norn/v2/api/model"
)

var ErrGitHubActionsAssertionConsumed = errors.New("GitHub Actions assertion already consumed")

func (db *DB) ConsumeGitHubActionsAssertion(ctx context.Context, issuer, jti string, expiresAt time.Time) error {
	if db == nil || db.Pool == nil || issuer == "" || jti == "" || expiresAt.IsZero() {
		return fmt.Errorf("GitHub Actions assertion store is unavailable")
	}
	// Replay records are only useful until the issuer's assertion expires.
	// Opportunistic bounded cleanup avoids retaining one row per CI run forever.
	_, _ = db.Pool.Exec(ctx, `DELETE FROM github_actions_assertion_uses WHERE ctid IN (SELECT ctid FROM github_actions_assertion_uses WHERE expires_at < now() LIMIT 1000)`)
	tag, err := db.Pool.Exec(ctx, `INSERT INTO github_actions_assertion_uses (issuer, jti, expires_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, issuer, jti, expiresAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrGitHubActionsAssertionConsumed
	}
	return nil
}

// InsertDeploymentOperation preserves the deployment and operation receipt in
// one transaction, so a promotion cannot create an orphaned target.
func (db *DB) InsertDeploymentOperation(ctx context.Context, deployment *model.Deployment, regions []model.ResolvedRegion, op *model.Operation) error {
	if db == nil || db.Pool == nil || deployment == nil || op == nil {
		return fmt.Errorf("deployment operation store is unavailable")
	}
	payload, metadata, err := prepareOperation(op)
	if err != nil {
		return err
	}
	changes, err := json.Marshal(deployment.SourceChanges)
	if err != nil {
		return err
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO deployments (id, app, commit_sha, image_tag, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, deployment.ID, deployment.App, deployment.CommitSHA, deployment.ImageTag, deployment.Environment, deployment.SagaID, deployment.Status, deployment.SourceKind, deployment.SourceRef, deployment.SourceDirty, changes, deployment.StartedAt); err != nil {
		return err
	}
	for _, region := range regions {
		if _, err = tx.Exec(ctx, `INSERT INTO deployment_regions (deployment_id, region, nomad_region, status, desired_weight, active_weight) VALUES ($1,$2,$3,$4,$5,0)`, deployment.ID, region.Name, region.NomadRegion, model.StatusQueued, region.TrafficWeight); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operations (id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,now())`, op.ID, op.Kind, op.App, op.SagaID, op.Ref, op.Status, op.Risk, op.Source, op.Message, payload, metadata, op.Attempts, op.MaxAttempts, op.NextAttemptAt, op.StartedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (db *DB) GetReleaseOperationByDeploymentID(ctx context.Context, deploymentID string) (*model.Operation, error) {
	var id string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM operations WHERE payload->>'deploymentId'=$1 AND kind='app.deploy' ORDER BY started_at DESC LIMIT 1`, deploymentID).Scan(&id); err != nil {
		return nil, err
	}
	return db.GetOperation(ctx, id)
}
func (db *DB) GetPromotionOperationByDeploymentID(ctx context.Context, deploymentID string) (*model.Operation, error) {
	var id string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM operations WHERE payload->>'deploymentId'=$1 AND kind='app.deploy' AND status='succeeded' AND metadata ? 'promotionQualification' LIMIT 1`, deploymentID).Scan(&id); err != nil {
		return nil, err
	}
	return db.GetOperation(ctx, id)
}
func (db *DB) GetOperationByPromotionQualificationID(ctx context.Context, qualificationID string) (*model.Operation, error) {
	var id string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM operations WHERE kind='app.deploy' AND metadata->'promotionQualification'->>'id'=$1`, qualificationID).Scan(&id); err != nil {
		return nil, err
	}
	return db.GetOperation(ctx, id)
}
func (db *DB) LatestSuccessfulDeployment(ctx context.Context, app, environment string) (*model.Deployment, error) {
	var d model.Deployment
	var changes []byte
	err := db.Pool.QueryRow(ctx, `SELECT id, app, commit_sha, image_tag, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at, finished_at FROM deployments WHERE app=$1 AND environment=$2 AND status='deployed' ORDER BY started_at DESC LIMIT 1`, app, environment).Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(changes, &d.SourceChanges)
	d.Regions, _ = db.DeploymentRegions(ctx, d.ID)
	return &d, nil
}

// LatestSuccessfulPromotedDeployment returns an immutable production rollback
// target. A successful deployment alone is insufficient: it must be in the
// exact lane and have durable, signed staging-qualification evidence recorded
// by the successful promotion operation.
func (db *DB) LatestSuccessfulPromotedDeployment(ctx context.Context, app, environment, excludeID string) (*model.Deployment, error) {
	var d model.Deployment
	var changes []byte
	err := db.Pool.QueryRow(ctx, latestSuccessfulPromotedDeploymentQuery(), app, environment, excludeID).Scan(
		&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt,
	)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(changes, &d.SourceChanges)
	d.Regions, _ = db.DeploymentRegions(ctx, d.ID)
	return &d, nil
}

func latestSuccessfulPromotedDeploymentQuery() string {
	return `SELECT d.id, d.app, d.commit_sha, d.image_tag, d.environment, d.saga_id, d.status, d.source_kind, d.source_ref, d.source_dirty, d.source_changes, d.started_at, d.finished_at
		FROM deployments d
		WHERE d.app=$1 AND d.environment=$2 AND d.status='deployed' AND d.id != $3
		  AND EXISTS (
			SELECT 1 FROM operations o
			WHERE o.kind='app.deploy' AND o.status='succeeded'
			  AND o.payload->>'deploymentId'=d.id
			  AND o.metadata ? 'promotionQualification'
			  AND o.metadata->'promotionQualification'->>'schemaVersion'='norn.release-qualification/v2'
			  AND COALESCE(o.metadata->'promotionQualification'->>'signature', '') <> ''
		  )
		ORDER BY d.started_at DESC LIMIT 1`
}
