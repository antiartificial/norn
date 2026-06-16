package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"norn/v2/api/model"
)

type BeaconFilter struct {
	App      string
	Type     string
	Severity string
	Limit    int
	Offset   int
}

type BeaconMetric struct {
	Type             string
	Severity         string
	Count            int
	LastOccurredUnix float64
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
		       dedupe_key, occurred_at, acknowledged_at, acknowledged_by,
		       acknowledgement_note, snoozed_until, metadata
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
		event, err := scanBeaconEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

func (db *DB) GetBeaconEvent(ctx context.Context, id string) (*model.BeaconEvent, error) {
	row := db.Pool.QueryRow(ctx, `
		SELECT id, source, app, environment, type, severity, title, body,
		       dedupe_key, occurred_at, acknowledged_at, acknowledged_by,
		       acknowledgement_note, snoozed_until, metadata
		FROM beacon_events
		WHERE id = $1
	`, id)
	event, err := scanBeaconEvent(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, err
		}
		return nil, err
	}
	return &event, nil
}

func (db *DB) AcknowledgeBeaconEvent(ctx context.Context, id, by, note string) (*model.BeaconEvent, error) {
	_, err := db.Pool.Exec(ctx, `
		UPDATE beacon_events
		SET acknowledged_at = now(),
		    acknowledged_by = $2,
		    acknowledgement_note = $3,
		    snoozed_until = NULL
		WHERE id = $1
	`, id, by, note)
	if err != nil {
		return nil, err
	}
	return db.GetBeaconEvent(ctx, id)
}

func (db *DB) SnoozeBeaconEvent(ctx context.Context, id, by, note string, until time.Time) (*model.BeaconEvent, error) {
	_, err := db.Pool.Exec(ctx, `
		UPDATE beacon_events
		SET snoozed_until = $2,
		    acknowledged_by = $3,
		    acknowledgement_note = $4
		WHERE id = $1
	`, id, until, by, note)
	if err != nil {
		return nil, err
	}
	return db.GetBeaconEvent(ctx, id)
}

func (db *DB) OpenBeaconEvent(ctx context.Context, id string) (*model.BeaconEvent, error) {
	_, err := db.Pool.Exec(ctx, `
		UPDATE beacon_events
		SET acknowledged_at = NULL,
		    acknowledged_by = '',
		    acknowledgement_note = '',
		    snoozed_until = NULL
		WHERE id = $1
	`, id)
	if err != nil {
		return nil, err
	}
	return db.GetBeaconEvent(ctx, id)
}

func (db *DB) ListCorrelatedEvents(ctx context.Context, correlationKey string, limit int) ([]model.BeaconEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := db.Pool.Query(ctx, `
		SELECT id, source, app, environment, type, severity, title, body,
		       dedupe_key, occurred_at, acknowledged_at, acknowledged_by,
		       acknowledgement_note, snoozed_until, metadata
		FROM beacon_events
		WHERE metadata->>'correlationKey' = $1
		ORDER BY occurred_at ASC
		LIMIT $2
	`, correlationKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []model.BeaconEvent
	for rows.Next() {
		event, err := scanBeaconEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (db *DB) AutoAckCorrelatedEvents(ctx context.Context, correlationKey, resolvingEventID string) (int, error) {
	tag, err := db.Pool.Exec(ctx, `
		UPDATE beacon_events
		SET acknowledged_at = now(),
		    acknowledged_by = 'system',
		    acknowledgement_note = 'resolved by ' || $2,
		    snoozed_until = NULL
		WHERE metadata->>'correlationKey' = $1
		  AND severity IN ('warning', 'critical')
		  AND acknowledged_at IS NULL
		  AND id != $2
	`, correlationKey, resolvingEventID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (db *DB) PruneBeaconEvents(ctx context.Context, olderThan time.Time) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM beacon_events WHERE occurred_at < $1`, olderThan)
	return err
}

func (db *DB) BeaconMetrics(ctx context.Context) ([]BeaconMetric, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT type, severity, COUNT(*), EXTRACT(EPOCH FROM MAX(occurred_at))
		FROM beacon_events
		GROUP BY type, severity
		ORDER BY type, severity
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var metrics []BeaconMetric
	for rows.Next() {
		var metric BeaconMetric
		if err := rows.Scan(&metric.Type, &metric.Severity, &metric.Count, &metric.LastOccurredUnix); err != nil {
			return nil, err
		}
		metrics = append(metrics, metric)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return metrics, nil
}

type beaconScanner interface {
	Scan(dest ...interface{}) error
}

func scanBeaconEvent(row beaconScanner) (model.BeaconEvent, error) {
	var event model.BeaconEvent
	var metadata []byte
	var acknowledgedAt pgtype.Timestamptz
	var snoozedUntil pgtype.Timestamptz
	if err := row.Scan(
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
		&acknowledgedAt,
		&event.AcknowledgedBy,
		&event.AcknowledgementNote,
		&snoozedUntil,
		&metadata,
	); err != nil {
		return event, err
	}
	if acknowledgedAt.Valid {
		event.AcknowledgedAt = &acknowledgedAt.Time
	}
	if snoozedUntil.Valid {
		event.SnoozedUntil = &snoozedUntil.Time
	}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &event.Metadata)
	}
	event.State = beaconEventState(event)
	return event, nil
}

func beaconEventState(event model.BeaconEvent) string {
	now := time.Now()
	if event.SnoozedUntil != nil && event.SnoozedUntil.After(now) {
		return "snoozed"
	}
	if event.AcknowledgedAt != nil {
		return "acknowledged"
	}
	return "open"
}
