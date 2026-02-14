package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/model"
)

type DB struct {
	Pool *pgxpool.Pool
}

func Connect(databaseURL string) (*DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	return &DB{Pool: pool}, nil
}

func (db *DB) Close() {
	db.Pool.Close()
}

func Migrate(db *DB) error {
	ctx := context.Background()
	_, err := db.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS saga_events (
			id         TEXT PRIMARY KEY,
			saga_id    TEXT NOT NULL,
			timestamp  TIMESTAMPTZ NOT NULL DEFAULT now(),
			source     TEXT NOT NULL DEFAULT '',
			app        TEXT NOT NULL DEFAULT '',
			category   TEXT NOT NULL DEFAULT '',
			action     TEXT NOT NULL DEFAULT '',
			message    TEXT NOT NULL DEFAULT '',
			metadata   JSONB NOT NULL DEFAULT '{}'
		);
		CREATE INDEX IF NOT EXISTS idx_saga_saga_id ON saga_events(saga_id, timestamp);
		CREATE INDEX IF NOT EXISTS idx_saga_app ON saga_events(app, timestamp DESC);

		CREATE TABLE IF NOT EXISTS deployments (
			id          TEXT PRIMARY KEY,
			app         TEXT NOT NULL,
			commit_sha  TEXT NOT NULL,
			image_tag   TEXT NOT NULL,
			saga_id     TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'running',
			started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_deployments_app ON deployments(app, started_at DESC);
	`)
	return err
}

func (db *DB) InsertDeployment(ctx context.Context, d *model.Deployment) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO deployments (id, app, commit_sha, image_tag, saga_id, status, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		d.ID, d.App, d.CommitSHA, d.ImageTag, d.SagaID, d.Status, d.StartedAt,
	)
	return err
}

func (db *DB) UpdateDeployment(ctx context.Context, id string, status model.DeployStatus) error {
	var finished *time.Time
	if status == model.StatusDeployed || status == model.StatusFailed {
		now := time.Now()
		finished = &now
	}
	_, err := db.Pool.Exec(ctx,
		`UPDATE deployments SET status = $1, finished_at = $2 WHERE id = $3`,
		status, finished, id,
	)
	return err
}

func (db *DB) ListDeployments(ctx context.Context, app string, limit int) ([]model.Deployment, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `SELECT id, app, commit_sha, image_tag, saga_id, status, started_at, finished_at
		 FROM deployments`
	args := []interface{}{}
	if app != "" {
		query += " WHERE app = $1 ORDER BY started_at DESC LIMIT $2"
		args = append(args, app, limit)
	} else {
		query += " ORDER BY started_at DESC LIMIT $1"
		args = append(args, limit)
	}

	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deployments []model.Deployment
	for rows.Next() {
		var d model.Deployment
		if err := rows.Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.SagaID, &d.Status, &d.StartedAt, &d.FinishedAt); err != nil {
			return nil, err
		}
		deployments = append(deployments, d)
	}
	return deployments, nil
}

func (db *DB) LastSuccessfulDeployment(ctx context.Context, app, excludeID string) (*model.Deployment, error) {
	var d model.Deployment
	err := db.Pool.QueryRow(ctx,
		`SELECT id, app, commit_sha, image_tag, saga_id, status, started_at, finished_at
		 FROM deployments
		 WHERE app = $1 AND status = 'deployed' AND id != $2
		 ORDER BY started_at DESC LIMIT 1`,
		app, excludeID,
	).Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.SagaID, &d.Status, &d.StartedAt, &d.FinishedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (db *DB) RecoverInFlightDeployments(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE deployments SET status = 'failed', finished_at = now()
		 WHERE status NOT IN ('deployed', 'failed')`,
	)
	return err
}

// Healthy checks the database connection.
func (db *DB) Healthy(ctx context.Context) error {
	var n int
	return db.Pool.QueryRow(ctx, "SELECT 1").Scan(&n)
}

// DailyStats returns basic deployment statistics for today.
type DailyStats struct {
	Total   int `json:"total"`
	Success int `json:"success"`
	Failed  int `json:"failed"`
}

func (db *DB) GetDailyStats(ctx context.Context) (*DailyStats, error) {
	s := &DailyStats{}
	err := db.Pool.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status = 'deployed'),
			COUNT(*) FILTER (WHERE status = 'failed')
		FROM deployments
		WHERE started_at >= CURRENT_DATE
	`).Scan(&s.Total, &s.Success, &s.Failed)
	if err != nil {
		return nil, fmt.Errorf("daily stats: %w", err)
	}
	return s, nil
}
