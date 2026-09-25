package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"norn/v2/api/model"
)

// CompleteDeploymentReconciliation records an independently accepted repair
// while preserving the original failed operation. Its caller must have just
// verified the exact healthy image in Nomad under the app operation lock.
// Every mutable database row and the new terminal receipt commit together.
func (db *DB) CompleteDeploymentReconciliation(ctx context.Context, claim OperationClaim, candidate DeploymentReconciliationCandidate, observedAt time.Time) error {
	if db == nil || db.Pool == nil || !claim.valid() || observedAt.IsZero() || observedAt.After(time.Now().Add(time.Minute)) {
		return fmt.Errorf("deployment reconciliation observation is invalid")
	}
	source, d := candidate.Acceptance.Operation, candidate.Acceptance.Deployment
	if d == nil || d.ID == "" || d.App == "" || source.ID == "" || source.App != d.App ||
		!model.IsContentAddressedImage(candidate.ImageTag) || candidate.CommitSHA == "" || candidate.SourceKind == "" || candidate.SourceRef == "" ||
		d.SpecDigest == "" || len(candidate.Acceptance.Regions) == 0 {
		return ErrDeploymentReconciliationUnavailable
	}
	// Re-load checkpoint provenance before writing. The signed accepted branch
	// ref is not a resolved commit, and must never be presented as one.
	if source.Kind == "app.deploy" {
		build, err := db.LoadOperationCheckpoint(ctx, source.ID, CheckpointBuild)
		if err != nil {
			return err
		}
		if build != nil {
			resolved, err := db.LoadOperationCheckpoint(ctx, source.ID, CheckpointSource)
			if err != nil {
				return err
			}
			if resolved == nil {
				return ErrDeploymentReconciliationUnavailable
			}
			var b struct {
				ImageTag       string `json:"imageTag"`
				SourceIdentity string `json:"sourceIdentity"`
			}
			var s struct {
				SourceKind    string   `json:"sourceKind"`
				CommitSHA     string   `json:"commitSha"`
				SourceRef     string   `json:"sourceRef"`
				SourceDirty   bool     `json:"sourceDirty"`
				SourceChanges []string `json:"sourceChanges"`
				TreeDigest    string   `json:"treeDigest"`
			}
			if json.Unmarshal(build.Outputs, &b) != nil || json.Unmarshal(resolved.Outputs, &s) != nil ||
				b.SourceIdentity != checkpointDigest(resolved.Outputs) || b.ImageTag != candidate.ImageTag ||
				s.SourceKind != candidate.SourceKind || s.CommitSHA != candidate.CommitSHA || s.SourceRef != candidate.SourceRef ||
				s.SourceDirty != candidate.SourceDirty || !equalStrings(s.SourceChanges, candidate.SourceChanges) || s.TreeDigest == "" {
				return ErrDeploymentReconciliationUnavailable
			}
		} else if d.SourceKind == "" || d.CommitSHA != candidate.CommitSHA || d.SourceKind != candidate.SourceKind || d.SourceRef != candidate.SourceRef ||
			d.SourceDirty != candidate.SourceDirty || !equalStrings(d.SourceChanges, candidate.SourceChanges) {
			return ErrDeploymentReconciliationUnavailable
		}
	} else if source.Kind != "app.rollback" || d.CommitSHA != candidate.CommitSHA || d.SourceKind != candidate.SourceKind || d.SourceRef != candidate.SourceRef ||
		d.SourceDirty != candidate.SourceDirty || !equalStrings(d.SourceChanges, candidate.SourceChanges) {
		return ErrDeploymentReconciliationUnavailable
	}
	changes, err := json.Marshal(candidate.SourceChanges)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]interface{}{
		"sourceOperationId": source.ID, "deploymentId": d.ID, "imageTag": candidate.ImageTag,
		"specDigest": d.SpecDigest, "observedAt": observedAt.UTC().Format(time.RFC3339Nano),
		"commitSha": candidate.CommitSHA, "sourceKind": candidate.SourceKind, "sourceRef": candidate.SourceRef,
		"sourceDirty": candidate.SourceDirty, "sourceChanges": candidate.SourceChanges,
	})
	if err != nil {
		return err
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var held bool
	if err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind='app.deployment-reconcile' AND app=$2
		AND payload->>'sourceOperationId'=$3 AND payload->>'deploymentId'=$4
		AND payload->>'imageTag'=$5 AND payload->>'specDigest'=$6
		AND acceptance_required AND EXISTS(SELECT 1 FROM operation_acceptance_intents ai WHERE ai.operation_id=operations.id)
		AND status='running' AND locked_by=$7 AND lock_generation=$8 AND locked_until>clock_timestamp() FOR UPDATE`,
		claim.OperationID(), d.App, source.ID, d.ID, candidate.ImageTag, d.SpecDigest, claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return ErrOperationOwnershipLost
	}
	var sourceStatus model.OperationStatus
	var sourceError string
	var manual bool
	if err := tx.QueryRow(ctx, `SELECT status,last_error,COALESCE(metadata->>'manualRecoveryRequired'='true',false)
		FROM operations WHERE id=$1 AND app=$2 AND saga_id=$3 AND kind=$5
		AND payload->>'deploymentId'=$4 FOR UPDATE`, source.ID, d.App, d.SagaID, d.ID, source.Kind).
		Scan(&sourceStatus, &sourceError, &manual); err != nil {
		return ErrDeploymentReconciliationUnavailable
	}
	if sourceStatus != model.OperationFailed || sourceError != "operation executor lease expired" || !manual {
		return ErrDeploymentReconciliationUnavailable
	}
	step := "submit"
	if source.Kind == "app.rollback" {
		step = "resolve-secrets"
	}
	var submitted, archived bool
	if err := tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM deployment_steps WHERE deployment_id=$1 AND step=$2 AND kind='mutable' AND status IN ('running','complete')),
		EXISTS(SELECT 1 FROM evidence_archive_intents WHERE operation_id=$3 AND subject_kind='saga' AND subject_id=$4)`,
		d.ID, step, source.ID, source.SagaID).Scan(&submitted, &archived); err != nil {
		return err
	}
	if !submitted || !archived {
		return ErrDeploymentReconciliationUnavailable
	}
	var status model.DeployStatus
	var image, specDigest, environment string
	var startedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT status,image_tag,spec_digest,environment,started_at FROM deployments WHERE id=$1 AND app=$2 AND saga_id=$3 FOR UPDATE`,
		d.ID, d.App, d.SagaID).Scan(&status, &image, &specDigest, &environment, &startedAt); err != nil {
		return ErrDeploymentReconciliationUnavailable
	}
	if status != model.StatusFailed || specDigest != d.SpecDigest || environment != d.Environment || !startedAt.Equal(d.StartedAt) || (image != "" && image != candidate.ImageTag) {
		return ErrDeploymentReconciliationUnavailable
	}
	if source.Kind == "app.rollback" {
		var sourceValid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE id=$1 AND app=$2 AND environment=$3
			AND status='deployed' AND image_tag=$4 AND commit_sha=$5 AND spec_digest=$6)`,
			candidate.SourceRef, d.App, environment, candidate.ImageTag, candidate.CommitSHA, d.SpecDigest).Scan(&sourceValid); err != nil {
			return err
		}
		if !sourceValid {
			return ErrDeploymentReconciliationUnavailable
		}
	}
	var superseded bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE app=$1 AND environment=$2 AND id<>$3 AND started_at >= $4)`,
		d.App, environment, d.ID, startedAt).Scan(&superseded); err != nil {
		return err
	}
	if superseded {
		return ErrDeploymentReconciliationUnavailable
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM deployment_regions WHERE deployment_id=$1`, d.ID).Scan(&count); err != nil {
		return err
	}
	if count != len(candidate.Acceptance.Regions) {
		return ErrDeploymentReconciliationUnavailable
	}
	for _, region := range candidate.Acceptance.Regions {
		result, err := tx.Exec(ctx, `UPDATE deployment_regions SET status='deployed',active_weight=desired_weight,last_error='',updated_at=now()
			WHERE deployment_id=$1 AND region=$2 AND nomad_region=$3 AND desired_weight=$4
			AND status='failed' AND eval_id<>''`, d.ID, region.Name, region.NomadRegion, region.TrafficWeight)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrDeploymentReconciliationUnavailable
		}
	}
	result, err := tx.Exec(ctx, `UPDATE deployments SET status='deployed',image_tag=$2,commit_sha=$3,source_kind=$4,
		source_ref=$5,source_dirty=$6,source_changes=$7,finished_at=now()
		WHERE id=$1 AND status='failed'`, d.ID, candidate.ImageTag, candidate.CommitSHA, candidate.SourceKind,
		candidate.SourceRef, candidate.SourceDirty, changes)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrDeploymentReconciliationUnavailable
	}
	var finished int
	if err := tx.QueryRow(ctx, `WITH finished AS (
		UPDATE operations SET status='succeeded',message='deployment reconciled against live Nomad image',
		metadata=metadata || $2::jsonb,locked_by='',locked_until=NULL,updated_at=now(),finished_at=now()
		WHERE id=$1 AND kind='app.deployment-reconcile' AND status='running'
		AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp()
		RETURNING id,saga_id,app
	), outbox AS (
		INSERT INTO evidence_archive_intents (id,subject_kind,subject_id,app,operation_id,sequence,state)
		SELECT 'ei-' || gen_random_uuid()::text,'saga',saga_id,app,id,1,'pending' FROM finished WHERE saga_id<>''
		ON CONFLICT (subject_kind,subject_id,sequence) DO NOTHING
	)
	SELECT count(*) FROM finished`, claim.OperationID(), metadata, claim.OwnerID(), claim.Generation()).Scan(&finished); err != nil {
		return err
	}
	if finished != 1 {
		return ErrOperationOwnershipLost
	}
	return tx.Commit(ctx)
}
