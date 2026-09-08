package store

import (
	"context"
	"fmt"
	"time"
)

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
