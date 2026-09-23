package etcdstore

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

// CronStore is an etcd-backed store.CronStore. Each state is a single JSON value
// under prefix/cron/<app>/<process>; GetCronStates scans the per-app prefix. It
// passes the shared conformance suite.
type CronStore struct {
	kv     clientv3.KV
	prefix string
}

// NewCronStore returns an etcd cron store rooted at prefix.
func NewCronStore(kv clientv3.KV, prefix string) *CronStore {
	return &CronStore{kv: kv, prefix: prefix}
}

var _ store.CronStore = (*CronStore)(nil)

func (s *CronStore) key(app, process string) string { return s.prefix + "/cron/" + app + "/" + process }
func (s *CronStore) appPrefix(app string) string     { return s.prefix + "/cron/" + app + "/" }

func (s *CronStore) GetCronState(ctx context.Context, app, process string) (*store.CronState, error) {
	resp, err := s.kv.Get(ctx, s.key(app, process))
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, ErrNotFound
	}
	var cs store.CronState
	if err := json.Unmarshal(resp.Kvs[0].Value, &cs); err != nil {
		return nil, err
	}
	return &cs, nil
}

func (s *CronStore) GetCronStates(ctx context.Context, app string) ([]store.CronState, error) {
	resp, err := s.kv.Get(ctx, s.appPrefix(app), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]store.CronState, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var cs store.CronState
		if err := json.Unmarshal(kv.Value, &cs); err != nil {
			return nil, err
		}
		out = append(out, cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Process < out[j].Process })
	return out, nil
}

func (s *CronStore) UpsertCronState(ctx context.Context, app, process string, paused bool, schedule string) error {
	cs := store.CronState{App: app, Process: process, Paused: paused, Schedule: schedule, UpdatedAt: time.Now()}
	raw, err := json.Marshal(cs)
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, s.key(app, process), string(raw))
	return err
}
