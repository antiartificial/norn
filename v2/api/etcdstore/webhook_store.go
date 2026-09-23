package etcdstore

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// WebhookStore is an etcd-backed store.WebhookStore. Each delivery is a single
// JSON value under prefix/webhook/<id>. UpdateWebhookDelivery reproduces the
// PostgreSQL adapter's jsonb-merge semantics (payload/metadata are merged onto
// the stored maps, not replaced) with an optimistic compare-and-swap so a
// concurrent update is not clobbered. Key-less lookups (list, metrics) scan the
// prefix. It passes the shared conformance suite.
type WebhookStore struct {
	kv     clientv3.KV
	prefix string
}

// NewWebhookStore returns an etcd webhook store rooted at prefix.
func NewWebhookStore(kv clientv3.KV, prefix string) *WebhookStore {
	return &WebhookStore{kv: kv, prefix: prefix}
}

var _ store.WebhookStore = (*WebhookStore)(nil)

func (s *WebhookStore) key(id string) string { return s.prefix + "/webhook/" + id }
func (s *WebhookStore) keyPrefix() string     { return s.prefix + "/webhook/" }

func hydrateWebhook(d *model.WebhookDelivery) {
	if d.Payload == nil {
		d.Payload = map[string]interface{}{}
	}
	if d.Metadata == nil {
		d.Metadata = map[string]interface{}{}
	}
}

func (s *WebhookStore) InsertWebhookDelivery(ctx context.Context, d *model.WebhookDelivery) error {
	stored := *d
	hydrateWebhook(&stored)
	if stored.Status == "" {
		stored.Status = "received"
	}
	if stored.ReceivedAt.IsZero() {
		stored.ReceivedAt = time.Now()
	}
	stored.UpdatedAt = time.Now()
	raw, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, s.key(stored.ID), string(raw))
	return err
}

func (s *WebhookStore) load(ctx context.Context, id string) (*model.WebhookDelivery, int64, error) {
	resp, err := s.kv.Get(ctx, s.key(id))
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, ErrNotFound
	}
	var d model.WebhookDelivery
	if err := json.Unmarshal(resp.Kvs[0].Value, &d); err != nil {
		return nil, 0, err
	}
	hydrateWebhook(&d)
	return &d, resp.Kvs[0].ModRevision, nil
}

// UpdateWebhookDelivery merges the supplied fields onto the stored record. A
// missing record is a silent no-op, matching the PostgreSQL UPDATE that affects
// zero rows. provider, remote_addr, user_agent and received_at are preserved;
// payload and metadata are merged; the rest are overwritten from d.
func (s *WebhookStore) UpdateWebhookDelivery(ctx context.Context, d *model.WebhookDelivery) error {
	for attempt := 0; attempt < 32; attempt++ {
		existing, rev, err := s.load(ctx, d.ID)
		if err == ErrNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		existing.Event = d.Event
		existing.DeliveryID = d.DeliveryID
		existing.Repository = d.Repository
		existing.Ref = d.Ref
		existing.Branch = d.Branch
		existing.App = d.App
		existing.SagaID = d.SagaID
		existing.Status = d.Status
		existing.Reason = d.Reason
		for k, v := range d.Payload {
			existing.Payload[k] = v
		}
		for k, v := range d.Metadata {
			existing.Metadata[k] = v
		}
		existing.UpdatedAt = time.Now()
		raw, err := json.Marshal(existing)
		if err != nil {
			return err
		}
		resp, err := s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(s.key(d.ID)), "=", rev)).
			Then(clientv3.OpPut(s.key(d.ID), string(raw))).
			Commit()
		if err != nil {
			return err
		}
		if resp.Succeeded {
			return nil
		}
	}
	return nil
}

// GetWebhookDelivery returns (nil, nil) for a missing record, matching the
// PostgreSQL adapter.
func (s *WebhookStore) GetWebhookDelivery(ctx context.Context, id string) (*model.WebhookDelivery, error) {
	d, _, err := s.load(ctx, id)
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (s *WebhookStore) scan(ctx context.Context) ([]model.WebhookDelivery, error) {
	resp, err := s.kv.Get(ctx, s.keyPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]model.WebhookDelivery, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var d model.WebhookDelivery
		if err := json.Unmarshal(kv.Value, &d); err != nil {
			return nil, err
		}
		hydrateWebhook(&d)
		out = append(out, d)
	}
	return out, nil
}

func (s *WebhookStore) ListWebhookDeliveries(ctx context.Context, filter store.WebhookFilter) ([]model.WebhookDelivery, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	all, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	var matched []model.WebhookDelivery
	for _, d := range all {
		if filter.Provider != "" && d.Provider != filter.Provider {
			continue
		}
		if filter.Status != "" && d.Status != filter.Status {
			continue
		}
		if filter.App != "" && d.App != filter.App {
			continue
		}
		matched = append(matched, d)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ReceivedAt.After(matched[j].ReceivedAt) })
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

func (s *WebhookStore) WebhookMetrics(ctx context.Context) ([]store.WebhookMetric, error) {
	all, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	type key struct{ provider, status string }
	agg := map[key]*store.WebhookMetric{}
	for _, d := range all {
		k := key{d.Provider, d.Status}
		m, ok := agg[k]
		if !ok {
			m = &store.WebhookMetric{Provider: d.Provider, Status: d.Status}
			agg[k] = m
		}
		m.Count++
		if received := float64(d.ReceivedAt.Unix()); received > m.LastReceivedUnix {
			m.LastReceivedUnix = received
		}
	}
	out := make([]store.WebhookMetric, 0, len(agg))
	for _, m := range agg {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Status < out[j].Status
	})
	return out, nil
}
