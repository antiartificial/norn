// Package etcdstore holds the etcd-backed implementations of the Norn v3
// control-store boundaries. They implement the same interfaces as the
// PostgreSQL adapter and pass the same storetest conformance suites, which is
// how v3 qualifies a fresh HA Fleet on etcd (roadmap M3 / P5) without a control
// PostgreSQL.
package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/hub"
)

// EventStore is an etcd-backed hub.EventStore. Cursors are a monotonic counter
// maintained under prefix/seq via a compare-and-swap transaction, and each event
// is written to a lexicographically sortable key (prefix/log/<20-digit-seq>), so
// a forward range scan returns events in cursor order. Cursor values are opaque
// and strictly increasing, matching the PostgreSQL serial-id contract.
type EventStore struct {
	kv     clientv3.KV
	prefix string
}

// NewEventStore returns an etcd event store rooted at prefix (e.g. "/norn").
func NewEventStore(kv clientv3.KV, prefix string) *EventStore {
	return &EventStore{kv: kv, prefix: prefix}
}

var _ hub.EventStore = (*EventStore)(nil)

func (s *EventStore) seqKey() string    { return s.prefix + "/events/seq" }
func (s *EventStore) logPrefix() string { return s.prefix + "/events/log/" }
func (s *EventStore) logKey(seq int64) string {
	return fmt.Sprintf("%s%020d", s.logPrefix(), seq)
}

type storedEvent struct {
	Timestamp time.Time   `json:"timestamp"`
	Type      string      `json:"type"`
	AppID     string      `json:"appId"`
	Payload   interface{} `json:"payload"`
}

// AppendHubEvent assigns the next monotonic cursor and writes the counter and
// the event atomically. The compare-and-swap on the counter's revision makes
// concurrent appends safe: a lost race retries against the new counter value.
func (s *EventStore) AppendHubEvent(ctx context.Context, event *hub.Event) error {
	value, err := json.Marshal(storedEvent{Timestamp: event.Timestamp, Type: event.Type, AppID: event.AppID, Payload: event.Payload})
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 32; attempt++ {
		current, err := s.kv.Get(ctx, s.seqKey())
		if err != nil {
			return err
		}
		var cur int64
		var guard clientv3.Cmp
		if len(current.Kvs) == 0 {
			guard = clientv3.Compare(clientv3.CreateRevision(s.seqKey()), "=", 0)
		} else {
			cur, err = strconv.ParseInt(string(current.Kvs[0].Value), 10, 64)
			if err != nil {
				return fmt.Errorf("corrupt event sequence: %w", err)
			}
			guard = clientv3.Compare(clientv3.ModRevision(s.seqKey()), "=", current.Kvs[0].ModRevision)
		}
		next := cur + 1
		resp, err := s.kv.Txn(ctx).
			If(guard).
			Then(
				clientv3.OpPut(s.seqKey(), strconv.FormatInt(next, 10)),
				clientv3.OpPut(s.logKey(next), string(value)),
			).
			Commit()
		if err != nil {
			return err
		}
		if resp.Succeeded {
			event.ID = next
			return nil
		}
	}
	return fmt.Errorf("append event: exhausted retries under contention")
}

func (s *EventStore) seqFromKey(key string) (int64, error) {
	return strconv.ParseInt(key[len(s.logPrefix()):], 10, 64)
}

func decodeEvent(id int64, raw []byte) (hub.Event, error) {
	var stored storedEvent
	if err := json.Unmarshal(raw, &stored); err != nil {
		return hub.Event{}, err
	}
	return hub.Event{ID: id, Timestamp: stored.Timestamp, Type: stored.Type, AppID: stored.AppID, Payload: stored.Payload}, nil
}

// ListHubEventsAfter returns events with a cursor strictly greater than after,
// in ascending cursor order, capped at limit (default/ceiling 500/1000).
func (s *EventStore) ListHubEventsAfter(ctx context.Context, after int64, limit int) ([]hub.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	start := s.logKey(after + 1)
	resp, err := s.kv.Get(ctx, start,
		clientv3.WithRange(prefixEnd(s.logPrefix())),
		clientv3.WithLimit(int64(limit)),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
	)
	if err != nil {
		return nil, err
	}
	events := []hub.Event{}
	for _, kv := range resp.Kvs {
		id, err := s.seqFromKey(string(kv.Key))
		if err != nil {
			return nil, err
		}
		event, err := decodeEvent(id, kv.Value)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

// LatestHubEventID returns the highest assigned cursor, or 0 when empty.
func (s *EventStore) LatestHubEventID(ctx context.Context) (int64, error) {
	resp, err := s.kv.Get(ctx, s.seqKey())
	if err != nil {
		return 0, err
	}
	if len(resp.Kvs) == 0 {
		return 0, nil
	}
	return strconv.ParseInt(string(resp.Kvs[0].Value), 10, 64)
}

// HubEventBounds reports the retained cursor range and timestamp span.
func (s *EventStore) HubEventBounds(ctx context.Context) (hub.EventBounds, error) {
	var bounds hub.EventBounds
	count, err := s.kv.Get(ctx, s.logPrefix(), clientv3.WithPrefix(), clientv3.WithCountOnly())
	if err != nil {
		return bounds, err
	}
	bounds.RetainedEvents = count.Count
	if count.Count == 0 {
		return bounds, nil
	}
	oldest, err := s.kv.Get(ctx, s.logPrefix(), clientv3.WithPrefix(), clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend), clientv3.WithLimit(1))
	if err != nil {
		return bounds, err
	}
	latest, err := s.kv.Get(ctx, s.logPrefix(), clientv3.WithPrefix(), clientv3.WithSort(clientv3.SortByKey, clientv3.SortDescend), clientv3.WithLimit(1))
	if err != nil {
		return bounds, err
	}
	oldestID, err := s.seqFromKey(string(oldest.Kvs[0].Key))
	if err != nil {
		return bounds, err
	}
	latestID, err := s.seqFromKey(string(latest.Kvs[0].Key))
	if err != nil {
		return bounds, err
	}
	oldestEvent, err := decodeEvent(oldestID, oldest.Kvs[0].Value)
	if err != nil {
		return bounds, err
	}
	latestEvent, err := decodeEvent(latestID, latest.Kvs[0].Value)
	if err != nil {
		return bounds, err
	}
	bounds.OldestCursor, bounds.LatestCursor = oldestID, latestID
	oldestTS, latestTS := oldestEvent.Timestamp, latestEvent.Timestamp
	bounds.OldestTimestamp, bounds.LatestTimestamp = &oldestTS, &latestTS
	return bounds, nil
}

// prefixEnd returns the end of the key range that WithPrefix would cover, so a
// bounded range scan starting mid-prefix stays within the prefix.
func prefixEnd(prefix string) string {
	end := []byte(prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return string(end[:i+1])
		}
	}
	return "\x00"
}
