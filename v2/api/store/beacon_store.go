package store

import (
	"context"
	"time"

	"norn/v2/api/model"
)

// BeaconStore is the control boundary for beacon incident/alert events: open
// actionable incidents, acknowledgement and snooze state, correlation-key
// grouping, dedupe windows and watcher/auto-resolve queries. It is a cleanly
// isolated single-table concern (no cross-boundary FKs) — a backend-neutral seam
// for Norn v3 (roadmap M3 / P5). Beacon bodies are not classified as logs;
// unresolved incidents are preserved.
type BeaconStore interface {
	InsertBeaconEvent(ctx context.Context, event *model.BeaconEvent) error
	ListBeaconEvents(ctx context.Context, filter BeaconFilter) ([]model.BeaconEvent, int, error)
	ListOpenBeaconEvents(ctx context.Context, app string, limit int) ([]model.BeaconEvent, error)
	GetBeaconEvent(ctx context.Context, id string) (*model.BeaconEvent, error)
	AcknowledgeBeaconEvent(ctx context.Context, id, by, note string) (*model.BeaconEvent, error)
	SnoozeBeaconEvent(ctx context.Context, id, by, note string, until time.Time) (*model.BeaconEvent, error)
	OpenBeaconEvent(ctx context.Context, id string) (*model.BeaconEvent, error)
	AcknowledgeIncidentGroup(ctx context.Context, key IncidentGroupKey, by, note string) (int, error)
	SnoozeIncidentGroup(ctx context.Context, key IncidentGroupKey, by, note string, until time.Time) (int, error)
	OpenIncidentGroup(ctx context.Context, key IncidentGroupKey) (int, error)
	ListCorrelatedEvents(ctx context.Context, correlationKey string, limit int) ([]model.BeaconEvent, error)
	LaterBeaconEventExists(ctx context.Context, app, eventType string, after time.Time) (bool, error)
	LaterBeaconEventForCorrelation(ctx context.Context, source, app, environment, eventType, correlationKey string, after time.Time) (*model.BeaconEvent, error)
	RecentDedupeExists(ctx context.Context, dedupeKey string, within time.Duration) (bool, error)
	ListActiveIncidents(ctx context.Context, limit int) ([]ActiveIncident, error)
	AutoAckCorrelatedEvents(ctx context.Context, source, app, environment, correlationKey, resolvingEventID string, resolvingOccurredAt time.Time) (int, error)
	RecentWatcherEvents(ctx context.Context, since time.Duration) ([]model.BeaconEvent, error)
	PruneBeaconEvents(ctx context.Context, olderThan time.Time) error
	BeaconMetrics(ctx context.Context) ([]BeaconMetric, error)
}

// Compile-time proof that the PostgreSQL adapter satisfies the boundary.
var _ BeaconStore = (*DB)(nil)
