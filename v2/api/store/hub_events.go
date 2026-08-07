package store

import (
	"context"
	"encoding/json"

	"norn/v2/api/hub"
)

func (db *DB) AppendHubEvent(ctx context.Context, event *hub.Event) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	return db.Pool.QueryRow(ctx, `
		INSERT INTO control_events (timestamp, type, app_id, payload)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, event.Timestamp, event.Type, event.AppID, payload).Scan(&event.ID)
}

func (db *DB) ListHubEventsAfter(ctx context.Context, after int64, limit int) ([]hub.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT id, timestamp, type, app_id, payload
		FROM control_events
		WHERE id > $1
		ORDER BY id ASC
		LIMIT $2
	`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []hub.Event{}
	for rows.Next() {
		var event hub.Event
		var payload []byte
		if err := rows.Scan(&event.ID, &event.Timestamp, &event.Type, &event.AppID, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &event.Payload); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (db *DB) LatestHubEventID(ctx context.Context) (int64, error) {
	var id int64
	err := db.Pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM control_events`).Scan(&id)
	return id, err
}
