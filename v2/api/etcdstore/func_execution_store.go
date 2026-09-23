package etcdstore

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

// FuncExecutionStore is an etcd-backed store.FuncExecutionStore. Each execution
// is a single JSON value under prefix/func/<id>; ListFuncExecutions scans the
// prefix and filters by app. It passes the shared conformance suite.
type FuncExecutionStore struct {
	kv     clientv3.KV
	prefix string
}

// NewFuncExecutionStore returns an etcd function-execution store rooted at prefix.
func NewFuncExecutionStore(kv clientv3.KV, prefix string) *FuncExecutionStore {
	return &FuncExecutionStore{kv: kv, prefix: prefix}
}

var _ store.FuncExecutionStore = (*FuncExecutionStore)(nil)

func (s *FuncExecutionStore) key(id string) string { return s.prefix + "/func/" + id }
func (s *FuncExecutionStore) keyPrefix() string     { return s.prefix + "/func/" }

func (s *FuncExecutionStore) InsertFuncExecution(ctx context.Context, fe *store.FuncExecution) error {
	raw, err := json.Marshal(fe)
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, s.key(fe.ID), string(raw))
	return err
}

// UpdateFuncExecution sets the terminal fields on an existing record; a missing
// record is a silent no-op, matching the PostgreSQL UPDATE affecting zero rows.
func (s *FuncExecutionStore) UpdateFuncExecution(ctx context.Context, id, status string, exitCode int, durationMs int64) error {
	for attempt := 0; attempt < 32; attempt++ {
		resp, err := s.kv.Get(ctx, s.key(id))
		if err != nil {
			return err
		}
		if len(resp.Kvs) == 0 {
			return nil
		}
		var fe store.FuncExecution
		if err := json.Unmarshal(resp.Kvs[0].Value, &fe); err != nil {
			return err
		}
		rev := resp.Kvs[0].ModRevision
		fe.Status = status
		ec := exitCode
		fe.ExitCode = &ec
		now := time.Now()
		fe.FinishedAt = &now
		dm := durationMs
		fe.DurationMs = &dm
		raw, err := json.Marshal(fe)
		if err != nil {
			return err
		}
		txn, err := s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(s.key(id)), "=", rev)).
			Then(clientv3.OpPut(s.key(id), string(raw))).
			Commit()
		if err != nil {
			return err
		}
		if txn.Succeeded {
			return nil
		}
	}
	return nil
}

func (s *FuncExecutionStore) ListFuncExecutions(ctx context.Context, app string, limit int) ([]store.FuncExecution, error) {
	if limit <= 0 {
		limit = 20
	}
	resp, err := s.kv.Get(ctx, s.keyPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	var matched []store.FuncExecution
	for _, kv := range resp.Kvs {
		var fe store.FuncExecution
		if err := json.Unmarshal(kv.Value, &fe); err != nil {
			return nil, err
		}
		if app != "" && fe.App != app {
			continue
		}
		matched = append(matched, fe)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].StartedAt.After(matched[j].StartedAt) })
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}
