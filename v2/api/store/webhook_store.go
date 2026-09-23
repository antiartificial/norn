package store

import (
	"context"

	"norn/v2/api/model"
)

// WebhookStore is the control boundary for inbound webhook delivery records:
// their identity, deduplication and pending dispatch/retry state, plus the
// merge-on-update of payload and metadata. It is a cleanly isolated single-table
// concern — a backend-neutral seam for Norn v3 (roadmap M3 / P5). GetWebhookDelivery
// returns (nil, nil) for a missing record, and UpdateWebhookDelivery merges the
// supplied payload/metadata onto the stored maps rather than replacing them.
type WebhookStore interface {
	InsertWebhookDelivery(ctx context.Context, d *model.WebhookDelivery) error
	UpdateWebhookDelivery(ctx context.Context, d *model.WebhookDelivery) error
	GetWebhookDelivery(ctx context.Context, id string) (*model.WebhookDelivery, error)
	ListWebhookDeliveries(ctx context.Context, filter WebhookFilter) ([]model.WebhookDelivery, error)
	WebhookMetrics(ctx context.Context) ([]WebhookMetric, error)
}

// Compile-time proof that the PostgreSQL adapter satisfies the boundary.
var _ WebhookStore = (*DB)(nil)
