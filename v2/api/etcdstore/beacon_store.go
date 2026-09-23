package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// BeaconStore is an etcd-backed store.BeaconStore. Each event is a single JSON
// value under prefix/beacon/<id>; beacon has no secondary index in PostgreSQL
// either, so every query is a prefix scan filtered in memory — acceptable for
// control-plane-sized incident state. Incident grouping, dedupe windows,
// correlation queries and metrics reproduce the SQL semantics exactly. It passes
// the shared conformance suite.
type BeaconStore struct {
	kv     clientv3.KV
	prefix string
}

// NewBeaconStore returns an etcd beacon store rooted at prefix.
func NewBeaconStore(kv clientv3.KV, prefix string) *BeaconStore {
	return &BeaconStore{kv: kv, prefix: prefix}
}

var _ store.BeaconStore = (*BeaconStore)(nil)

func (s *BeaconStore) key(id string) string { return s.prefix + "/beacon/" + id }
func (s *BeaconStore) keyPrefix() string     { return s.prefix + "/beacon/" }

// watcherTypePrefixes mirror the SQL `type LIKE ANY(ARRAY[...])` prefixes.
var watcherTypePrefixes = []string{"nomad.allocation.", "service.health.", "cron.", "nomad.task."}

func beaconState(e model.BeaconEvent) string {
	now := time.Now()
	if e.SnoozedUntil != nil && e.SnoozedUntil.After(now) {
		return "snoozed"
	}
	if e.AcknowledgedAt != nil {
		return "acknowledged"
	}
	return "open"
}

func correlationKeyOf(e model.BeaconEvent) string {
	if e.Metadata == nil {
		return ""
	}
	if v, ok := e.Metadata["correlationKey"].(string); ok {
		return v
	}
	return ""
}

func isActionableSeverity(sev model.BeaconSeverity) bool {
	s := string(sev)
	return s == "warning" || s == "critical"
}

// isOpen reproduces `acknowledged_at IS NULL AND (snoozed_until IS NULL OR
// snoozed_until < now())`.
func isOpen(e model.BeaconEvent, now time.Time) bool {
	return e.AcknowledgedAt == nil && (e.SnoozedUntil == nil || e.SnoozedUntil.Before(now))
}

func (s *BeaconStore) put(ctx context.Context, e *model.BeaconEvent) error {
	stored := *e
	stored.State = "" // State is computed on read, never persisted.
	if stored.Metadata == nil {
		stored.Metadata = map[string]interface{}{}
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, s.key(e.ID), string(raw))
	return err
}

func (s *BeaconStore) load(ctx context.Context, id string) (*model.BeaconEvent, int64, error) {
	resp, err := s.kv.Get(ctx, s.key(id))
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, ErrNotFound
	}
	var e model.BeaconEvent
	if err := json.Unmarshal(resp.Kvs[0].Value, &e); err != nil {
		return nil, 0, err
	}
	e.State = beaconState(e)
	return &e, resp.Kvs[0].ModRevision, nil
}

func (s *BeaconStore) scan(ctx context.Context) ([]model.BeaconEvent, error) {
	resp, err := s.kv.Get(ctx, s.keyPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]model.BeaconEvent, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var e model.BeaconEvent
		if err := json.Unmarshal(kv.Value, &e); err != nil {
			return nil, err
		}
		e.State = beaconState(e)
		out = append(out, e)
	}
	return out, nil
}

func (s *BeaconStore) InsertBeaconEvent(ctx context.Context, event *model.BeaconEvent) error {
	return s.put(ctx, event)
}

func sortByOccurredDesc(events []model.BeaconEvent) {
	sort.Slice(events, func(i, j int) bool {
		if !events[i].OccurredAt.Equal(events[j].OccurredAt) {
			return events[i].OccurredAt.After(events[j].OccurredAt)
		}
		return events[i].ID > events[j].ID
	})
}

func (s *BeaconStore) ListBeaconEvents(ctx context.Context, filter store.BeaconFilter) ([]model.BeaconEvent, int, error) {
	all, err := s.scan(ctx)
	if err != nil {
		return nil, 0, err
	}
	var matched []model.BeaconEvent
	for _, e := range all {
		if filter.App != "" && e.App != filter.App {
			continue
		}
		if filter.Type != "" && e.Type != filter.Type {
			continue
		}
		if filter.Severity != "" && string(e.Severity) != filter.Severity {
			continue
		}
		matched = append(matched, e)
	}
	total := len(matched)
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	sortByOccurredDesc(matched)
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(matched) {
		offset = len(matched)
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	page := matched[offset:end]
	if page == nil {
		page = []model.BeaconEvent{}
	}
	return page, total, nil
}

func (s *BeaconStore) ListOpenBeaconEvents(ctx context.Context, app string, limit int) ([]model.BeaconEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	all, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var matched []model.BeaconEvent
	for _, e := range all {
		if !isActionableSeverity(e.Severity) || !isOpen(e, now) {
			continue
		}
		if app != "" && e.App != app {
			continue
		}
		matched = append(matched, e)
	}
	sortByOccurredDesc(matched)
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

func (s *BeaconStore) GetBeaconEvent(ctx context.Context, id string) (*model.BeaconEvent, error) {
	e, _, err := s.load(ctx, id)
	return e, err
}

// mutateOne loads an event, applies apply, and CAS-writes it. A missing event
// returns ErrNotFound, matching the PG UPDATE ... WHERE id=$1 followed by a
// GetBeaconEvent that finds no row.
func (s *BeaconStore) mutateOne(ctx context.Context, id string, apply func(*model.BeaconEvent)) (*model.BeaconEvent, error) {
	for attempt := 0; attempt < 32; attempt++ {
		e, rev, err := s.load(ctx, id)
		if err != nil {
			return nil, err
		}
		apply(e)
		stored := *e
		stored.State = ""
		if stored.Metadata == nil {
			stored.Metadata = map[string]interface{}{}
		}
		raw, err := json.Marshal(stored)
		if err != nil {
			return nil, err
		}
		resp, err := s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(s.key(id)), "=", rev)).
			Then(clientv3.OpPut(s.key(id), string(raw))).
			Commit()
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			e.State = beaconState(*e)
			return e, nil
		}
	}
	return nil, errors.New("etcdstore: beacon mutate exhausted retries under contention")
}

func (s *BeaconStore) AcknowledgeBeaconEvent(ctx context.Context, id, by, note string) (*model.BeaconEvent, error) {
	now := time.Now()
	return s.mutateOne(ctx, id, func(e *model.BeaconEvent) {
		e.AcknowledgedAt = &now
		e.AcknowledgedBy = by
		e.AcknowledgementNote = note
		e.SnoozedUntil = nil
	})
}

func (s *BeaconStore) SnoozeBeaconEvent(ctx context.Context, id, by, note string, until time.Time) (*model.BeaconEvent, error) {
	return s.mutateOne(ctx, id, func(e *model.BeaconEvent) {
		u := until
		e.SnoozedUntil = &u
		e.AcknowledgedBy = by
		e.AcknowledgementNote = note
	})
}

func (s *BeaconStore) OpenBeaconEvent(ctx context.Context, id string) (*model.BeaconEvent, error) {
	return s.mutateOne(ctx, id, func(e *model.BeaconEvent) {
		e.AcknowledgedAt = nil
		e.AcknowledgedBy = ""
		e.AcknowledgementNote = ""
		e.SnoozedUntil = nil
	})
}

// incidentGroupMatch reproduces incidentGroupWhere: correlation key wins, else
// dedupe key; empty key means no group.
func incidentGroupMatch(key store.IncidentGroupKey) (func(model.BeaconEvent) bool, bool) {
	switch {
	case key.CorrelationKey != "":
		return func(e model.BeaconEvent) bool { return correlationKeyOf(e) == key.CorrelationKey }, true
	case key.DedupeKey != "":
		return func(e model.BeaconEvent) bool { return e.DedupeKey == key.DedupeKey }, true
	default:
		return nil, false
	}
}

func (s *BeaconStore) updateGroup(ctx context.Context, key store.IncidentGroupKey, apply func(*model.BeaconEvent)) (int, error) {
	match, ok := incidentGroupMatch(key)
	if !ok {
		return 0, fmt.Errorf("incident group key is required")
	}
	all, err := s.scan(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for i := range all {
		e := all[i]
		if !match(e) || !isActionableSeverity(e.Severity) {
			continue
		}
		if _, err := s.mutateOne(ctx, e.ID, apply); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *BeaconStore) AcknowledgeIncidentGroup(ctx context.Context, key store.IncidentGroupKey, by, note string) (int, error) {
	now := time.Now()
	return s.updateGroup(ctx, key, func(e *model.BeaconEvent) {
		e.AcknowledgedAt = &now
		e.AcknowledgedBy = by
		e.AcknowledgementNote = note
		e.SnoozedUntil = nil
	})
}

func (s *BeaconStore) SnoozeIncidentGroup(ctx context.Context, key store.IncidentGroupKey, by, note string, until time.Time) (int, error) {
	return s.updateGroup(ctx, key, func(e *model.BeaconEvent) {
		u := until
		e.SnoozedUntil = &u
		e.AcknowledgedBy = by
		e.AcknowledgementNote = note
	})
}

func (s *BeaconStore) OpenIncidentGroup(ctx context.Context, key store.IncidentGroupKey) (int, error) {
	return s.updateGroup(ctx, key, func(e *model.BeaconEvent) {
		e.AcknowledgedAt = nil
		e.AcknowledgedBy = ""
		e.AcknowledgementNote = ""
		e.SnoozedUntil = nil
	})
}

func (s *BeaconStore) ListCorrelatedEvents(ctx context.Context, correlationKey string, limit int) ([]model.BeaconEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	all, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	var matched []model.BeaconEvent
	for _, e := range all {
		if correlationKeyOf(e) == correlationKey {
			matched = append(matched, e)
		}
	}
	// ORDER BY occurred_at ASC.
	sort.Slice(matched, func(i, j int) bool { return matched[i].OccurredAt.Before(matched[j].OccurredAt) })
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

func (s *BeaconStore) LaterBeaconEventExists(ctx context.Context, app, eventType string, after time.Time) (bool, error) {
	all, err := s.scan(ctx)
	if err != nil {
		return false, err
	}
	for _, e := range all {
		if e.App == app && e.Type == eventType && e.OccurredAt.After(after) {
			return true, nil
		}
	}
	return false, nil
}

func (s *BeaconStore) LaterBeaconEventForCorrelation(ctx context.Context, source, app, environment, eventType, correlationKey string, after time.Time) (*model.BeaconEvent, error) {
	all, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	var matched []model.BeaconEvent
	for _, e := range all {
		if e.Source == source && e.App == app && e.Environment == environment && e.Type == eventType &&
			correlationKeyOf(e) == correlationKey && e.OccurredAt.After(after) {
			matched = append(matched, e)
		}
	}
	if len(matched) == 0 {
		return nil, ErrNotFound
	}
	// ORDER BY occurred_at DESC, id DESC LIMIT 1.
	sortByOccurredDesc(matched)
	return &matched[0], nil
}

func (s *BeaconStore) RecentDedupeExists(ctx context.Context, dedupeKey string, within time.Duration) (bool, error) {
	all, err := s.scan(ctx)
	if err != nil {
		return false, err
	}
	cutoff := time.Now().UTC().Add(-within)
	for _, e := range all {
		if e.DedupeKey == dedupeKey && e.OccurredAt.After(cutoff) {
			return true, nil
		}
	}
	return false, nil
}

func (s *BeaconStore) ListActiveIncidents(ctx context.Context, limit int) ([]store.ActiveIncident, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	all, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	groups := map[string][]model.BeaconEvent{}
	for _, e := range all {
		ck := correlationKeyOf(e)
		if ck == "" {
			continue
		}
		groups[ck] = append(groups[ck], e)
	}
	var incidents []store.ActiveIncident
	for ck, events := range groups {
		sortByOccurredDesc(events)
		latest := events[0]
		if !isActionableSeverity(latest.Severity) {
			continue
		}
		// Parity with the PostgreSQL query: its window aggregates (COUNT/MIN/MAX/
		// SUM OVER PARTITION) are computed AFTER `WHERE rn = 1`, which leaves a
		// single latest row per correlation key. So event_count is always 1,
		// first_seen == last_seen == the latest event's time, and open_count
		// reflects only whether the latest event is open. This mirrors that exact
		// (latest-event-only) behavior; see the note surfaced with this change.
		if !isOpen(latest, now) {
			continue
		}
		incidents = append(incidents, store.ActiveIncident{
			CorrelationKey: ck,
			App:            latest.App,
			LatestSeverity: string(latest.Severity),
			LatestType:     latest.Type,
			LatestTitle:    latest.Title,
			EventCount:     1,
			LatestEventID:  latest.ID,
			FirstSeen:      latest.OccurredAt,
			LastSeen:       latest.OccurredAt,
			OpenCount:      1,
		})
	}
	// ORDER BY last_seen DESC.
	sort.Slice(incidents, func(i, j int) bool { return incidents[i].LastSeen.After(incidents[j].LastSeen) })
	if len(incidents) > limit {
		incidents = incidents[:limit]
	}
	return incidents, nil
}

func (s *BeaconStore) AutoAckCorrelatedEvents(ctx context.Context, source, app, environment, correlationKey, resolvingEventID string, resolvingOccurredAt time.Time) (int, error) {
	all, err := s.scan(ctx)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	count := 0
	for _, e := range all {
		if e.Source != source || e.App != app || e.Environment != environment {
			continue
		}
		if correlationKeyOf(e) != correlationKey || !isActionableSeverity(e.Severity) {
			continue
		}
		if e.AcknowledgedAt != nil || e.ID == resolvingEventID || e.OccurredAt.After(resolvingOccurredAt) {
			continue
		}
		if _, err := s.mutateOne(ctx, e.ID, func(ev *model.BeaconEvent) {
			ev.AcknowledgedAt = &now
			ev.AcknowledgedBy = "system"
			ev.AcknowledgementNote = "resolved by " + resolvingEventID
			ev.SnoozedUntil = nil
		}); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *BeaconStore) RecentWatcherEvents(ctx context.Context, since time.Duration) ([]model.BeaconEvent, error) {
	all, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().Add(-since)
	var matched []model.BeaconEvent
	for _, e := range all {
		if !e.OccurredAt.After(cutoff) {
			continue
		}
		isWatcher := false
		for _, p := range watcherTypePrefixes {
			if strings.HasPrefix(e.Type, p) {
				isWatcher = true
				break
			}
		}
		if isWatcher {
			matched = append(matched, e)
		}
	}
	// ORDER BY occurred_at ASC.
	sort.Slice(matched, func(i, j int) bool { return matched[i].OccurredAt.Before(matched[j].OccurredAt) })
	return matched, nil
}

func (s *BeaconStore) PruneBeaconEvents(ctx context.Context, olderThan time.Time) error {
	all, err := s.kv.Get(ctx, s.keyPrefix(), clientv3.WithPrefix())
	if err != nil {
		return err
	}
	for _, kv := range all.Kvs {
		var e model.BeaconEvent
		if err := json.Unmarshal(kv.Value, &e); err != nil {
			return err
		}
		if e.OccurredAt.Before(olderThan) {
			if _, err := s.kv.Delete(ctx, string(kv.Key)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *BeaconStore) BeaconMetrics(ctx context.Context) ([]store.BeaconMetric, error) {
	all, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	type key struct {
		typ string
		sev string
	}
	agg := map[key]*store.BeaconMetric{}
	for _, e := range all {
		k := key{e.Type, string(e.Severity)}
		m, ok := agg[k]
		if !ok {
			m = &store.BeaconMetric{Type: e.Type, Severity: string(e.Severity)}
			agg[k] = m
		}
		m.Count++
		if occurred := float64(e.OccurredAt.Unix()); occurred > m.LastOccurredUnix {
			m.LastOccurredUnix = occurred
		}
	}
	out := make([]store.BeaconMetric, 0, len(agg))
	for _, m := range agg {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Severity < out[j].Severity
	})
	return out, nil
}
