package store

import (
	"context"
	"encoding/json"

	"norn/v2/api/model"
)

// LatestSuccessfulPromotedDeployment returns an immutable production rollback
// target. A successful deployment alone is insufficient: it must be in the
// exact lane and have durable, signed staging-qualification evidence recorded
// by the successful promotion operation.
func (db *DB) LatestSuccessfulPromotedDeployment(ctx context.Context, app, environment, excludeID string) (*model.Deployment, error) {
	var d model.Deployment
	var changes []byte
	err := db.Pool.QueryRow(ctx, latestSuccessfulPromotedDeploymentQuery(), app, environment, excludeID).Scan(
		&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.SpecDigest, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt,
	)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(changes, &d.SourceChanges)
	d.Regions, _ = db.DeploymentRegions(ctx, d.ID)
	return &d, nil
}

func latestSuccessfulPromotedDeploymentQuery() string {
	return `SELECT d.id, d.app, d.commit_sha, d.image_tag, d.spec_digest, d.environment, d.saga_id, d.status, d.source_kind, d.source_ref, d.source_dirty, d.source_changes, d.started_at, d.finished_at
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
