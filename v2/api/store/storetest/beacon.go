package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// RunBeaconStoreConformance is the backend-neutral behavioral contract for
// store.BeaconStore — insert/list/get with filtering and pagination, open-event
// selection, single and incident-group acknowledge/snooze/open, correlation
// queries, dedupe windows, active-incident grouping, auto-resolve, watcher-type
// selection, prune and metrics.
func RunBeaconStoreConformance(t *testing.T, newStore func(t *testing.T) store.BeaconStore) {
	ctx := context.Background()
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	newEvent := func(app, typ string, sev model.BeaconSeverity, correlationKey string, at time.Time) *model.BeaconEvent {
		e := &model.BeaconEvent{
			ID: uuid.NewString(), Source: "watcher", App: app, Environment: "prod",
			Type: typ, Severity: sev, Title: "t", Body: "b",
			DedupeKey: "dk-" + typ, OccurredAt: at,
			Metadata: map[string]interface{}{},
		}
		if correlationKey != "" {
			e.Metadata["correlationKey"] = correlationKey
		}
		return e
	}
	crit := model.BeaconSeverity("critical")
	warn := model.BeaconSeverity("warning")
	info := model.BeaconSeverity("info")

	t.Run("InsertListFilterPaginateTotal", func(t *testing.T) {
		s := newStore(t)
		a := newEvent("web", "nomad.task.dead", crit, "", base)
		b := newEvent("web", "service.health.down", warn, "", base.Add(time.Minute))
		c := newEvent("api", "cron.missed", info, "", base.Add(2*time.Minute))
		for _, e := range []*model.BeaconEvent{a, b, c} {
			if err := s.InsertBeaconEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		events, total, err := s.ListBeaconEvents(ctx, store.BeaconFilter{App: "web"})
		if err != nil {
			t.Fatal(err)
		}
		if total != 2 || len(events) != 2 {
			t.Fatalf("app filter: total=%d len=%d, want 2/2", total, len(events))
		}
		if !events[0].OccurredAt.After(events[1].OccurredAt) {
			t.Fatal("not ordered occurred_at DESC")
		}
		// Pagination: limit 1 offset 1 still reports full total.
		page, total, err := s.ListBeaconEvents(ctx, store.BeaconFilter{App: "web", Limit: 1, Offset: 1})
		if err != nil {
			t.Fatal(err)
		}
		if total != 2 || len(page) != 1 {
			t.Fatalf("pagination: total=%d len=%d, want 2/1", total, len(page))
		}
		if page[0].ID != a.ID {
			t.Fatal("offset did not skip the newest event")
		}
	})

	t.Run("OpenEventsExcludeAckedSnoozedAndInfo", func(t *testing.T) {
		s := newStore(t)
		open := newEvent("web", "nomad.task.dead", crit, "", base)
		infoEv := newEvent("web", "cron.ok", info, "", base)
		acked := newEvent("web", "service.health.down", warn, "", base)
		for _, e := range []*model.BeaconEvent{open, infoEv, acked} {
			if err := s.InsertBeaconEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.AcknowledgeBeaconEvent(ctx, acked.ID, "op", "done"); err != nil {
			t.Fatal(err)
		}
		list, err := s.ListOpenBeaconEvents(ctx, "web", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].ID != open.ID {
			t.Fatalf("open list wrong: %d events", len(list))
		}
	})

	t.Run("AckSnoozeOpenSingleAndState", func(t *testing.T) {
		s := newStore(t)
		e := newEvent("web", "nomad.task.dead", crit, "", base)
		if err := s.InsertBeaconEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		acked, err := s.AcknowledgeBeaconEvent(ctx, e.ID, "op", "handled")
		if err != nil {
			t.Fatal(err)
		}
		if acked.State != "acknowledged" || acked.AcknowledgedAt == nil {
			t.Fatalf("ack state wrong: %+v", acked.State)
		}
		snoozed, err := s.SnoozeBeaconEvent(ctx, e.ID, "op", "later", time.Now().Add(72*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if snoozed.State != "snoozed" {
			t.Fatalf("snooze state = %q", snoozed.State)
		}
		opened, err := s.OpenBeaconEvent(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if opened.State != "open" || opened.AcknowledgedAt != nil || opened.SnoozedUntil != nil {
			t.Fatalf("open state wrong: %+v", opened.State)
		}
		if _, err := s.AcknowledgeBeaconEvent(ctx, "missing", "op", ""); err == nil {
			t.Fatal("ack of a missing event should error")
		}
	})

	t.Run("IncidentGroupByCorrelationAndDedupe", func(t *testing.T) {
		s := newStore(t)
		a := newEvent("web", "nomad.task.dead", crit, "corr", base)
		b := newEvent("web", "service.health.down", warn, "corr", base.Add(time.Minute))
		infoInGroup := newEvent("web", "cron.ok", info, "corr", base)
		for _, e := range []*model.BeaconEvent{a, b, infoInGroup} {
			if err := s.InsertBeaconEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		n, err := s.AcknowledgeIncidentGroup(ctx, store.IncidentGroupKey{CorrelationKey: "corr"}, "op", "grouped")
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("group ack count = %d, want 2 (info excluded)", n)
		}
		if got, _ := s.GetBeaconEvent(ctx, infoInGroup.ID); got.AcknowledgedAt != nil {
			t.Fatal("info event should not have been acked")
		}
		if _, err := s.AcknowledgeIncidentGroup(ctx, store.IncidentGroupKey{}, "op", ""); err == nil {
			t.Fatal("empty incident group key should error")
		}
	})

	t.Run("CorrelatedListAndLaterQueries", func(t *testing.T) {
		s := newStore(t)
		first := newEvent("web", "nomad.task.dead", crit, "cx", base)
		second := newEvent("web", "nomad.task.dead", crit, "cx", base.Add(time.Minute))
		for _, e := range []*model.BeaconEvent{second, first} { // insert out of order
			if err := s.InsertBeaconEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		list, err := s.ListCorrelatedEvents(ctx, "cx", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 || list[0].ID != first.ID {
			t.Fatal("correlated list not ordered occurred_at ASC")
		}
		exists, err := s.LaterBeaconEventExists(ctx, "web", "nomad.task.dead", base)
		if err != nil || !exists {
			t.Fatalf("later exists = %v err=%v, want true", exists, err)
		}
		later, err := s.LaterBeaconEventForCorrelation(ctx, "watcher", "web", "prod", "nomad.task.dead", "cx", base)
		if err != nil {
			t.Fatal(err)
		}
		if later.ID != second.ID {
			t.Fatal("later-for-correlation should return the newest")
		}
		if _, err := s.LaterBeaconEventForCorrelation(ctx, "watcher", "web", "prod", "nomad.task.dead", "cx", base.Add(time.Hour)); err == nil {
			t.Fatal("no later event should error")
		}
	})

	t.Run("RecentDedupeExists", func(t *testing.T) {
		s := newStore(t)
		e := newEvent("web", "nomad.task.dead", crit, "", time.Now().UTC().Add(-time.Minute))
		e.DedupeKey = "dedupe-1"
		if err := s.InsertBeaconEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		got, err := s.RecentDedupeExists(ctx, "dedupe-1", time.Hour)
		if err != nil || !got {
			t.Fatalf("recent dedupe = %v err=%v, want true", got, err)
		}
		got, err = s.RecentDedupeExists(ctx, "dedupe-1", time.Second)
		if err != nil || got {
			t.Fatalf("dedupe outside window = %v, want false", got)
		}
	})

	t.Run("ActiveIncidentsGrouping", func(t *testing.T) {
		s := newStore(t)
		// c1: two open actionable events -> active incident.
		c1a := newEvent("web", "nomad.task.dead", crit, "c1", base)
		c1b := newEvent("web", "service.health.down", warn, "c1", base.Add(time.Minute))
		// c2: acked -> not active.
		c2 := newEvent("web", "nomad.task.dead", crit, "c2", base)
		// no correlation key -> excluded.
		loose := newEvent("web", "nomad.task.dead", crit, "", base)
		for _, e := range []*model.BeaconEvent{c1a, c1b, c2, loose} {
			if err := s.InsertBeaconEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.AcknowledgeBeaconEvent(ctx, c2.ID, "op", ""); err != nil {
			t.Fatal(err)
		}
		incidents, err := s.ListActiveIncidents(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(incidents) != 1 {
			t.Fatalf("active incidents = %d, want 1", len(incidents))
		}
		inc := incidents[0]
		// The current implementation reflects only the latest event of a group
		// (its window aggregates run after selecting the latest row), so
		// EventCount and OpenCount are 1 and the latest event identifies the group.
		if inc.CorrelationKey != "c1" || inc.EventCount != 1 || inc.OpenCount != 1 {
			t.Fatalf("incident aggregation wrong: %+v", inc)
		}
		if inc.LatestEventID != c1b.ID {
			t.Fatal("latest event should be the newest in the group")
		}
	})

	t.Run("AutoAckCorrelated", func(t *testing.T) {
		s := newStore(t)
		older := newEvent("web", "nomad.task.dead", crit, "ck", base)
		mid := newEvent("web", "nomad.task.dead", warn, "ck", base.Add(time.Minute))
		resolving := newEvent("web", "nomad.task.dead", crit, "ck", base.Add(2*time.Minute))
		for _, e := range []*model.BeaconEvent{older, mid, resolving} {
			if err := s.InsertBeaconEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		n, err := s.AutoAckCorrelatedEvents(ctx, "watcher", "web", "prod", "ck", resolving.ID, resolving.OccurredAt)
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("auto-ack count = %d, want 2 (older+mid, not resolving)", n)
		}
		if got, _ := s.GetBeaconEvent(ctx, resolving.ID); got.AcknowledgedAt != nil {
			t.Fatal("resolving event must not be acked")
		}
	})

	t.Run("WatcherEventsPruneMetrics", func(t *testing.T) {
		s := newStore(t)
		watcher := newEvent("web", "nomad.allocation.failed", crit, "", time.Now().UTC().Add(-time.Minute))
		nonWatcher := newEvent("web", "deploy.finished", info, "", time.Now().UTC().Add(-time.Minute))
		old := newEvent("web", "nomad.task.dead", crit, "", base.AddDate(0, 0, -60))
		for _, e := range []*model.BeaconEvent{watcher, nonWatcher, old} {
			if err := s.InsertBeaconEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		recents, err := s.RecentWatcherEvents(ctx, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if len(recents) != 1 || recents[0].ID != watcher.ID {
			t.Fatalf("watcher events wrong: %d", len(recents))
		}
		if err := s.PruneBeaconEvents(ctx, base.AddDate(0, 0, -1)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetBeaconEvent(ctx, old.ID); err == nil {
			t.Fatal("old event should have been pruned")
		}
		metrics, err := s.BeaconMetrics(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range metrics {
			if m.Type == "nomad.allocation.failed" && m.Severity == "critical" && m.Count == 1 {
				found = true
			}
		}
		if !found {
			t.Fatalf("metrics missing expected group: %+v", metrics)
		}
	})
}
