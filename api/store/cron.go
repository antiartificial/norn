package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/api/model"
)

func (db *DB) InsertCronExecution(ctx context.Context, exec *model.CronExecution) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx,
		`INSERT INTO cron_executions (app, image_tag, status, started_at)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		exec.App, exec.ImageTag, exec.Status, exec.StartedAt,
	).Scan(&id)
	return id, err
}

func (db *DB) UpdateCronExecution(ctx context.Context, id int64, status model.CronExecStatus, exitCode int, output string, durationMs int64) error {
	now := time.Now()
	_, err := db.pool.Exec(ctx,
		`UPDATE cron_executions
		 SET status = $1, exit_code = $2, output = $3, duration_ms = $4, finished_at = $5
		 WHERE id = $6`,
		status, exitCode, output, durationMs, now, id,
	)
	return err
}

func (db *DB) ListCronExecutions(ctx context.Context, app string, limit int) ([]model.CronExecution, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.pool.Query(ctx,
		`SELECT id, app, image_tag, status, exit_code, output, duration_ms, started_at, finished_at
		 FROM cron_executions WHERE app = $1 ORDER BY started_at DESC LIMIT $2`,
		app, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var execs []model.CronExecution
	for rows.Next() {
		var e model.CronExecution
		if err := rows.Scan(&e.ID, &e.App, &e.ImageTag, &e.Status, &e.ExitCode, &e.Output, &e.DurationMs, &e.StartedAt, &e.FinishedAt); err != nil {
			return nil, err
		}
		execs = append(execs, e)
	}
	return execs, nil
}

func (db *DB) UpsertCronState(ctx context.Context, app, schedule string, paused bool) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO cron_states (app, schedule, paused, updated_at)
		 VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (app) DO UPDATE SET
		   schedule = EXCLUDED.schedule,
		   paused = EXCLUDED.paused,
		   updated_at = NOW()`,
		app, schedule, paused,
	)
	return err
}

func (db *DB) GetCronState(ctx context.Context, app string) (*model.CronState, error) {
	var s model.CronState
	err := db.pool.QueryRow(ctx,
		`SELECT app, schedule, paused, next_run_at FROM cron_states WHERE app = $1`, app,
	).Scan(&s.App, &s.Schedule, &s.Paused, &s.NextRunAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func (db *DB) UpdateCronNextRun(ctx context.Context, app string, nextRunAt time.Time) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE cron_states SET next_run_at = $1, updated_at = NOW() WHERE app = $2`,
		nextRunAt, app,
	)
	return err
}

func (db *DB) PruneCronExecutions(ctx context.Context, olderThan time.Time) error {
	_, err := db.pool.Exec(ctx,
		`DELETE FROM cron_executions WHERE started_at < $1`,
		olderThan,
	)
	return err
}
