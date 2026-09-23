package store

import (
	"context"
	"time"
)

// AccessPatternStore is the control boundary for access-observation analytics:
// hour-bucketed request/success/error counts per (app, process, endpoint,
// source), and the re-grouping into hour-of-day/weekday pattern rows a readiness
// decision may reference. It is a cleanly isolated single-table concern — a
// backend-neutral seam for Norn v3 (roadmap M3 / P5). RecordAccessObservation
// accumulates onto the hour bucket; ReplaceAccessObservation overwrites it.
type AccessPatternStore interface {
	RecordAccessObservation(ctx context.Context, obs AccessObservation) error
	ReplaceAccessObservation(ctx context.Context, obs AccessObservation) error
	ListAccessPatternRows(ctx context.Context, since time.Time) ([]AccessPatternRow, error)
	PruneAccessObservations(ctx context.Context, olderThan time.Time) error
}

// Compile-time proof that the PostgreSQL adapter satisfies the boundary.
var _ AccessPatternStore = (*DB)(nil)
