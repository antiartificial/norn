package store

import (
	"context"
	"strings"
	"time"
)

type AccessObservation struct {
	App        string    `json:"app"`
	Process    string    `json:"process,omitempty"`
	Endpoint   string    `json:"endpoint,omitempty"`
	Source     string    `json:"source,omitempty"`
	ObservedAt time.Time `json:"observedAt,omitempty"`
	Count      int64     `json:"count,omitempty"`
	Status     int       `json:"status,omitempty"`
}

type AccessPatternRow struct {
	App          string
	Process      string
	Endpoint     string
	Source       string
	Hour         int
	Weekday      int
	Requests     int64
	Successes    int64
	ClientErrors int64
	ServerErrors int64
	FirstSeen    time.Time
	LastSeen     time.Time
}

func (db *DB) RecordAccessObservation(ctx context.Context, obs AccessObservation) error {
	obs = normalizeAccessObservation(obs)
	successes, clientErrors, serverErrors := statusBuckets(obs.Count, obs.Status)
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO access_observation_buckets (
			app, process, endpoint, source, bucket_start,
			requests, successes, client_errors, server_errors, first_seen, last_seen
		)
		VALUES ($1, $2, $3, $4, date_trunc('hour', $5::timestamptz),
		        $6, $7, $8, $9, $5, $5)
		ON CONFLICT (app, process, endpoint, source, bucket_start)
		DO UPDATE SET
			requests = access_observation_buckets.requests + EXCLUDED.requests,
			successes = access_observation_buckets.successes + EXCLUDED.successes,
			client_errors = access_observation_buckets.client_errors + EXCLUDED.client_errors,
			server_errors = access_observation_buckets.server_errors + EXCLUDED.server_errors,
			first_seen = LEAST(access_observation_buckets.first_seen, EXCLUDED.first_seen),
			last_seen = GREATEST(access_observation_buckets.last_seen, EXCLUDED.last_seen)
	`, obs.App, obs.Process, obs.Endpoint, obs.Source, obs.ObservedAt, obs.Count, successes, clientErrors, serverErrors)
	return err
}

func (db *DB) ReplaceAccessObservation(ctx context.Context, obs AccessObservation) error {
	obs = normalizeAccessObservation(obs)
	successes, clientErrors, serverErrors := statusBuckets(obs.Count, obs.Status)
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO access_observation_buckets (
			app, process, endpoint, source, bucket_start,
			requests, successes, client_errors, server_errors, first_seen, last_seen
		)
		VALUES ($1, $2, $3, $4, date_trunc('hour', $5::timestamptz),
		        $6, $7, $8, $9, $5, $5)
		ON CONFLICT (app, process, endpoint, source, bucket_start)
		DO UPDATE SET
			requests = EXCLUDED.requests,
			successes = EXCLUDED.successes,
			client_errors = EXCLUDED.client_errors,
			server_errors = EXCLUDED.server_errors,
			first_seen = EXCLUDED.first_seen,
			last_seen = EXCLUDED.last_seen
	`, obs.App, obs.Process, obs.Endpoint, obs.Source, obs.ObservedAt, obs.Count, successes, clientErrors, serverErrors)
	return err
}

func normalizeAccessObservation(obs AccessObservation) AccessObservation {
	obs.App = strings.TrimSpace(obs.App)
	obs.Process = strings.TrimSpace(obs.Process)
	obs.Endpoint = strings.TrimSpace(obs.Endpoint)
	obs.Source = strings.TrimSpace(obs.Source)
	if obs.Source == "" {
		obs.Source = "external"
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = time.Now().UTC()
	}
	obs.ObservedAt = obs.ObservedAt.UTC()
	if obs.Count <= 0 {
		obs.Count = 1
	}
	return obs
}

func (db *DB) ListAccessPatternRows(ctx context.Context, since time.Time) ([]AccessPatternRow, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT
			app,
			process,
			endpoint,
			source,
			EXTRACT(HOUR FROM bucket_start)::int AS hour,
			EXTRACT(DOW FROM bucket_start)::int AS weekday,
			SUM(requests)::bigint AS requests,
			SUM(successes)::bigint AS successes,
			SUM(client_errors)::bigint AS client_errors,
			SUM(server_errors)::bigint AS server_errors,
			MIN(first_seen) AS first_seen,
			MAX(last_seen) AS last_seen
		FROM access_observation_buckets
		WHERE bucket_start >= $1
		GROUP BY app, process, endpoint, source, hour, weekday
		ORDER BY app, process, requests DESC
	`, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AccessPatternRow
	for rows.Next() {
		var row AccessPatternRow
		if err := rows.Scan(
			&row.App,
			&row.Process,
			&row.Endpoint,
			&row.Source,
			&row.Hour,
			&row.Weekday,
			&row.Requests,
			&row.Successes,
			&row.ClientErrors,
			&row.ServerErrors,
			&row.FirstSeen,
			&row.LastSeen,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (db *DB) PruneAccessObservations(ctx context.Context, olderThan time.Time) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM access_observation_buckets WHERE bucket_start < $1`, olderThan.UTC())
	return err
}

func statusBuckets(count int64, status int) (successes, clientErrors, serverErrors int64) {
	switch {
	case status >= 500:
		return 0, 0, count
	case status >= 400:
		return 0, count, 0
	default:
		return count, 0, 0
	}
}
