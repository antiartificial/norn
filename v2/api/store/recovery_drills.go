package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type RecoveryDrill struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Target      string            `json:"target,omitempty"`
	Status      string            `json:"status"`
	InitiatedBy string            `json:"initiatedBy"`
	Evidence    map[string]string `json:"evidence,omitempty"`
	StartedAt   time.Time         `json:"startedAt"`
	FinishedAt  *time.Time        `json:"finishedAt,omitempty"`
}

func (db *DB) InsertRecoveryDrill(ctx context.Context, drill *RecoveryDrill) error {
	evidence, err := json.Marshal(drill.Evidence)
	if err != nil {
		return err
	}
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO recovery_drills(id,kind,target,status,initiated_by,evidence,started_at)
		VALUES($1,$2,$3,'running',$4,$5,$6)
	`, drill.ID, drill.Kind, drill.Target, drill.InitiatedBy, evidence, drill.StartedAt)
	return err
}

func (db *DB) FinishRecoveryDrill(ctx context.Context, id, status string, evidence map[string]string, finishedAt time.Time) (*RecoveryDrill, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	row := db.Pool.QueryRow(ctx, `
		UPDATE recovery_drills SET status=$2,evidence=$3,finished_at=$4
		WHERE id=$1 AND status='running'
		RETURNING id,kind,target,status,initiated_by,evidence,started_at,finished_at
	`, id, status, encoded, finishedAt)
	return scanRecoveryDrill(row)
}

func (db *DB) GetRecoveryDrill(ctx context.Context, id string) (*RecoveryDrill, error) {
	return scanRecoveryDrill(db.Pool.QueryRow(ctx, `
		SELECT id,kind,target,status,initiated_by,evidence,started_at,finished_at FROM recovery_drills WHERE id=$1
	`, id))
}

func (db *DB) ListRecoveryDrills(ctx context.Context, limit int) ([]RecoveryDrill, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT id,kind,target,status,initiated_by,evidence,started_at,finished_at
		FROM recovery_drills ORDER BY started_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	drills := []RecoveryDrill{}
	for rows.Next() {
		drill, err := scanRecoveryDrill(rows)
		if err != nil {
			return nil, err
		}
		drills = append(drills, *drill)
	}
	return drills, rows.Err()
}

func (db *DB) LatestPassedRecoveryDrills(ctx context.Context) (map[string]time.Time, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT kind,max(finished_at) FROM recovery_drills
		WHERE status='passed' AND finished_at IS NOT NULL GROUP BY kind
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var kind string
		var finished time.Time
		if err := rows.Scan(&kind, &finished); err != nil {
			return nil, err
		}
		out[kind] = finished
	}
	return out, rows.Err()
}

func scanRecoveryDrill(row pgx.Row) (*RecoveryDrill, error) {
	var drill RecoveryDrill
	var evidence []byte
	if err := row.Scan(&drill.ID, &drill.Kind, &drill.Target, &drill.Status, &drill.InitiatedBy,
		&evidence, &drill.StartedAt, &drill.FinishedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(evidence, &drill.Evidence)
	return &drill, nil
}
