package beacon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestCapacityRecoveryAutoAckRequiresLegacyTargetOrScopedKey(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := store.Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM beacon_events WHERE id = ANY($1)`, ids)
	})
	service := New(db, nil, Config{Environment: "development"})
	emit := func(event model.BeaconEvent) {
		t.Helper()
		if _, err := service.Emit(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	legacyKey := "norn-host:minimum-capacity"
	emit(model.BeaconEvent{ID: ids[0], Source: "norn", App: "norn-host", Environment: "development", Type: "service.capacity.below_minimum", Severity: model.BeaconWarning, Title: "legacy warning", Body: "test", DedupeKey: "test:" + ids[0], OccurredAt: now, Metadata: map[string]interface{}{"correlationKey": legacyKey}})
	emit(model.BeaconEvent{ID: ids[1], Source: "norn", App: "norn-host", Environment: "development", Type: "service.capacity.recovered", Severity: model.BeaconInfo, Title: "generic legacy recovery", Body: "test", DedupeKey: "test:" + ids[1], OccurredAt: now.Add(time.Minute), Metadata: map[string]interface{}{"correlationKey": legacyKey}})
	legacyWarning, err := db.GetBeaconEvent(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if legacyWarning.AcknowledgedAt != nil {
		t.Fatal("generic legacy recovery bulk-acknowledged a global capacity warning")
	}
	emit(model.BeaconEvent{ID: ids[2], Source: "norn", App: "norn-host", Environment: "development", Type: "service.capacity.recovered", Severity: model.BeaconInfo, Title: "targeted legacy recovery", Body: "test", DedupeKey: "test:" + ids[2], OccurredAt: now.Add(2 * time.Minute), Metadata: map[string]interface{}{"correlationKey": legacyKey, "legacyCapacityWarningID": ids[0]}})
	legacyWarning, err = db.GetBeaconEvent(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if legacyWarning.AcknowledgedAt == nil {
		t.Fatal("targeted legacy recovery did not acknowledge its exact warning")
	}

	scopedKey := "norn-host:mini-a:minimum-capacity"
	emit(model.BeaconEvent{ID: ids[3], Source: "norn-host:mini-a", App: "norn-host", Environment: "development", Type: "service.capacity.below_minimum", Severity: model.BeaconWarning, Title: "scoped warning", Body: "test", DedupeKey: "test:" + ids[3], OccurredAt: now, Metadata: map[string]interface{}{"correlationKey": scopedKey, "hostScope": "mini-a"}})
	emit(model.BeaconEvent{ID: ids[4], Source: "norn-host:mini-a", App: "norn-host", Environment: "development", Type: "service.capacity.recovered", Severity: model.BeaconInfo, Title: "scoped recovery", Body: "test", DedupeKey: "test:" + ids[4], OccurredAt: now.Add(time.Minute), Metadata: map[string]interface{}{"correlationKey": scopedKey, "hostScope": "mini-a"}})
	scopedWarning, err := db.GetBeaconEvent(ctx, ids[3])
	if err != nil {
		t.Fatal(err)
	}
	if scopedWarning.AcknowledgedAt == nil {
		t.Fatal("scoped capacity recovery did not acknowledge its warning")
	}
}
