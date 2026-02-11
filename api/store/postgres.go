package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
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

		CREATE TABLE IF NOT EXISTS health_checks (
			id          TEXT PRIMARY KEY,
			app         TEXT NOT NULL,
			healthy     BOOLEAN NOT NULL,
			response_ms INTEGER NOT NULL DEFAULT 0,
			checked_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_health_checks_app_time
			ON health_checks(app, checked_at DESC);

		CREATE TABLE IF NOT EXISTS forge_states (
			app         TEXT PRIMARY KEY,
			status      TEXT NOT NULL DEFAULT 'unforged',
			steps       JSONB NOT NULL DEFAULT '[]',
			resources   JSONB NOT NULL DEFAULT '{}',
			error       TEXT NOT NULL DEFAULT '',
			started_at  TIMESTAMPTZ,
			finished_at TIMESTAMPTZ
		);
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

// --- Forge State ---

func (db *DB) UpsertForgeState(ctx context.Context, state *model.ForgeState) error {
	stepsJSON, _ := json.Marshal(state.Steps)
	resJSON, _ := json.Marshal(state.Resources)
	_, err := db.pool.Exec(ctx,
		`INSERT INTO forge_states (app, status, steps, resources, error, started_at, finished_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (app) DO UPDATE SET
		   status = EXCLUDED.status,
		   steps = EXCLUDED.steps,
		   resources = EXCLUDED.resources,
		   error = EXCLUDED.error,
		   started_at = EXCLUDED.started_at,
		   finished_at = EXCLUDED.finished_at`,
		state.App, state.Status, stepsJSON, resJSON, state.Error, state.StartedAt, state.FinishedAt,
	)
	return err
}

func (db *DB) UpdateForgeState(ctx context.Context, app string, status model.ForgeStatus, steps []model.ForgeStepLog, resources model.ForgeResources, errMsg string) error {
	stepsJSON, _ := json.Marshal(steps)
	resJSON, _ := json.Marshal(resources)
	var finished *time.Time
	if status.IsTerminal() {
		now := time.Now()
		finished = &now
	}
	_, err := db.pool.Exec(ctx,
		`UPDATE forge_states SET status = $1, steps = $2, resources = $3, error = $4, finished_at = $5 WHERE app = $6`,
		status, stepsJSON, resJSON, errMsg, finished, app,
	)
	return err
}

func (db *DB) GetForgeState(ctx context.Context, app string) (*model.ForgeState, error) {
	var state model.ForgeState
	var stepsJSON, resJSON []byte
	err := db.pool.QueryRow(ctx,
		`SELECT app, status, steps, resources, error, started_at, finished_at
		 FROM forge_states WHERE app = $1`, app,
	).Scan(&state.App, &state.Status, &stepsJSON, &resJSON, &state.Error, &state.StartedAt, &state.FinishedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	json.Unmarshal(stepsJSON, &state.Steps)
	json.Unmarshal(resJSON, &state.Resources)
	return &state, nil
}

func (db *DB) RecoverInFlightForges(ctx context.Context) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE forge_states
		 SET status = 'forge_failed', error = 'norn restarted during operation', finished_at = now()
		 WHERE status IN ('forging', 'tearing_down')`,
	)
	return err
}

// --- Health Checks ---

func (db *DB) InsertHealthCheck(ctx context.Context, hc *model.HealthCheck) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO health_checks (id, app, healthy, response_ms, checked_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		hc.ID, hc.App, hc.Healthy, hc.ResponseMs, hc.CheckedAt,
	)
	return err
}

func (db *DB) ListHealthChecks(ctx context.Context, app string, since time.Time) ([]model.HealthCheck, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, app, healthy, response_ms, checked_at
		 FROM health_checks WHERE app = $1 AND checked_at >= $2
		 ORDER BY checked_at ASC`,
		app, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var checks []model.HealthCheck
	for rows.Next() {
		var hc model.HealthCheck
		if err := rows.Scan(&hc.ID, &hc.App, &hc.Healthy, &hc.ResponseMs, &hc.CheckedAt); err != nil {
			return nil, err
		}
		checks = append(checks, hc)
	}
	return checks, nil
}

func (db *DB) PruneHealthChecks(ctx context.Context) (int64, error) {
	tag, err := db.pool.Exec(ctx,
		`DELETE FROM health_checks WHERE checked_at < now() - interval '24 hours'`,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
