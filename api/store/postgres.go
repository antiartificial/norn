package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/api/model"
)

type DB struct {
	pool *pgxpool.Pool
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
	return &DB{pool: pool}, nil
}

func (db *DB) Close() {
	db.pool.Close()
}

func (db *DB) QueryRow(ctx context.Context, sql string, args ...interface{}) interface{ Scan(...interface{}) error } {
	return db.pool.QueryRow(ctx, sql, args...)
}

func Migrate(db *DB) error {
	ctx := context.Background()
	_, err := db.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS deployments (
			id          TEXT PRIMARY KEY,
			app         TEXT NOT NULL,
			commit_sha  TEXT NOT NULL,
			image_tag   TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'queued',
			steps       JSONB NOT NULL DEFAULT '[]',
			error       TEXT NOT NULL DEFAULT '',
			started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_deployments_app ON deployments(app);
		CREATE INDEX IF NOT EXISTS idx_deployments_status ON deployments(status);
	`)
	return err
}

func (db *DB) InsertDeployment(ctx context.Context, d *model.Deployment) error {
	steps, _ := json.Marshal(d.Steps)
	_, err := db.pool.Exec(ctx,
		`INSERT INTO deployments (id, app, commit_sha, image_tag, status, steps, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		d.ID, d.App, d.CommitSHA, d.ImageTag, d.Status, steps, d.StartedAt,
	)
	return err
}

func (db *DB) UpdateDeployment(ctx context.Context, id string, status model.DeployStatus, steps []model.StepLog, errMsg string) error {
	stepsJSON, _ := json.Marshal(steps)
	var finished *time.Time
	if status == model.StatusDeployed || status == model.StatusFailed || status == model.StatusRolledBack {
		now := time.Now()
		finished = &now
	}
	_, err := db.pool.Exec(ctx,
		`UPDATE deployments SET status = $1, steps = $2, error = $3, finished_at = $4 WHERE id = $5`,
		status, stepsJSON, errMsg, finished, id,
	)
	return err
}

func (db *DB) ListDeployments(ctx context.Context, app string, limit int) ([]model.Deployment, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, app, commit_sha, image_tag, status, steps, error, started_at, finished_at
		 FROM deployments WHERE app = $1 ORDER BY started_at DESC LIMIT $2`,
		app, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deployments []model.Deployment
	for rows.Next() {
		var d model.Deployment
		var stepsJSON []byte
		if err := rows.Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.Status, &stepsJSON, &d.Error, &d.StartedAt, &d.FinishedAt); err != nil {
			return nil, err
		}
		json.Unmarshal(stepsJSON, &d.Steps)
		deployments = append(deployments, d)
	}
	return deployments, nil
}

func (db *DB) RecoverInFlightDeployments(ctx context.Context) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE deployments
		 SET status = 'failed', error = 'norn restarted during deployment', finished_at = now()
		 WHERE status NOT IN ('deployed', 'failed', 'rolled_back')`,
	)
	return err
}
