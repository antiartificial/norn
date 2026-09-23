package store

import "context"

// CronStore is the control boundary for scheduled-process pause/schedule state,
// keyed by (app, process). It is a cleanly isolated single-table concern — a
// backend-neutral seam for Norn v3 (roadmap M3 / P5). The state is authoritative
// until its resource is deleted; changes belong in independent audit evidence.
type CronStore interface {
	GetCronState(ctx context.Context, app, process string) (*CronState, error)
	GetCronStates(ctx context.Context, app string) ([]CronState, error)
	UpsertCronState(ctx context.Context, app, process string, paused bool, schedule string) error
}

// Compile-time proof that the PostgreSQL adapter satisfies the boundary.
var _ CronStore = (*DB)(nil)
