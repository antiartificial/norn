package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

var ErrExternalDeploymentNonceConsumed = errors.New("external deployment nonce is consumed or unavailable")
var ErrExternalDeploymentIdempotencyConflict = errors.New("external deployment idempotency key conflicts")

// ExternalDeploymentNonce is deliberately metadata-only. Callers receive the
// opaque raw nonce once; the database keeps only a SHA-256 digest.
type ExternalDeploymentNonce struct {
	ID           string
	NonceSHA256  string
	App          string
	Environment  string
	CIRepository string
	CIRunID      string
	CIRunAttempt string
	ExpiresAt    time.Time
}

func (db *DB) IssueExternalDeploymentNonce(ctx context.Context, nonce ExternalDeploymentNonce) error {
	if db == nil || db.Pool == nil || nonce.ID == "" || nonce.NonceSHA256 == "" || nonce.App == "" || nonce.Environment == "" || nonce.CIRepository == "" || nonce.CIRunID == "" || nonce.CIRunAttempt == "" || nonce.ExpiresAt.IsZero() {
		return fmt.Errorf("external deployment nonce store is unavailable")
	}
	_, err := db.Pool.Exec(ctx, `INSERT INTO external_deployment_nonces (id, nonce_sha256, app, environment, ci_repository, ci_run_id, ci_run_attempt, expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, nonce.ID, nonce.NonceSHA256, nonce.App, nonce.Environment, nonce.CIRepository, nonce.CIRunID, nonce.CIRunAttempt, nonce.ExpiresAt)
	return err
}

// ConsumeExternalDeploymentNonce is the final compare-and-set before durable
// admission. A verifier may read live evidence before this call, but only one
// caller can consume the nonce and create the corresponding receipt.
func (db *DB) ConsumeExternalDeploymentNonce(ctx context.Context, nonce ExternalDeploymentNonce) (bool, error) {
	if db == nil || db.Pool == nil || nonce.ID == "" || nonce.NonceSHA256 == "" || nonce.App == "" || nonce.Environment == "" || nonce.CIRepository == "" || nonce.CIRunID == "" || nonce.CIRunAttempt == "" {
		return false, fmt.Errorf("external deployment nonce store is unavailable")
	}
	tag, err := db.Pool.Exec(ctx, `UPDATE external_deployment_nonces SET consumed_at=now() WHERE id=$1 AND nonce_sha256=$2 AND app=$3 AND environment=$4 AND ci_repository=$5 AND ci_run_id=$6 AND ci_run_attempt=$7 AND consumed_at IS NULL AND expires_at > now()`, nonce.ID, nonce.NonceSHA256, nonce.App, nonce.Environment, nonce.CIRepository, nonce.CIRunID, nonce.CIRunAttempt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ExternalDeploymentAdmission is the complete terminal write set for a
// verifier-approved direct Fleet workload. The raw nonce never reaches this
// type; only its SHA-256 binding is accepted.
type ExternalDeploymentAdmission struct {
	Nonce          ExternalDeploymentNonce
	Deployment     *model.Deployment
	Regions        []model.DeploymentRegion
	Operation      *model.Operation
	IdempotencyKey string
	RequestDigest  string
}

type ExternalDeploymentAdmissionResult struct {
	Operation *model.Operation
	Replayed  bool
}

// AdmitExternalDeployment atomically consumes the one-use nonce and writes a
// terminal deployment, its verified regional truth, and its terminal
// operation. A crash at any point rolls all of it back. Replays are exact: the
// same idempotency key and request digest return the original operation;
// another request never gets to consume the nonce after that operation exists.
func (db *DB) AdmitExternalDeployment(ctx context.Context, admission ExternalDeploymentAdmission) (*ExternalDeploymentAdmissionResult, error) {
	if db == nil || db.Pool == nil || admission.Deployment == nil || admission.Operation == nil || admission.IdempotencyKey == "" || admission.RequestDigest == "" || admission.Nonce.ID == "" || admission.Nonce.NonceSHA256 == "" || admission.Nonce.App == "" || admission.Nonce.Environment == "" || admission.Nonce.CIRepository == "" || admission.Nonce.CIRunID == "" || admission.Nonce.CIRunAttempt == "" || !admission.Operation.Status.Terminal() || admission.Deployment.FinishedAt == nil || admission.Operation.FinishedAt == nil || len(admission.Regions) == 0 {
		return nil, fmt.Errorf("external deployment admission store is unavailable")
	}
	payload, metadata, err := prepareOperation(admission.Operation)
	if err != nil {
		return nil, err
	}
	changes, err := json.Marshal(admission.Deployment.SourceChanges)
	if err != nil {
		return nil, fmt.Errorf("encode external deployment source changes: %w", err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Bound expiry cleanup prevents one nonce row per pilot run from becoming
	// permanent state. It is intentionally best-effort and cannot affect live
	// rows because it selects only expired entries.
	_, _ = tx.Exec(ctx, `DELETE FROM external_deployment_nonces WHERE ctid IN (SELECT ctid FROM external_deployment_nonces WHERE expires_at < now() AND consumed_at IS NOT NULL LIMIT 1000)`)

	var existingID, existingDigest string
	err = tx.QueryRow(ctx, `SELECT id, COALESCE(metadata->>'requestDigest','') FROM operations WHERE metadata->>'idempotencyKey'=$1 FOR KEY SHARE`, admission.IdempotencyKey).Scan(&existingID, &existingDigest)
	if err == nil {
		if existingDigest != admission.RequestDigest {
			return nil, ErrExternalDeploymentIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		op, err := db.GetOperation(ctx, existingID)
		if err != nil {
			return nil, err
		}
		return &ExternalDeploymentAdmissionResult{Operation: op, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	var nonceID string
	err = tx.QueryRow(ctx, `UPDATE external_deployment_nonces SET consumed_at=now()
		WHERE id=$1 AND nonce_sha256=$2 AND app=$3 AND environment=$4 AND ci_repository=$5 AND ci_run_id=$6 AND ci_run_attempt=$7 AND consumed_at IS NULL AND expires_at > now()
		RETURNING id`, admission.Nonce.ID, admission.Nonce.NonceSHA256, admission.Nonce.App, admission.Nonce.Environment, admission.Nonce.CIRepository, admission.Nonce.CIRunID, admission.Nonce.CIRunAttempt).Scan(&nonceID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Another matching request may have committed while this transaction was
		// waiting on the nonce row. In read-committed mode this fresh query sees
		// the durable terminal receipt and returns only an exact replay.
		var replayID, replayDigest string
		replayErr := tx.QueryRow(ctx, `SELECT id, COALESCE(metadata->>'requestDigest','') FROM operations WHERE metadata->>'idempotencyKey'=$1`, admission.IdempotencyKey).Scan(&replayID, &replayDigest)
		if replayErr == nil && replayDigest == admission.RequestDigest {
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			op, err := db.GetOperation(ctx, replayID)
			if err != nil {
				return nil, err
			}
			return &ExternalDeploymentAdmissionResult{Operation: op, Replayed: true}, nil
		}
		if replayErr == nil {
			return nil, ErrExternalDeploymentIdempotencyConflict
		}
		return nil, ErrExternalDeploymentNonceConsumed
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deployments (id, app, commit_sha, image_tag, environment, saga_id, status, source_kind, source_ref, source_dirty, source_changes, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, admission.Deployment.ID, admission.Deployment.App, admission.Deployment.CommitSHA, admission.Deployment.ImageTag, admission.Deployment.Environment, admission.Deployment.SagaID, admission.Deployment.Status, admission.Deployment.SourceKind, admission.Deployment.SourceRef, admission.Deployment.SourceDirty, changes, admission.Deployment.StartedAt, admission.Deployment.FinishedAt); err != nil {
		return nil, err
	}
	for _, region := range admission.Regions {
		if _, err = tx.Exec(ctx, `INSERT INTO deployment_regions (deployment_id, region, nomad_region, status, desired_weight, active_weight, eval_id, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,now())`, admission.Deployment.ID, region.Region, region.NomadRegion, region.Status, region.DesiredWeight, region.ActiveWeight, region.EvalID); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operations (id, kind, app, saga_id, ref, status, risk, source, message, payload, metadata, attempts, max_attempts, next_attempt_at, started_at, updated_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,now(),$17)`, admission.Operation.ID, admission.Operation.Kind, admission.Operation.App, admission.Operation.SagaID, admission.Operation.Ref, admission.Operation.Status, admission.Operation.Risk, admission.Operation.Source, admission.Operation.Message, payload, metadata, admission.Operation.Attempts, admission.Operation.MaxAttempts, admission.Operation.NextAttemptAt, admission.Operation.StartedAt, admission.Operation.FinishedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ExternalDeploymentAdmissionResult{Operation: admission.Operation}, nil
}
