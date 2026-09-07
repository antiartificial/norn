package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

// TestCapacityRecoveryScopeAndTimeFence exercises the SQL predicates that
// prevent a host recovery from acknowledging correlated warnings belonging to
// another source, app, environment, or later capacity observation.
func TestCapacityRecoveryScopeAndTimeFence(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	const (
		source      = "capacity-test-host"
		app         = "norn-host"
		environment = "staging"
		key         = "norn-host:minimum-capacity"
	)
	ids := make([]string, 0, 8)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM beacon_events WHERE id = ANY($1)`, ids)
	})
	insert := func(id, eventSource, eventApp, eventEnvironment, eventType string, severity model.BeaconSeverity, occurredAt time.Time, correlationKey string) {
		ids = append(ids, id)
		event := &model.BeaconEvent{
			ID:          id,
			Source:      eventSource,
			App:         eventApp,
			Environment: eventEnvironment,
			Type:        eventType,
			Severity:    severity,
			Title:       "capacity integration test",
			Body:        "test-only",
			DedupeKey:   "integration:" + id,
			OccurredAt:  occurredAt,
			Metadata:    map[string]interface{}{"correlationKey": correlationKey},
		}
		if err := db.InsertBeaconEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	warningID := uuid.NewString()
	wrongSourceID := uuid.NewString()
	wrongAppID := uuid.NewString()
	wrongEnvironmentID := uuid.NewString()
	wrongKeyID := uuid.NewString()
	futureWarningID := uuid.NewString()
	wrongTypeID := uuid.NewString()
	recoveryID := uuid.NewString()

	insert(warningID, source, app, environment, "service.capacity.below_minimum", model.BeaconWarning, now, key)
	insert(wrongSourceID, "another-host", app, environment, "service.capacity.below_minimum", model.BeaconWarning, now, key)
	insert(wrongAppID, source, "another-app", environment, "service.capacity.below_minimum", model.BeaconWarning, now, key)
	insert(wrongEnvironmentID, source, app, "production", "service.capacity.below_minimum", model.BeaconWarning, now, key)
	insert(wrongKeyID, source, app, environment, "service.capacity.below_minimum", model.BeaconWarning, now, "another-correlation")
	insert(futureWarningID, source, app, environment, "service.capacity.below_minimum", model.BeaconWarning, now.Add(2*time.Minute), key)
	insert(wrongTypeID, source, app, environment, "host.assurance.failed", model.BeaconWarning, now, key)
	insert(recoveryID, source, app, environment, "service.capacity.recovered", model.BeaconInfo, now.Add(time.Minute), key)

	recovery, err := db.LaterBeaconEventForCorrelation(ctx, source, app, environment, "service.capacity.recovered", key, now)
	if err != nil || recovery.ID != recoveryID {
		t.Fatalf("matching capacity recovery = %#v, err = %v", recovery, err)
	}
	if _, err := db.LaterBeaconEventForCorrelation(ctx, source, app, "production", "service.capacity.recovered", key, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-environment recovery query err = %v, want pgx.ErrNoRows", err)
	}

	acked, err := db.AutoAckCorrelatedEventsOfType(ctx, source, app, environment, key, recoveryID, recovery.OccurredAt, "service.capacity.below_minimum")
	if err != nil || acked != 1 {
		t.Fatalf("AutoAckCorrelatedEvents() = %d, %v; want 1, nil", acked, err)
	}
	for _, check := range []struct {
		id          string
		acked       bool
		description string
	}{
		{warningID, true, "matching prior warning"},
		{wrongSourceID, false, "other source"},
		{wrongAppID, false, "other app"},
		{wrongEnvironmentID, false, "other environment"},
		{wrongKeyID, false, "other correlation"},
		{futureWarningID, false, "later observation"},
		{wrongTypeID, false, "other warning type sharing legacy key"},
	} {
		event, err := db.GetBeaconEvent(ctx, check.id)
		if err != nil {
			t.Fatal(err)
		}
		if got := event.AcknowledgedAt != nil; got != check.acked {
			t.Fatalf("%s acknowledgement = %t, want %t", check.description, got, check.acked)
		}
	}
}

func TestListActiveIncidentsAggregatesAllCorrelationEvents(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	correlationKey := "integration:active-incident:" + uuid.NewString()
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM beacon_events WHERE id = ANY($1)`, ids)
	})
	for i, event := range []struct {
		severity   model.BeaconSeverity
		occurredAt time.Time
	}{
		{model.BeaconWarning, now},
		{model.BeaconWarning, now.Add(time.Minute)},
		{model.BeaconCritical, now.Add(2 * time.Minute)},
	} {
		if err := db.InsertBeaconEvent(ctx, &model.BeaconEvent{
			ID: ids[i], Source: "integration-test", App: "test-app", Environment: "test",
			Type: "integration.active-incident", Severity: event.severity,
			Title: "active incident aggregation", Body: "test-only", DedupeKey: "integration:" + ids[i],
			OccurredAt: event.occurredAt, Metadata: map[string]interface{}{"correlationKey": correlationKey},
		}); err != nil {
			t.Fatal(err)
		}
	}

	incidents, err := db.ListActiveIncidents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, incident := range incidents {
		if incident.CorrelationKey != correlationKey {
			continue
		}
		if incident.EventCount != 3 || incident.OpenCount != 3 {
			t.Fatalf("incident counts = events %d, open %d; want 3, 3", incident.EventCount, incident.OpenCount)
		}
		if !incident.FirstSeen.Equal(now) || !incident.LastSeen.Equal(now.Add(2*time.Minute)) {
			t.Fatalf("incident window = %s to %s; want %s to %s", incident.FirstSeen, incident.LastSeen, now, now.Add(2*time.Minute))
		}
		if incident.LatestEventID != ids[2] || incident.LatestSeverity != string(model.BeaconCritical) {
			t.Fatalf("latest incident = id %q severity %q; want id %q severity %q", incident.LatestEventID, incident.LatestSeverity, ids[2], model.BeaconCritical)
		}
		return
	}
	t.Fatalf("incident with correlation key %q not found", correlationKey)
}

func TestListActiveIncidentsSeparatesSourceAndEnvironment(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	correlationKey := "integration:legacy-capacity:" + uuid.NewString()
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM beacon_events WHERE id = ANY($1)`, ids)
	})
	insert := func(i int, source, environment string, occurredAt time.Time) {
		t.Helper()
		if err := db.InsertBeaconEvent(ctx, &model.BeaconEvent{
			ID: ids[i], Source: source, App: "norn-host", Environment: environment,
			Type: "service.capacity.below_minimum", Severity: model.BeaconWarning,
			Title: "legacy capacity", Body: "test-only", DedupeKey: "integration:" + ids[i],
			OccurredAt: occurredAt, Metadata: map[string]interface{}{"correlationKey": correlationKey},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Old hash-style rows from one Mini remain one timeline, while the same
	// global key from a separate source/environment must not be merged.
	insert(0, "norn", "development", now)
	insert(1, "norn", "development", now.Add(time.Minute))
	insert(2, "another-mini", "development", now.Add(2*time.Minute))
	insert(3, "norn", "production", now.Add(3*time.Minute))

	incidents, err := db.ListActiveIncidents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var matched int
	for _, incident := range incidents {
		if incident.CorrelationKey != correlationKey {
			continue
		}
		matched++
		if incident.Source == "norn" && incident.Environment == "development" {
			if incident.EventCount != 2 || incident.OpenCount != 2 || !incident.FirstSeen.Equal(now) || !incident.LastSeen.Equal(now.Add(time.Minute)) {
				t.Fatalf("legacy Mini timeline = %#v, want two events from %s through %s", incident, now, now.Add(time.Minute))
			}
		}
	}
	if matched != 3 {
		t.Fatalf("same correlation key produced %d scoped incidents, want 3", matched)
	}
	scopedEvents, err := db.ListCorrelatedEventsScoped(ctx, correlationKey, 100, "norn", "norn-host", "development")
	if err != nil {
		t.Fatal(err)
	}
	if len(scopedEvents) != 2 {
		t.Fatalf("scoped correlated events = %d, want 2", len(scopedEvents))
	}
	allEvents, err := db.ListCorrelatedEvents(ctx, correlationKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(allEvents) != 4 {
		t.Fatalf("key-only correlated events = %d, want 4", len(allEvents))
	}
}

func TestAutoAckCapacityWarningByIDDoesNotBroadenLegacyAdoption(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM beacon_events WHERE id = ANY($1)`, ids)
	})
	for i, id := range ids[:2] {
		if err := db.InsertBeaconEvent(ctx, &model.BeaconEvent{
			ID: id, Source: "norn", App: "norn-host", Environment: "development",
			Type: "service.capacity.below_minimum", Severity: model.BeaconWarning,
			Title: "legacy capacity", Body: "test-only", DedupeKey: "integration:" + id,
			OccurredAt: now.Add(time.Duration(i) * time.Minute), Metadata: map[string]interface{}{"correlationKey": "norn-host:minimum-capacity"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InsertBeaconEvent(ctx, &model.BeaconEvent{
		ID: ids[2], Source: "norn", App: "norn-host", Environment: "development",
		Type: "service.capacity.recovered", Severity: model.BeaconInfo,
		Title: "legacy adoption", Body: "test-only", DedupeKey: "integration:" + ids[2],
		OccurredAt: now.Add(2 * time.Minute), Metadata: map[string]interface{}{"correlationKey": "norn-host:minimum-capacity", "legacyCapacityWarningID": ids[0]},
	}); err != nil {
		t.Fatal(err)
	}
	acked, err := db.AutoAckCapacityWarningByID(ctx, ids[0], "norn", "norn-host", "development", "norn-host:minimum-capacity", ids[2], now.Add(2*time.Minute))
	if err != nil || acked != 1 {
		t.Fatalf("AutoAckCapacityWarningByID() = %d, %v; want 1, nil", acked, err)
	}
	for _, check := range []struct {
		id   string
		want bool
	}{{ids[0], true}, {ids[1], false}} {
		event, err := db.GetBeaconEvent(ctx, check.id)
		if err != nil {
			t.Fatal(err)
		}
		if got := event.AcknowledgedAt != nil; got != check.want {
			t.Fatalf("legacy warning %s acknowledged = %t, want %t", check.id, got, check.want)
		}
	}
}

func TestListActiveIncidentsUsesNewestOpenWarningAfterTargetedLegacyAdoption(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM beacon_events WHERE id = ANY($1)`, ids)
	})
	insert := func(id, eventType, title string, severity model.BeaconSeverity, occurredAt time.Time, metadata map[string]interface{}) {
		t.Helper()
		if err := db.InsertBeaconEvent(ctx, &model.BeaconEvent{
			ID: id, Source: "norn", App: "norn-host", Environment: "development", Type: eventType,
			Severity: severity, Title: title, Body: "test-only", DedupeKey: "integration:" + id,
			OccurredAt: occurredAt, Metadata: metadata,
		}); err != nil {
			t.Fatal(err)
		}
	}
	legacyKey := "norn-host:minimum-capacity"
	insert(ids[0], "service.capacity.below_minimum", "10 processes below minimum", model.BeaconWarning, now, map[string]interface{}{"correlationKey": legacyKey})
	insert(ids[1], "service.capacity.below_minimum", "9 processes below minimum", model.BeaconWarning, now.Add(time.Minute), map[string]interface{}{"correlationKey": legacyKey})
	insert(ids[2], "service.capacity.recovered", "adopted 9-process warning", model.BeaconInfo, now.Add(2*time.Minute), map[string]interface{}{"correlationKey": legacyKey, "legacyCapacityWarningID": ids[1]})
	acked, err := db.AutoAckCapacityWarningByID(ctx, ids[1], "norn", "norn-host", "development", legacyKey, ids[2], now.Add(2*time.Minute))
	if err != nil || acked != 1 {
		t.Fatalf("AutoAckCapacityWarningByID() = %d, %v; want 1, nil", acked, err)
	}

	incidents, err := db.ListActiveIncidents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, incident := range incidents {
		if incident.CorrelationKey != legacyKey || incident.Source != "norn" || incident.Environment != "development" || incident.App != "norn-host" {
			continue
		}
		if incident.EventCount != 3 || incident.OpenCount != 1 {
			t.Fatalf("incident counts = events %d, open %d; want 3, 1", incident.EventCount, incident.OpenCount)
		}
		if incident.LatestEventID != ids[0] || incident.LatestTitle != "10 processes below minimum" || incident.LatestSeverity != string(model.BeaconWarning) {
			t.Fatalf("active representative = %#v, want unmatched 10-process warning", incident)
		}
		if !incident.FirstSeen.Equal(now) || !incident.LastSeen.Equal(now.Add(2*time.Minute)) {
			t.Fatalf("timeline = %s to %s, want %s to %s", incident.FirstSeen, incident.LastSeen, now, now.Add(2*time.Minute))
		}
		return
	}
	t.Fatal("unmatched legacy warning was hidden after targeted adoption")
}

func TestLaterLegacyCapacityRecoveryForWarningIgnoresNewerGenericRecovery(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM beacon_events WHERE id = ANY($1)`, ids)
	})
	insert := func(id, eventType string, occurredAt time.Time, metadata map[string]interface{}) {
		t.Helper()
		if err := db.InsertBeaconEvent(ctx, &model.BeaconEvent{ID: id, Source: "norn", App: "norn-host", Environment: "development", Type: eventType, Severity: model.BeaconInfo, Title: "legacy recovery", Body: "test-only", DedupeKey: "integration:" + id, OccurredAt: occurredAt, Metadata: metadata}); err != nil {
			t.Fatal(err)
		}
	}
	legacyKey := "norn-host:minimum-capacity"
	insert(ids[0], "service.capacity.below_minimum", now, map[string]interface{}{"correlationKey": legacyKey})
	insert(ids[1], "service.capacity.recovered", now.Add(time.Minute), map[string]interface{}{"correlationKey": legacyKey, "legacyCapacityWarningID": ids[0]})
	insert(ids[2], "service.capacity.recovered", now.Add(2*time.Minute), map[string]interface{}{"correlationKey": legacyKey})
	recovery, err := db.LaterLegacyCapacityRecoveryForWarning(ctx, "norn", "norn-host", "development", ids[0], now)
	if err != nil || recovery.ID != ids[1] {
		t.Fatalf("LaterLegacyCapacityRecoveryForWarning() = %#v, %v; want %s", recovery, err, ids[1])
	}
}

func TestIncidentGroupActionScopeDoesNotCrossSourceOrEnvironment(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM beacon_events WHERE id = ANY($1)`, ids)
	})
	key := "integration:scoped-action:" + uuid.NewString()
	insert := func(id, source, environment string) {
		t.Helper()
		if err := db.InsertBeaconEvent(ctx, &model.BeaconEvent{ID: id, Source: source, App: "norn-host", Environment: environment, Type: "service.capacity.below_minimum", Severity: model.BeaconWarning, Title: "scoped action", Body: "test-only", DedupeKey: "integration:" + id, OccurredAt: now, Metadata: map[string]interface{}{"correlationKey": key}}); err != nil {
			t.Fatal(err)
		}
	}
	insert(ids[0], "norn-host:mini-a", "mini")
	insert(ids[1], "norn-host:mini-b", "mini")
	insert(ids[2], "norn-host:mini-a", "production")
	affected, err := db.AcknowledgeIncidentGroup(ctx, IncidentGroupKey{CorrelationKey: key, Source: "norn-host:mini-a", App: "norn-host", Environment: "mini"}, "operator", "scoped test")
	if err != nil || affected != 1 {
		t.Fatalf("AcknowledgeIncidentGroup() = %d, %v; want 1, nil", affected, err)
	}
	for _, check := range []struct {
		id   string
		want bool
	}{{ids[0], true}, {ids[1], false}, {ids[2], false}} {
		event, err := db.GetBeaconEvent(ctx, check.id)
		if err != nil {
			t.Fatal(err)
		}
		if got := event.AcknowledgedAt != nil; got != check.want {
			t.Fatalf("event %s acknowledged = %t, want %t", check.id, got, check.want)
		}
	}
}
