package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"norn/v2/api/model"
)

type BeaconFilter struct {
	App      string
	Type     string
	Severity string
	Limit    int
	Offset   int
}

func (db *DB) InsertBeaconEvent(ctx context.Context, event *model.BeaconEvent) error {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	if event.Metadata == nil {
		metadata = []byte("{}")
	}

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO beacon_events (
			id, source, app, environment, type, severity, title, body,
			dedupe_key, occurred_at, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, event.ID, event.Source, event.App, event.Environment, event.Type, event.Severity,
		event.Title, event.Body, event.DedupeKey, event.OccurredAt, metadata)
	return err
}

func (db *DB) ListBeaconEvents(ctx context.Context, filter BeaconFilter) ([]model.BeaconEvent, int, error) {
	where := ""
	args := []interface{}{}
	argN := 1

	if filter.App != "" {
		where += fmt.Sprintf(" AND app = $%d", argN)
		args = append(args, filter.App)
		argN++
	}
	if filter.Type != "" {
		where += fmt.Sprintf(" AND type = $%d", argN)
		args = append(args, filter.Type)
		argN++
	}
	if filter.Severity != "" {
		where += fmt.Sprintf(" AND severity = $%d", argN)
		args = append(args, filter.Severity)
		argN++
	}

	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var total int
	countSQL := "SELECT COUNT(*) FROM beacon_events WHERE 1=1" + where
	if err := db.Pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	querySQL := fmt.Sprintf(`
		SELECT id, source, app, environment, type, severity, title, body,
		       dedupe_key, occurred_at, metadata
		FROM beacon_events
		WHERE 1=1%s
		ORDER BY occurred_at DESC
		LIMIT $%d OFFSET $%d
	`, where, argN, argN+1)
	args = append(args, limit, filter.Offset)

	rows, err := db.Pool.Query(ctx, querySQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var events []model.BeaconEvent
	for rows.Next() {
		var event model.BeaconEvent
		var metadata []byte
		if err := rows.Scan(
			&event.ID,
			&event.Source,
			&event.App,
			&event.Environment,
			&event.Type,
			&event.Severity,
			&event.Title,
			&event.Body,
			&event.DedupeKey,
			&event.OccurredAt,
			&metadata,
		); err != nil {
			return nil, 0, err
		}
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &event.Metadata)
		}
		events = append(events, event)
	}
	return events, total, nil
}

func (db *DB) PruneBeaconEvents(ctx context.Context, olderThan time.Time) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM beacon_events WHERE occurred_at < $1`, olderThan)
	return err
}
