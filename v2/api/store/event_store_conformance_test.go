package store

import (
	"context"
	"os"
	"testing"
	"time"

	"norn/v2/api/hub"
	"norn/v2/api/memstore"
)

// TestEventStoreConformance_Memory runs the same event-store conformance suite
// against the in-memory adapter. It needs no database, so it proves in ordinary
// CI that the contract is backend-neutral rather than PostgreSQL-specific — the
// second-backend acceptance mechanism the etcd adapter will reuse.
func TestEventStoreConformance_Memory(t *testing.T) {
	runEventStoreConformance(t, func(t *testing.T) hub.EventStore {
		return memstore.NewEventStore()
	})
}

// TestEventStoreConformance_Postgres runs the shared event-store conformance
// suite against the PostgreSQL adapter. Opt-in via NORN_TEST_DATABASE_URL. A
// future etcd adapter (cursors -> revisions under an authority epoch) gets a
// sibling test that calls runEventStoreConformance with its own factory and
// must pass the same invariants.
func TestEventStoreConformance_Postgres(t *testing.T) {
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
	runEventStoreConformance(t, func(t *testing.T) hub.EventStore {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM control_events"); err != nil {
			t.Fatalf("reset control_events: %v", err)
		}
		return db
	})
}

// runEventStoreConformance is the backend-neutral behavioral contract for the
// hub event store. newStore must return a store backed by an empty event log on
// each call. Cursor VALUES are backend-defined (Postgres serial ids, etcd
// revisions), so the suite asserts only relative ordering and monotonicity.
func runEventStoreConformance(t *testing.T, newStore func(t *testing.T) hub.EventStore) {
	ctx := context.Background()

	appendEvent := func(t *testing.T, s hub.EventStore, typ string, payload interface{}) hub.Event {
		t.Helper()
		e := hub.Event{Type: typ, AppID: "conf-app", Timestamp: time.Now().UTC(), Payload: payload}
		if err := s.AppendHubEvent(ctx, &e); err != nil {
			t.Fatal(err)
		}
		if e.ID == 0 {
			t.Fatal("append did not assign a cursor id")
		}
		return e
	}

	t.Run("AppendAssignsMonotonicCursor", func(t *testing.T) {
		s := newStore(t)
		a := appendEvent(t, s, "a", nil)
		b := appendEvent(t, s, "b", nil)
		c := appendEvent(t, s, "c", nil)
		if !(a.ID < b.ID && b.ID < c.ID) {
			t.Fatalf("cursors not monotonic: %d, %d, %d", a.ID, b.ID, c.ID)
		}
		latest, err := s.LatestHubEventID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if latest != c.ID {
			t.Fatalf("latest cursor = %d, want %d", latest, c.ID)
		}
	})

	t.Run("ListAfterReturnsOrderedTail", func(t *testing.T) {
		s := newStore(t)
		a := appendEvent(t, s, "a", nil)
		b := appendEvent(t, s, "b", nil)
		c := appendEvent(t, s, "c", nil)
		all, err := s.ListHubEventsAfter(ctx, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 3 || all[0].ID != a.ID || all[1].ID != b.ID || all[2].ID != c.ID {
			t.Fatalf("tail not ordered from zero: %+v", all)
		}
		tail, err := s.ListHubEventsAfter(ctx, a.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(tail) != 2 || tail[0].ID != b.ID {
			t.Fatalf("cursor did not skip consumed events: %+v", tail)
		}
	})

	t.Run("ListRespectsLimit", func(t *testing.T) {
		s := newStore(t)
		a := appendEvent(t, s, "a", nil)
		b := appendEvent(t, s, "b", nil)
		_ = appendEvent(t, s, "c", nil)
		page, err := s.ListHubEventsAfter(ctx, 0, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 2 || page[0].ID != a.ID || page[1].ID != b.ID {
			t.Fatalf("limit not honored as the earliest window: %+v", page)
		}
	})

	t.Run("BoundsReflectRetainedRange", func(t *testing.T) {
		s := newStore(t)
		first := appendEvent(t, s, "a", nil)
		_ = appendEvent(t, s, "b", nil)
		last := appendEvent(t, s, "c", nil)
		bounds, err := s.HubEventBounds(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if bounds.OldestCursor != first.ID || bounds.LatestCursor != last.ID || bounds.RetainedEvents != 3 {
			t.Fatalf("bounds wrong: %+v (first=%d last=%d)", bounds, first.ID, last.ID)
		}
		if bounds.OldestTimestamp == nil || bounds.LatestTimestamp == nil {
			t.Fatalf("bounds timestamps not populated: %+v", bounds)
		}
	})

	t.Run("PayloadRoundTrips", func(t *testing.T) {
		s := newStore(t)
		e := appendEvent(t, s, "deploy", map[string]interface{}{"app": "web", "n": float64(3)})
		got, err := s.ListHubEventsAfter(ctx, e.ID-1, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			t.Fatal("event not returned")
		}
		payload, ok := got[len(got)-1].Payload.(map[string]interface{})
		if !ok || payload["app"] != "web" || payload["n"] != float64(3) {
			t.Fatalf("payload did not round trip: %+v", got[len(got)-1].Payload)
		}
	})

	t.Run("EmptyLogHasZeroBounds", func(t *testing.T) {
		s := newStore(t)
		bounds, err := s.HubEventBounds(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if bounds.OldestCursor != 0 || bounds.LatestCursor != 0 || bounds.RetainedEvents != 0 {
			t.Fatalf("empty bounds nonzero: %+v", bounds)
		}
		if bounds.OldestTimestamp != nil || bounds.LatestTimestamp != nil {
			t.Fatalf("empty bounds have timestamps: %+v", bounds)
		}
		latest, err := s.LatestHubEventID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if latest != 0 {
			t.Fatalf("empty latest cursor = %d, want 0", latest)
		}
	})
}
