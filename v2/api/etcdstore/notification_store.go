package etcdstore

import (
	"context"
	"encoding/json"
	"sort"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// NotificationStore is an etcd-backed store.NotificationStore. Each channel is a
// single JSON value under prefix/notify/<id>. It passes the shared conformance
// suite; there is no cross-key transaction because the concern is a single
// isolated table.
type NotificationStore struct {
	kv     clientv3.KV
	prefix string
}

// NewNotificationStore returns an etcd notification store rooted at prefix.
func NewNotificationStore(kv clientv3.KV, prefix string) *NotificationStore {
	return &NotificationStore{kv: kv, prefix: prefix}
}

var _ store.NotificationStore = (*NotificationStore)(nil)

func (s *NotificationStore) key(id string) string { return s.prefix + "/notify/" + id }
func (s *NotificationStore) keyPrefix() string     { return s.prefix + "/notify/" }

func (s *NotificationStore) InsertNotificationChannel(ctx context.Context, ch *model.NotificationChannel) error {
	raw, err := json.Marshal(ch)
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, s.key(ch.ID), string(raw))
	return err
}

func (s *NotificationStore) ListNotificationChannels(ctx context.Context) ([]model.NotificationChannel, error) {
	resp, err := s.kv.Get(ctx, s.keyPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]model.NotificationChannel, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var ch model.NotificationChannel
		if err := json.Unmarshal(kv.Value, &ch); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *NotificationStore) GetNotificationChannel(ctx context.Context, id string) (*model.NotificationChannel, error) {
	resp, err := s.kv.Get(ctx, s.key(id))
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, ErrNotFound
	}
	var ch model.NotificationChannel
	if err := json.Unmarshal(resp.Kvs[0].Value, &ch); err != nil {
		return nil, err
	}
	return &ch, nil
}

func (s *NotificationStore) DeleteNotificationChannel(ctx context.Context, id string) error {
	_, err := s.kv.Delete(ctx, s.key(id))
	return err
}
