package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

type WebhookFilter struct {
	Provider string
	Status   string
	App      string
	Limit    int
}

type WebhookMetric struct {
	Provider         string
	Status           string
	Count            int64
	LastReceivedUnix float64
}

func (db *DB) InsertWebhookDelivery(ctx context.Context, d *model.WebhookDelivery) error {
	if d.Metadata == nil {
		d.Metadata = map[string]interface{}{}
	}
	if d.Payload == nil {
		d.Payload = map[string]interface{}{}
	}
	if d.Status == "" {
		d.Status = "received"
	}
	if d.ReceivedAt.IsZero() {
		d.ReceivedAt = time.Now()
	}
	payload, _ := json.Marshal(d.Payload)
	metadata, _ := json.Marshal(d.Metadata)
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO webhook_deliveries (id, provider, event, delivery_id, repository, ref, branch, app, saga_id, status, reason, remote_addr, user_agent, payload, metadata, received_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, now())
	`, d.ID, d.Provider, d.Event, d.DeliveryID, d.Repository, d.Ref, d.Branch, d.App, d.SagaID, d.Status, d.Reason, d.RemoteAddr, d.UserAgent, payload, metadata, d.ReceivedAt)
	return err
}

func (db *DB) UpdateWebhookDelivery(ctx context.Context, d *model.WebhookDelivery) error {
	if d.Payload == nil {
		d.Payload = map[string]interface{}{}
	}
	payload, _ := json.Marshal(d.Payload)
	metadata, _ := json.Marshal(d.Metadata)
	_, err := db.Pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET event = $1, delivery_id = $2, repository = $3, ref = $4, branch = $5, app = $6, saga_id = $7,
		    status = $8, reason = $9, payload = payload || $10::jsonb, metadata = metadata || $11::jsonb, updated_at = now()
		WHERE id = $12
	`, d.Event, d.DeliveryID, d.Repository, d.Ref, d.Branch, d.App, d.SagaID, d.Status, d.Reason, payload, metadata, d.ID)
	return err
}

func (db *DB) GetWebhookDelivery(ctx context.Context, id string) (*model.WebhookDelivery, error) {
	var d model.WebhookDelivery
	var payload, metadata []byte
	err := db.Pool.QueryRow(ctx, `
		SELECT id, provider, event, delivery_id, repository, ref, branch, app, saga_id, status, reason, remote_addr, user_agent, payload, metadata, received_at, updated_at
		FROM webhook_deliveries
		WHERE id = $1
	`, id).Scan(&d.ID, &d.Provider, &d.Event, &d.DeliveryID, &d.Repository, &d.Ref, &d.Branch, &d.App, &d.SagaID, &d.Status, &d.Reason, &d.RemoteAddr, &d.UserAgent, &payload, &metadata, &d.ReceivedAt, &d.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(payload, &d.Payload)
	_ = json.Unmarshal(metadata, &d.Metadata)
	return &d, nil
}

func (db *DB) ListWebhookDeliveries(ctx context.Context, filter WebhookFilter) ([]model.WebhookDelivery, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	clauses := []string{}
	args := []interface{}{}
	add := func(clause string, value interface{}) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if filter.Provider != "" {
		add("provider = $%d", filter.Provider)
	}
	if filter.Status != "" {
		add("status = $%d", filter.Status)
	}
	if filter.App != "" {
		add("app = $%d", filter.App)
	}

	query := `SELECT id, provider, event, delivery_id, repository, ref, branch, app, saga_id, status, reason, remote_addr, user_agent, payload, metadata, received_at, updated_at FROM webhook_deliveries`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY received_at DESC LIMIT $%d", len(args))

	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.WebhookDelivery
	for rows.Next() {
		var d model.WebhookDelivery
		var payload, metadata []byte
		if err := rows.Scan(&d.ID, &d.Provider, &d.Event, &d.DeliveryID, &d.Repository, &d.Ref, &d.Branch, &d.App, &d.SagaID, &d.Status, &d.Reason, &d.RemoteAddr, &d.UserAgent, &payload, &metadata, &d.ReceivedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(payload, &d.Payload)
		_ = json.Unmarshal(metadata, &d.Metadata)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (db *DB) WebhookMetrics(ctx context.Context) ([]WebhookMetric, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT
			provider,
			status,
			COUNT(*)::bigint,
			COALESCE(EXTRACT(EPOCH FROM MAX(received_at)), 0)::float8
		FROM webhook_deliveries
		GROUP BY provider, status
		ORDER BY provider, status
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WebhookMetric
	for rows.Next() {
		var metric WebhookMetric
		if err := rows.Scan(&metric.Provider, &metric.Status, &metric.Count, &metric.LastReceivedUnix); err != nil {
			return nil, err
		}
		out = append(out, metric)
	}
	return out, rows.Err()
}
