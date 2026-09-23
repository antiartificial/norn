package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

// FleetGitHubDispatchStore is an etcd-backed store.FleetGitHubDispatchStore. Each
// dispatch is a single JSON value under prefix/ghdispatch/<planID>. Create fails
// if the plan already has a dispatch (matching the PG unique key); Finish matches
// only when the stored nonce hash equals the supplied one, both CAS-guarded. It
// passes the shared conformance suite.
type FleetGitHubDispatchStore struct {
	kv     clientv3.KV
	prefix string
}

// NewFleetGitHubDispatchStore returns an etcd dispatch store rooted at prefix.
func NewFleetGitHubDispatchStore(kv clientv3.KV, prefix string) *FleetGitHubDispatchStore {
	return &FleetGitHubDispatchStore{kv: kv, prefix: prefix}
}

var _ store.FleetGitHubDispatchStore = (*FleetGitHubDispatchStore)(nil)

func (s *FleetGitHubDispatchStore) key(planID string) string { return s.prefix + "/ghdispatch/" + planID }

func (s *FleetGitHubDispatchStore) load(ctx context.Context, planID string) (*store.FleetGitHubDispatch, int64, error) {
	resp, err := s.kv.Get(ctx, s.key(planID))
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, ErrNotFound
	}
	var item store.FleetGitHubDispatch
	if err := json.Unmarshal(resp.Kvs[0].Value, &item); err != nil {
		return nil, 0, err
	}
	return &item, resp.Kvs[0].ModRevision, nil
}

func (s *FleetGitHubDispatchStore) GetFleetGitHubDispatch(ctx context.Context, planID string) (*store.FleetGitHubDispatch, error) {
	item, _, err := s.load(ctx, planID)
	return item, err
}

func (s *FleetGitHubDispatchStore) CreateFleetGitHubDispatch(ctx context.Context, item store.FleetGitHubDispatch) (*store.FleetGitHubDispatch, error) {
	now := time.Now()
	item.CreatedAt = now
	item.UpdatedAt = now
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	resp, err := s.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(s.key(item.PlanID)), "=", 0)).
		Then(clientv3.OpPut(s.key(item.PlanID), string(raw))).
		Commit()
	if err != nil {
		return nil, err
	}
	if !resp.Succeeded {
		return nil, fmt.Errorf("etcdstore: dispatch for plan %s already exists", item.PlanID)
	}
	return s.GetFleetGitHubDispatch(ctx, item.PlanID)
}

// FinishFleetGitHubDispatch records the run id and workflow URL only when the
// stored nonce hash matches; a missing dispatch or a nonce mismatch yields an
// error, matching the PG UPDATE ... WHERE plan_id AND dispatch_nonce_sha256
// RETURNING that produces no row.
func (s *FleetGitHubDispatchStore) FinishFleetGitHubDispatch(ctx context.Context, planID, nonceHash string, runID int64, workflowURL string) (*store.FleetGitHubDispatch, error) {
	for attempt := 0; attempt < 32; attempt++ {
		item, rev, err := s.load(ctx, planID)
		if err != nil {
			return nil, err
		}
		if item.DispatchNonceSHA256 != nonceHash {
			return nil, ErrNotFound
		}
		item.RunID = runID
		item.WorkflowURL = workflowURL
		item.UpdatedAt = time.Now()
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		resp, err := s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(s.key(planID)), "=", rev)).
			Then(clientv3.OpPut(s.key(planID), string(raw))).
			Commit()
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			return item, nil
		}
	}
	return nil, ErrNotFound
}
