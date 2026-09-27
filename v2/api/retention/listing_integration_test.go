package retention

import (
	"context"
	"errors"
	"testing"

	"norn/v2/api/saga"
)

// App and recent listings stay exactly what they were before pruning: the
// newest events across hot and archived history, not a silently hot-only
// view. A server without the archive answers only when no archived bundle
// could contribute, and otherwise fails explicitly.
func TestAppAndRecentListingsAreArchiveAwareAfterPruning(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	older := []string{f.finishedSaga("listing-a", 3).SagaID, f.finishedSaga("listing-b", 2).SagaID, f.finishedSaga("listing-a", 2).SagaID}
	ids := func(events []saga.Event) []string {
		out := make([]string, len(events))
		for index, event := range events {
			out[index] = event.ID
		}
		return out
	}
	type listing struct {
		app   string
		limit int
	}
	cases := []listing{{"listing-a", 3}, {"listing-a", 10}, {"listing-b", 1}, {"", 4}, {"", 50}}
	list := func(s saga.Store, c listing) ([]saga.Event, error) {
		if c.app == "" {
			return s.ListRecent(ctx, c.limit)
		}
		return s.ListByApp(ctx, c.app, c.limit)
	}
	truth := map[listing][]string{}
	for _, c := range cases {
		events, err := list(f.hot, c)
		if err != nil {
			t.Fatal(err)
		}
		truth[c] = ids(events)
	}
	f.archiver.Mode = ModePrune
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Pruned != 7 {
		t.Fatalf("prune = %+v, %v", report, err)
	}
	for _, sagaID := range older {
		if f.hotCount(sagaID) != 0 {
			t.Fatalf("saga %s still hot", sagaID)
		}
	}
	history := &HistoryStore{Hot: f.hot, DB: f.db, Archive: f.objects}
	for _, c := range cases {
		events, err := list(history, c)
		if err != nil || !equalIDs(ids(events), truth[c]) {
			t.Fatalf("%+v after pruning = %v, %v; before pruning %v", c, ids(events), err, truth[c])
		}
	}

	// New hot activity newer than every archived bundle: a server without the
	// archive can answer a small recent listing, but not one that reaches
	// into archived history.
	newer := f.finishedSaga("listing-a", 2)
	unarchived := &HistoryStore{Hot: f.hot, DB: f.db}
	if events, err := unarchived.ListByApp(ctx, "listing-a", 2); err != nil || len(events) != 2 || events[0].SagaID != newer.SagaID {
		t.Fatalf("hot-only-sufficient listing = %v, %v", ids(events), err)
	}
	var unavailable *ErrArchivedHistoryUnavailable
	if _, err := unarchived.ListByApp(ctx, "listing-a", 5); !errors.As(err, &unavailable) {
		t.Fatalf("listing needing archived history without an archive = %v", err)
	}
	if _, err := unarchived.ListRecent(ctx, 10); !errors.As(err, &unavailable) {
		t.Fatalf("recent listing needing archived history without an archive = %v", err)
	}
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
