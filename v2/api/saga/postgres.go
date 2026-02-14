package saga

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) Append(ctx context.Context, evt *Event) error {
	meta, _ := json.Marshal(evt.Metadata)
	if meta == nil {
		meta = []byte("{}")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO saga_events (id, saga_id, timestamp, source, app, category, action, message, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		evt.ID, evt.SagaID, evt.Timestamp, evt.Source, evt.App, evt.Category, evt.Action, evt.Message, meta,
	)
	return err
}

func (s *PostgresStore) ListBySaga(ctx context.Context, sagaID string) ([]Event, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, saga_id, timestamp, source, app, category, action, message, metadata
		 FROM saga_events WHERE saga_id = $1 ORDER BY timestamp ASC`, sagaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (s *PostgresStore) ListByApp(ctx context.Context, app string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, saga_id, timestamp, source, app, category, action, message, metadata
		 FROM saga_events WHERE app = $1 ORDER BY timestamp DESC LIMIT $2`, app, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (s *PostgresStore) ListRecent(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, saga_id, timestamp, source, app, category, action, message, metadata
		 FROM saga_events ORDER BY timestamp DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

type scannable interface {
	Next() bool
	Scan(dest ...interface{}) error
}

func scanEvents(rows scannable) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var evt Event
		var meta []byte
		if err := rows.Scan(&evt.ID, &evt.SagaID, &evt.Timestamp, &evt.Source, &evt.App, &evt.Category, &evt.Action, &evt.Message, &meta); err != nil {
			return nil, err
		}
		if len(meta) > 0 {
			json.Unmarshal(meta, &evt.Metadata)
		}
		events = append(events, evt)
	}
	return events, nil
}
