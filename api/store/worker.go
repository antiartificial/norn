package store

import (
	"context"
	"time"
)

type WorkerTask struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	App        string     `json:"app"`
	WorkerID   string     `json:"workerId"`
	Image      string     `json:"image"`
	Status     string     `json:"status"`
	ExitCode   int        `json:"exitCode"`
	Output     string     `json:"output"`
	DurationMs int64      `json:"durationMs"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

func (db *DB) InsertWorkerTask(ctx context.Context, t *WorkerTask) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO worker_tasks (id, type, app, worker_id, image, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.ID, t.Type, t.App, t.WorkerID, t.Image, t.Status, t.CreatedAt,
	)
	return err
}

func (db *DB) UpdateWorkerTask(ctx context.Context, id, status string, exitCode int, output string, durationMs int64) error {
	now := time.Now()
	_, err := db.pool.Exec(ctx,
		`UPDATE worker_tasks SET status = $1, exit_code = $2, output = $3, duration_ms = $4, finished_at = $5 WHERE id = $6`,
		status, exitCode, output, durationMs, now, id,
	)
	return err
}

func (db *DB) ListWorkerTasks(ctx context.Context, workerID string, limit int) ([]WorkerTask, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.pool.Query(ctx,
		`SELECT id, type, app, worker_id, image, status, exit_code, output, duration_ms, created_at, started_at, finished_at
		 FROM worker_tasks WHERE worker_id = $1 ORDER BY created_at DESC LIMIT $2`,
		workerID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []WorkerTask
	for rows.Next() {
		var t WorkerTask
		if err := rows.Scan(&t.ID, &t.Type, &t.App, &t.WorkerID, &t.Image, &t.Status, &t.ExitCode, &t.Output, &t.DurationMs, &t.CreatedAt, &t.StartedAt, &t.FinishedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}
