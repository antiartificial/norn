package etcdstore

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

// MutationAuditStore is an etcd-backed store.MutationAuditStore. Evidence
// records live under prefix/maudit/events/<id> and incidents under
// prefix/maudit/incidents/<id>, each a JSON value. It preserves the two-phase
// reserve/finish contract (finish only transitions a still-"started" record,
// once) and retention semantics (prune only finished records), and passes the
// shared conformance suite.
type MutationAuditStore struct {
	kv     clientv3.KV
	prefix string
}

// NewMutationAuditStore returns an etcd evidence store rooted at prefix.
func NewMutationAuditStore(kv clientv3.KV, prefix string) *MutationAuditStore {
	return &MutationAuditStore{kv: kv, prefix: prefix}
}

var _ store.MutationAuditStore = (*MutationAuditStore)(nil)

func (s *MutationAuditStore) eventKey(id string) string   { return s.prefix + "/maudit/events/" + id }
func (s *MutationAuditStore) eventsPrefix() string         { return s.prefix + "/maudit/events/" }
func (s *MutationAuditStore) incidentKey(id string) string { return s.prefix + "/maudit/incidents/" + id }
func (s *MutationAuditStore) incidentsPrefix() string      { return s.prefix + "/maudit/incidents/" }

// storedAuditEvent/storedIncident persist RecordDigest, which the model types tag
// json:"-" (the PostgreSQL adapter keeps it in a dedicated column). Embedding
// keeps every other field; the outer RecordDigest wins the JSON name.
type storedAuditEvent struct {
	store.MutationAuditEvent
	RecordDigest string `json:"recordDigest"`
}

type storedIncident struct {
	store.MutationAuditIncident
	RecordDigest string `json:"recordDigest"`
}

func marshalEvent(e store.MutationAuditEvent) ([]byte, error) {
	return json.Marshal(storedAuditEvent{MutationAuditEvent: e, RecordDigest: e.RecordDigest})
}

func unmarshalEvent(raw []byte) (store.MutationAuditEvent, error) {
	var s storedAuditEvent
	if err := json.Unmarshal(raw, &s); err != nil {
		return store.MutationAuditEvent{}, err
	}
	e := s.MutationAuditEvent
	e.RecordDigest = s.RecordDigest
	return e, nil
}

func (s *MutationAuditStore) putEvent(ctx context.Context, e store.MutationAuditEvent) error {
	raw, err := marshalEvent(e)
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, s.eventKey(e.ID), string(raw))
	return err
}

// ReserveMutationAudit persists a started record before the mutation runs.
func (s *MutationAuditStore) ReserveMutationAudit(ctx context.Context, event *store.MutationAuditEvent) error {
	stored := *event
	stored.Status = 0
	stored.Outcome = "started"
	stored.FinishedAt = nil
	stored.DurationMs = 0
	stored.RecordDigest = ""
	return s.putEvent(ctx, stored)
}

func (s *MutationAuditStore) loadEvent(ctx context.Context, id string) (*store.MutationAuditEvent, error) {
	resp, err := s.kv.Get(ctx, s.eventKey(id))
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, ErrNotFound
	}
	event, err := unmarshalEvent(resp.Kvs[0].Value)
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// FinishMutationAudit records the outcome of a still-started record exactly
// once; finishing an already-finished or unknown record returns ErrNotFound,
// matching the PostgreSQL adapter's pgx.ErrNoRows.
func (s *MutationAuditStore) FinishMutationAudit(ctx context.Context, id, path string, status int, outcome string, finishedAt time.Time, durationMs int64, digest string) error {
	event, err := s.loadEvent(ctx, id)
	if err != nil {
		return err
	}
	if event.Outcome != "started" {
		return ErrNotFound
	}
	event.Path = path
	event.Status = status
	event.Outcome = outcome
	finished := finishedAt
	event.FinishedAt = &finished
	event.DurationMs = durationMs
	event.RecordDigest = digest
	return s.putEvent(ctx, *event)
}

func (s *MutationAuditStore) ListMutationAudits(ctx context.Context, limit int) ([]store.MutationAuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	resp, err := s.kv.Get(ctx, s.eventsPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	events := make([]store.MutationAuditEvent, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		event, err := unmarshalEvent(kv.Value)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].StartedAt.After(events[j].StartedAt) })
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func (s *MutationAuditStore) GetMutationAudit(ctx context.Context, id string) (*store.MutationAuditEvent, error) {
	return s.loadEvent(ctx, id)
}

func (s *MutationAuditStore) InsertMutationAuditIncident(ctx context.Context, incident *store.MutationAuditIncident) error {
	raw, err := json.Marshal(storedIncident{MutationAuditIncident: *incident, RecordDigest: incident.RecordDigest})
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, s.incidentKey(incident.ID), string(raw))
	return err
}

func (s *MutationAuditStore) ListMutationAuditIncidents(ctx context.Context, eventIDs []string) (map[string]store.MutationAuditIncident, error) {
	incidents := map[string]store.MutationAuditIncident{}
	if len(eventIDs) == 0 {
		return incidents, nil
	}
	wanted := map[string]bool{}
	for _, id := range eventIDs {
		wanted[id] = true
	}
	resp, err := s.kv.Get(ctx, s.incidentsPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	for _, kv := range resp.Kvs {
		var si storedIncident
		if err := json.Unmarshal(kv.Value, &si); err != nil {
			return nil, err
		}
		incident := si.MutationAuditIncident
		incident.RecordDigest = si.RecordDigest
		if wanted[incident.AuditEventID] {
			incidents[incident.AuditEventID] = incident
		}
	}
	return incidents, nil
}

func (s *MutationAuditStore) CountStaleMutationAudits(ctx context.Context, before time.Time) (int, error) {
	resp, err := s.kv.Get(ctx, s.eventsPrefix(), clientv3.WithPrefix())
	if err != nil {
		return 0, err
	}
	count := 0
	for _, kv := range resp.Kvs {
		event, err := unmarshalEvent(kv.Value)
		if err != nil {
			return 0, err
		}
		if event.Outcome == "started" && event.StartedAt.Before(before) {
			count++
		}
	}
	return count, nil
}

func (s *MutationAuditStore) PruneMutationAudits(ctx context.Context, before time.Time) (int64, error) {
	resp, err := s.kv.Get(ctx, s.eventsPrefix(), clientv3.WithPrefix())
	if err != nil {
		return 0, err
	}
	var pruned int64
	for _, kv := range resp.Kvs {
		event, err := unmarshalEvent(kv.Value)
		if err != nil {
			return pruned, err
		}
		if event.FinishedAt != nil && event.FinishedAt.Before(before) {
			if _, err := s.kv.Delete(ctx, s.eventKey(event.ID)); err != nil {
				return pruned, err
			}
			pruned++
		}
	}
	return pruned, nil
}
