package store_test

import (
	"context"
	"testing"

	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestRecoveryDrillStoreConformance_Postgres runs the shared recovery-drill
// suite against PG. Opt-in via NORN_TEST_DATABASE_URL.
func TestRecoveryDrillStoreConformance_Postgres(t *testing.T) {
	db := connectForConformance(t)
	storetest.RunRecoveryDrillStoreConformance(t, func(t *testing.T) store.RecoveryDrillStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM recovery_drills"); err != nil {
			t.Fatalf("reset recovery_drills: %v", err)
		}
		return db
	})
}
