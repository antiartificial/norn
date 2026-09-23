package store

import (
	"context"

	"norn/v2/api/model"
)

// NotificationStore is the control boundary for notification channel
// configuration (Discord/ntfy/Pushover destinations and their credentials). It
// is a cleanly isolated single-table concern with no cross-boundary coupling — a
// backend-neutral seam for Norn v3 (roadmap M3 / P5). Channel credentials are
// authoritative configuration, not diagnostics; a second adapter must preserve
// them intact.
type NotificationStore interface {
	InsertNotificationChannel(ctx context.Context, ch *model.NotificationChannel) error
	ListNotificationChannels(ctx context.Context) ([]model.NotificationChannel, error)
	GetNotificationChannel(ctx context.Context, id string) (*model.NotificationChannel, error)
	DeleteNotificationChannel(ctx context.Context, id string) error
}

// Compile-time proof that the PostgreSQL adapter satisfies the boundary.
var _ NotificationStore = (*DB)(nil)
