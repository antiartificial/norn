package store

import (
	"context"
	"encoding/json"
	"time"

	"norn/v2/api/model"
)

func (db *DB) StartDeploymentStep(ctx context.Context, step model.DeploymentStep) error {
	if step.Metadata == nil {
		step.Metadata = map[string]interface{}{}
	}
	if step.StartedAt.IsZero() {
		step.StartedAt = time.Now()
	}
	if step.Status == "" {
		step.Status = model.DeploymentStepRunning
	}
	metadata, _ := json.Marshal(step.Metadata)
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO deployment_steps (deployment_id, app, saga_id, step, status, kind, attempt, started_at, duration_ms, message, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0, $9, $10)
		ON CONFLICT (deployment_id, step) DO UPDATE
		SET status = EXCLUDED.status,
		    kind = EXCLUDED.kind,
		    attempt = EXCLUDED.attempt,
		    started_at = EXCLUDED.started_at,
		    finished_at = NULL,
		    duration_ms = 0,
		    message = EXCLUDED.message,
		    metadata = deployment_steps.metadata || EXCLUDED.metadata,
		    app = EXCLUDED.app,
		    saga_id = EXCLUDED.saga_id
	`, step.DeploymentID, step.App, step.SagaID, step.Step, step.Status, step.Kind, step.Attempt, step.StartedAt, step.Message, metadata)
	return err
}

func (db *DB) FinishDeploymentStep(ctx context.Context, deploymentID, step string, status model.DeploymentStepStatus, durationMs int64, message string, metadata map[string]interface{}) error {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	data, _ := json.Marshal(metadata)
	_, err := db.Pool.Exec(ctx, `
		UPDATE deployment_steps
		SET status = $1,
		    finished_at = now(),
		    duration_ms = $2,
		    message = $3,
		    metadata = metadata || $4::jsonb
		WHERE deployment_id = $5 AND step = $6
	`, status, durationMs, message, data, deploymentID, step)
	return err
}

func (db *DB) ListDeploymentSteps(ctx context.Context, deploymentID string) ([]model.DeploymentStep, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT deployment_id, app, saga_id, step, status, kind, attempt, started_at, finished_at, duration_ms, message, metadata
		FROM deployment_steps
		WHERE deployment_id = $1
		ORDER BY started_at ASC
	`, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.DeploymentStep
	for rows.Next() {
		var step model.DeploymentStep
		var metadata []byte
		if err := rows.Scan(&step.DeploymentID, &step.App, &step.SagaID, &step.Step, &step.Status, &step.Kind, &step.Attempt, &step.StartedAt, &step.FinishedAt, &step.DurationMs, &step.Message, &metadata); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metadata, &step.Metadata)
		out = append(out, step)
	}
	return out, rows.Err()
}
