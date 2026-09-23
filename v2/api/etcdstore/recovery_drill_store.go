package etcdstore

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

// RecoveryDrillStore is an etcd-backed store.RecoveryDrillStore. Each drill is a
// single JSON value under prefix/drill/<id>. FinishRecoveryDrill transitions only
// a still-running drill (CAS-guarded); LatestPassedRecoveryDrills scans and
// reduces to the newest passed finish per kind. It passes the shared suite.
type RecoveryDrillStore struct {
	kv     clientv3.KV
	prefix string
}

// NewRecoveryDrillStore returns an etcd recovery-drill store rooted at prefix.
func NewRecoveryDrillStore(kv clientv3.KV, prefix string) *RecoveryDrillStore {
	return &RecoveryDrillStore{kv: kv, prefix: prefix}
}

var _ store.RecoveryDrillStore = (*RecoveryDrillStore)(nil)

func (s *RecoveryDrillStore) key(id string) string { return s.prefix + "/drill/" + id }
func (s *RecoveryDrillStore) keyPrefix() string     { return s.prefix + "/drill/" }

func hydrateDrill(d *store.RecoveryDrill) {
	if d.Evidence == nil {
		d.Evidence = map[string]string{}
	}
}

func (s *RecoveryDrillStore) InsertRecoveryDrill(ctx context.Context, drill *store.RecoveryDrill) error {
	stored := *drill
	stored.Status = "running"
	hydrateDrill(&stored)
	raw, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, s.key(stored.ID), string(raw))
	return err
}

func (s *RecoveryDrillStore) load(ctx context.Context, id string) (*store.RecoveryDrill, int64, error) {
	resp, err := s.kv.Get(ctx, s.key(id))
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, ErrNotFound
	}
	var d store.RecoveryDrill
	if err := json.Unmarshal(resp.Kvs[0].Value, &d); err != nil {
		return nil, 0, err
	}
	hydrateDrill(&d)
	return &d, resp.Kvs[0].ModRevision, nil
}

// FinishRecoveryDrill transitions a still-running drill and returns it; a drill
// that is missing or no longer running yields an error, matching the PostgreSQL
// UPDATE ... WHERE status='running' RETURNING that produces no row.
func (s *RecoveryDrillStore) FinishRecoveryDrill(ctx context.Context, id, status string, evidence map[string]string, finishedAt time.Time) (*store.RecoveryDrill, error) {
	for attempt := 0; attempt < 32; attempt++ {
		d, rev, err := s.load(ctx, id)
		if err != nil {
			return nil, err
		}
		if d.Status != "running" {
			return nil, ErrNotFound
		}
		d.Status = status
		d.Evidence = evidence
		hydrateDrill(d)
		fin := finishedAt
		d.FinishedAt = &fin
		raw, err := json.Marshal(d)
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
			return d, nil
		}
	}
	return nil, ErrNotFound
}

func (s *RecoveryDrillStore) GetRecoveryDrill(ctx context.Context, id string) (*store.RecoveryDrill, error) {
	d, _, err := s.load(ctx, id)
	return d, err
}

func (s *RecoveryDrillStore) scan(ctx context.Context) ([]store.RecoveryDrill, error) {
	resp, err := s.kv.Get(ctx, s.keyPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]store.RecoveryDrill, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var d store.RecoveryDrill
		if err := json.Unmarshal(kv.Value, &d); err != nil {
			return nil, err
		}
		hydrateDrill(&d)
		out = append(out, d)
	}
	return out, nil
}

func (s *RecoveryDrillStore) ListRecoveryDrills(ctx context.Context, limit int) ([]store.RecoveryDrill, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	all, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].StartedAt.After(all[j].StartedAt) })
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (s *RecoveryDrillStore) LatestPassedRecoveryDrills(ctx context.Context) (map[string]time.Time, error) {
	all, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for _, d := range all {
		if d.Status != "passed" || d.FinishedAt == nil {
			continue
		}
		if cur, ok := out[d.Kind]; !ok || d.FinishedAt.After(cur) {
			out[d.Kind] = *d.FinishedAt
		}
	}
	return out, nil
}
