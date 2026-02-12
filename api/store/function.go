package store

import (
	"context"
	"time"

	"norn/api/model"
)

func (db *DB) InsertFuncExecution(ctx context.Context, exec *model.FuncExecution) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx,
		`INSERT INTO func_executions (app, image_tag, status, started_at)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		exec.App, exec.ImageTag, exec.Status, exec.StartedAt,
	).Scan(&id)
	return id, err
}

func (db *DB) UpdateFuncExecution(ctx context.Context, id int64, status model.FuncExecStatus, exitCode int, output string, durationMs int64) error {
	now := time.Now()
	_, err := db.pool.Exec(ctx,
		`UPDATE func_executions SET status = $1, exit_code = $2, output = $3, duration_ms = $4, finished_at = $5 WHERE id = $6`,
		status, exitCode, output, durationMs, now, id,
	)
	return err
}

func (db *DB) ListFuncExecutions(ctx context.Context, app string, limit int) ([]model.FuncExecution, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.pool.Query(ctx,
		`SELECT id, app, image_tag, status, exit_code, output, duration_ms, started_at, finished_at
		 FROM func_executions WHERE app = $1 ORDER BY started_at DESC LIMIT $2`,
		app, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var execs []model.FuncExecution
	for rows.Next() {
		var e model.FuncExecution
		if err := rows.Scan(&e.ID, &e.App, &e.ImageTag, &e.Status, &e.ExitCode, &e.Output, &e.DurationMs, &e.StartedAt, &e.FinishedAt); err != nil {
			return nil, err
		}
		execs = append(execs, e)
	}
	return execs, nil
}
