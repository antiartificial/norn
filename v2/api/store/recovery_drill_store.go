package store

import (
	"context"
	"time"
)

// RecoveryDrillStore is the control boundary for disaster-recovery drill records
// and the latest qualifying proof a readiness decision depends on. It is a
// cleanly isolated single-table concern — a backend-neutral seam for Norn v3
// (roadmap M3 / P5). FinishRecoveryDrill only transitions a still-running drill.
type RecoveryDrillStore interface {
	InsertRecoveryDrill(ctx context.Context, drill *RecoveryDrill) error
	FinishRecoveryDrill(ctx context.Context, id, status string, evidence map[string]string, finishedAt time.Time) (*RecoveryDrill, error)
	GetRecoveryDrill(ctx context.Context, id string) (*RecoveryDrill, error)
	ListRecoveryDrills(ctx context.Context, limit int) ([]RecoveryDrill, error)
	LatestPassedRecoveryDrills(ctx context.Context) (map[string]time.Time, error)
}

// Compile-time proof that the PostgreSQL adapter satisfies the boundary.
var _ RecoveryDrillStore = (*DB)(nil)
