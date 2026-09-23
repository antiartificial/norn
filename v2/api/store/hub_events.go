package store

import (
	"context"
	"encoding/json"
	"time"

	"norn/v2/api/hub"
)

func (db *DB) AppendHubEvent(ctx context.Context, event *hub.Event) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// BIGSERIAL values are allocated before commit. Without a transaction-wide
	// writer lock, ID N+1 can commit and advance stream cursors while ID N is
	// still invisible, permanently hiding N from replay. Every v3 writer locks
	// the resolved table OID before allocating an ID and holds it through commit;
	// unlike current_schema(), this remains identical when search_path begins
	// with another schema but resolves control_events from the same later schema.
	if _, err := tx.Exec(ctx, `/* hub-event-append-lock */
		SELECT pg_advisory_xact_lock(hashtext('norn.control-events.append'), 'control_events'::regclass::oid::integer)
	`); err != nil {
		return err
	}
	var eventID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO control_events (timestamp, type, app_id, payload)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, event.Timestamp, event.Type, event.AppID, payload).Scan(&eventID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	event.ID = eventID
	return nil
}

func (db *DB) HubEventBounds(ctx context.Context) (hub.EventBounds, error) {
	var bounds hub.EventBounds
	var oldest, latest *time.Time
	err := db.Pool.QueryRow(ctx, `
		SELECT COALESCE(MIN(id), 0), COALESCE(MAX(id), 0), COUNT(*)::bigint,
		       MIN(timestamp), MAX(timestamp)
		FROM control_events
	`).Scan(&bounds.OldestCursor, &bounds.LatestCursor, &bounds.RetainedEvents, &oldest, &latest)
	bounds.OldestTimestamp = oldest
	bounds.LatestTimestamp = latest
	return bounds, err
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
