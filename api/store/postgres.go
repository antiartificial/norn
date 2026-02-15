package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

		CREATE TABLE IF NOT EXISTS cron_executions (
			id          BIGSERIAL PRIMARY KEY,
			app         TEXT NOT NULL,
			image_tag   TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'running',
			exit_code   INT NOT NULL DEFAULT -1,
			output      TEXT NOT NULL DEFAULT '',
			duration_ms BIGINT NOT NULL DEFAULT 0,
			started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			finished_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_cron_exec_app ON cron_executions(app, started_at DESC);

		CREATE TABLE IF NOT EXISTS cron_states (
			app         TEXT PRIMARY KEY,
			schedule    TEXT NOT NULL DEFAULT '',
			paused      BOOLEAN NOT NULL DEFAULT FALSE,
			next_run_at TIMESTAMPTZ,
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE TABLE IF NOT EXISTS cluster_nodes (
			id           TEXT PRIMARY KEY,
			name         TEXT NOT NULL,
			provider     TEXT NOT NULL,
			region       TEXT NOT NULL DEFAULT '',
			size         TEXT NOT NULL DEFAULT '',
			role         TEXT NOT NULL,
			public_ip    TEXT NOT NULL DEFAULT '',
			tailscale_ip TEXT NOT NULL DEFAULT '',
			status       TEXT NOT NULL DEFAULT 'provisioning',
			provider_id  TEXT NOT NULL DEFAULT '',
			error        TEXT NOT NULL DEFAULT '',
			created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		);

		CREATE TABLE IF NOT EXISTS func_executions (
			id          BIGSERIAL PRIMARY KEY,
			app         TEXT NOT NULL,
			image_tag   TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'running',
			exit_code   INT NOT NULL DEFAULT -1,
			output      TEXT NOT NULL DEFAULT '',
			duration_ms BIGINT NOT NULL DEFAULT 0,
			started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			finished_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_func_exec_app ON func_executions(app, started_at DESC);

		CREATE TABLE IF NOT EXISTS worker_tasks (
			id          TEXT PRIMARY KEY,
			type        TEXT NOT NULL,
			app         TEXT NOT NULL,
			worker_id   TEXT NOT NULL,
			image       TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'dispatched',
			exit_code   INTEGER NOT NULL DEFAULT -1,
			output      TEXT NOT NULL DEFAULT '',
			duration_ms BIGINT NOT NULL DEFAULT 0,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			started_at  TIMESTAMPTZ,
			finished_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_worker_tasks_worker ON worker_tasks(worker_id, created_at DESC);

		ALTER TABLE cluster_nodes ADD COLUMN IF NOT EXISTS capabilities TEXT[] DEFAULT '{}';
		ALTER TABLE cluster_nodes ADD COLUMN IF NOT EXISTS last_heartbeat TIMESTAMPTZ;
		ALTER TABLE cluster_nodes ADD COLUMN IF NOT EXISTS tasks_active INTEGER DEFAULT 0;
		ALTER TABLE cluster_nodes ADD COLUMN IF NOT EXISTS max_concurrent INTEGER DEFAULT 4;
		ALTER TABLE cluster_nodes ADD COLUMN IF NOT EXISTS public_url TEXT DEFAULT '';
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

type DeploymentFilter struct {
	App    string
	Status string
	Limit  int
	Offset int
}

func (db *DB) ListAllDeployments(ctx context.Context, f DeploymentFilter) ([]model.Deployment, int, error) {
	where := ""
	args := []interface{}{}
	argN := 1

	if f.App != "" {
		where += fmt.Sprintf(" AND app = $%d", argN)
		args = append(args, f.App)
		argN++
	}
	if f.Status != "" {
		where += fmt.Sprintf(" AND status = $%d", argN)
		args = append(args, f.Status)
		argN++
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var total int
	countSQL := "SELECT COUNT(*) FROM deployments WHERE 1=1" + where
	if err := db.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	querySQL := fmt.Sprintf(
		"SELECT id, app, commit_sha, image_tag, status, steps, error, started_at, finished_at FROM deployments WHERE 1=1%s ORDER BY started_at DESC LIMIT $%d OFFSET $%d",
		where, argN, argN+1,
	)
	args = append(args, limit, f.Offset)

	rows, err := db.pool.Query(ctx, querySQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var deployments []model.Deployment
	for rows.Next() {
		var d model.Deployment
		var stepsJSON []byte
		if err := rows.Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.Status, &stepsJSON, &d.Error, &d.StartedAt, &d.FinishedAt); err != nil {
			return nil, 0, err
		}
		json.Unmarshal(stepsJSON, &d.Steps)
		deployments = append(deployments, d)
	}
	return deployments, total, nil
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

// --- Daily Stats ---

type DailyStats struct {
	TotalBuilds    int    `json:"totalBuilds"`
	TotalDeploys   int    `json:"totalDeploys"`
	TotalFailures  int    `json:"totalFailures"`
	MostPopularApp string `json:"mostPopularApp"`
	MostPopularN   int    `json:"mostPopularN"`
}

func (db *DB) GetDailyStats(ctx context.Context) (*DailyStats, error) {
	s := &DailyStats{}

	row := db.pool.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status = 'deployed'),
			COUNT(*) FILTER (WHERE status = 'failed')
		FROM deployments
		WHERE started_at >= CURRENT_DATE
	`)
	if err := row.Scan(&s.TotalBuilds, &s.TotalDeploys, &s.TotalFailures); err != nil {
		return nil, err
	}

	var app *string
	var n *int
	err := db.pool.QueryRow(ctx, `
		SELECT app, COUNT(*) AS n
		FROM deployments
		WHERE started_at >= CURRENT_DATE
		GROUP BY app
		ORDER BY n DESC
		LIMIT 1
	`).Scan(&app, &n)
	if err == nil && app != nil {
		s.MostPopularApp = *app
		s.MostPopularN = *n
	}

	return s, nil
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
