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

// ErrNotFound mirrors the "no such row" result the PostgreSQL adapter surfaces
// for single-record lookups that miss.
var ErrNotFound = errors.New("etcdstore: not found")

// OperationStore is an etcd-backed store.OperationStore. Each operation is a
// single JSON value under prefix/ops/<id>. Claim exclusivity and every mutation
// use an etcd compare-and-swap transaction on the record's ModRevision, so two
// workers racing for the same operation cannot both win — the loser's CAS fails
// and it re-scans. This reproduces the same fencing the PostgreSQL adapter gets
// from SELECT ... FOR UPDATE SKIP LOCKED, and it passes the same conformance
// suite. Lookups without a key (idempotency, deployment/promotion, list,
// recovery, metrics) scan the prefix and filter in memory; etcd has no
// secondary index, which is acceptable for control-plane-sized state.
type OperationStore struct {
	kv     clientv3.KV
	prefix string
}

// NewOperationStore returns an etcd operation store rooted at prefix.
func NewOperationStore(kv clientv3.KV, prefix string) *OperationStore {
	return &OperationStore{kv: kv, prefix: prefix}
}

// Legacy adapter surface imported from the durable branch. V3 wiring is added in a follow-up adapter.
var _ = (*OperationStore)(nil)

func (s *OperationStore) opKey(id string) string    { return s.prefix + "/ops/" + id }
func (s *OperationStore) opsPrefix() string         { return s.prefix + "/ops/" }
func (s *OperationStore) lockKey(app string) string { return s.prefix + "/applock/" + app }

type opRev struct {
	op  *model.Operation
	rev int64
}

func (s *OperationStore) load(ctx context.Context, id string) (*model.Operation, int64, error) {
	resp, err := s.kv.Get(ctx, s.opKey(id))
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, ErrNotFound
	}
	var op model.Operation
	if err := json.Unmarshal(resp.Kvs[0].Value, &op); err != nil {
		return nil, 0, err
	}
	hydrate(&op)
	return &op, resp.Kvs[0].ModRevision, nil
}

// hydrate restores the non-nil empty Payload/Metadata maps that the PostgreSQL
// adapter always returns. JSON marshaling drops empty maps (omitempty), so
// without this a round-tripped record would come back with nil maps.
func hydrate(op *model.Operation) {
	if op.Payload == nil {
		op.Payload = map[string]interface{}{}
	}
	if op.Metadata == nil {
		op.Metadata = map[string]interface{}{}
	}
}

func (s *OperationStore) put(ctx context.Context, op *model.Operation) error {
	value, err := json.Marshal(op)
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, s.opKey(op.ID), string(value))
	return err
}

// casPut writes op only if the stored record still has the observed revision.
func (s *OperationStore) casPut(ctx context.Context, op *model.Operation, rev int64) (bool, error) {
	value, err := json.Marshal(op)
	if err != nil {
		return false, err
	}
	resp, err := s.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(s.opKey(op.ID)), "=", rev)).
		Then(clientv3.OpPut(s.opKey(op.ID), string(value))).
		Commit()
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}

func (s *OperationStore) scanAll(ctx context.Context) ([]opRev, error) {
	resp, err := s.kv.Get(ctx, s.opsPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]opRev, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var op model.Operation
		if err := json.Unmarshal(kv.Value, &op); err != nil {
			return nil, err
		}
		hydrate(&op)
		out = append(out, opRev{op: &op, rev: kv.ModRevision})
	}
	return out, nil
}

// update applies apply to the record under id with optimistic retry. apply
// returns false to skip the write (condition not met).
func (s *OperationStore) update(ctx context.Context, id string, apply func(op *model.Operation) bool) error {
	for attempt := 0; attempt < 32; attempt++ {
		op, rev, err := s.load(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !apply(op) {
			return nil
		}
		ok, err := s.casPut(ctx, op, rev)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("update %s: exhausted retries under contention", id)
}

func normalizeOperation(op *model.Operation) {
	if op.Metadata == nil {
		op.Metadata = map[string]interface{}{}
	}
	if op.Payload == nil {
		op.Payload = map[string]interface{}{}
	}
	if op.Status == "" {
		op.Status = model.OperationQueued
	}
	if op.StartedAt.IsZero() {
		op.StartedAt = time.Now()
	}
	if op.MaxAttempts <= 0 {
		op.MaxAttempts = 1
	}
	if op.NextAttemptAt.IsZero() {
		op.NextAttemptAt = op.StartedAt
	}
	op.UpdatedAt = time.Now()
}

func mergeMetadata(op *model.Operation, patch map[string]interface{}) {
	if op.Metadata == nil {
		op.Metadata = map[string]interface{}{}
	}
	for k, v := range patch {
		op.Metadata[k] = v
	}
}

func (s *OperationStore) AcquireAppOperationLock(ctx context.Context, app string) (store.AppOperationLock, bool, error) {
	// This legacy adapter receives only clientv3.KV, so it cannot attach its
	// key to a lease or observe ownership loss. Refuse app execution rather
	// than recreating the crash-persistent lock it used to expose. V3 callers
	// must use V3OperationStore, whose leasedKV boundary provides both.
	return nil, false, errors.New("legacy etcd app operation lock is unavailable without lease support")
}

func (s *OperationStore) InsertOperation(ctx context.Context, op *model.Operation) error {
	stored := *op
	normalizeOperation(&stored)
	return s.put(ctx, &stored)
}

func (s *OperationStore) InsertCompletedOperation(ctx context.Context, op *model.Operation) error {
	if op == nil {
		return errors.New("operation is required")
	}
	if !op.Status.Terminal() {
		return errors.New("completed operation must have a terminal status")
	}
	stored := *op
	normalizeOperation(&stored)
	if stored.FinishedAt == nil {
		finished := stored.StartedAt
		stored.FinishedAt = &finished
	}
	return s.put(ctx, &stored)
}

func (s *OperationStore) GetOperation(ctx context.Context, id string) (*model.Operation, error) {
	op, _, err := s.load(ctx, id)
	return op, err
}

func (s *OperationStore) GetOperationByIdempotencyKey(ctx context.Context, key string) (*model.Operation, error) {
	all, err := s.scanAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range all {
		if v, ok := item.op.Metadata["idempotencyKey"].(string); ok && v == key {
			return item.op, nil
		}
	}
	return nil, ErrNotFound
}

func (s *OperationStore) GetOperationByPromotionQualificationID(ctx context.Context, qualificationID string) (*model.Operation, error) {
	all, err := s.scanAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range all {
		if item.op.Kind != "app.deploy" {
			continue
		}
		if q, ok := item.op.Metadata["promotionQualification"].(map[string]interface{}); ok {
			if id, ok := q["id"].(string); ok && id == qualificationID {
				return item.op, nil
			}
		}
	}
	return nil, ErrNotFound
}

func (s *OperationStore) GetReleaseOperationByDeploymentID(ctx context.Context, deploymentID string) (*model.Operation, error) {
	return s.findByDeploymentID(ctx, deploymentID, false)
}

func (s *OperationStore) GetPromotionOperationByDeploymentID(ctx context.Context, deploymentID string) (*model.Operation, error) {
	return s.findByDeploymentID(ctx, deploymentID, true)
}

func (s *OperationStore) findByDeploymentID(ctx context.Context, deploymentID string, promotion bool) (*model.Operation, error) {
	all, err := s.scanAll(ctx)
	if err != nil {
		return nil, err
	}
	var matches []*model.Operation
	for _, item := range all {
		op := item.op
		if op.Kind != "app.deploy" {
			continue
		}
		if id, ok := op.Payload["deploymentId"].(string); !ok || id != deploymentID {
			continue
		}
		if promotion {
			if op.Status != model.OperationSucceeded {
				continue
			}
			if _, ok := op.Metadata["promotionQualification"]; !ok {
				continue
			}
		}
		matches = append(matches, op)
	}
	if len(matches) == 0 {
		return nil, ErrNotFound
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].StartedAt.After(matches[j].StartedAt) })
	return matches[0], nil
}

func (s *OperationStore) ListOperations(ctx context.Context, filter store.OperationFilter) ([]model.Operation, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	all, err := s.scanAll(ctx)
	if err != nil {
		return nil, err
	}
	var matched []*model.Operation
	for _, item := range all {
		op := item.op
		if filter.App != "" && op.App != filter.App {
			continue
		}
		if filter.Kind != "" && op.Kind != filter.Kind {
			continue
		}
		if filter.Ref != "" && op.Ref != filter.Ref {
			continue
		}
		if filter.Status != "" && string(op.Status) != filter.Status {
			continue
		}
		if filter.ExcludeID != "" && op.ID == filter.ExcludeID {
			continue
		}
		if filter.Active && op.Status != model.OperationQueued && op.Status != model.OperationRunning {
			continue
		}
		matched = append(matched, op)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].StartedAt.After(matched[j].StartedAt) })
	out := []model.Operation{}
	for _, op := range matched {
		if len(out) >= limit {
			break
		}
		out = append(out, *op)
	}
	return out, nil
}

func (s *OperationStore) ClaimNextOperation(ctx context.Context, workerID string, lease time.Duration, kinds []string) (*model.Operation, error) {
	kindSet := map[string]bool{}
	for _, k := range kinds {
		kindSet[k] = true
	}
	for attempt := 0; attempt < 32; attempt++ {
		now := time.Now()
		all, err := s.scanAll(ctx)
		if err != nil {
			return nil, err
		}
		var candidate *opRev
		for i := range all {
			op := all[i].op
			if op.Status != model.OperationQueued {
				continue
			}
			if op.NextAttemptAt.After(now) {
				continue
			}
			if op.Attempts >= op.MaxAttempts {
				continue
			}
			if op.LockedUntil != nil && op.LockedUntil.After(now) {
				continue
			}
			if len(kindSet) > 0 && !kindSet[op.Kind] {
				continue
			}
			if candidate == nil || op.StartedAt.Before(candidate.op.StartedAt) {
				candidate = &all[i]
			}
		}
		if candidate == nil {
			return nil, nil
		}
		claimed := candidate.op
		claimed.Status = model.OperationRunning
		claimed.Attempts++
		claimed.LockedBy = workerID
		until := now.Add(lease)
		claimed.LockedUntil = &until
		claimed.UpdatedAt = now
		ok, err := s.casPut(ctx, claimed, candidate.rev)
		if err != nil {
			return nil, err
		}
		if ok {
			return claimed, nil
		}
		// Lost the race for this candidate; re-scan and try again.
	}
	return nil, nil
}

func (s *OperationStore) RenewOperationLease(ctx context.Context, id, workerID string, until time.Time) error {
	return s.update(ctx, id, func(op *model.Operation) bool {
		if op.Status != model.OperationRunning || op.LockedBy != workerID {
			return false
		}
		lease := until
		op.LockedUntil = &lease
		op.UpdatedAt = time.Now()
		return true
	})
}

func (s *OperationStore) FinishOperation(ctx context.Context, id string, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	return s.update(ctx, id, func(op *model.Operation) bool {
		now := time.Now()
		op.Status = status
		op.Message = message
		mergeMetadata(op, metadata)
		op.LockedBy = ""
		op.LockedUntil = nil
		op.UpdatedAt = now
		op.FinishedAt = &now
		return true
	})
}

func (s *OperationStore) FinishOperationBySaga(ctx context.Context, sagaID string, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	all, err := s.scanAll(ctx)
	if err != nil {
		return err
	}
	for _, item := range all {
		if item.op.SagaID != sagaID {
			continue
		}
		if item.op.Status != model.OperationQueued && item.op.Status != model.OperationRunning {
			continue
		}
		id := item.op.ID
		if err := s.update(ctx, id, func(op *model.Operation) bool {
			if op.Status != model.OperationQueued && op.Status != model.OperationRunning {
				return false
			}
			now := time.Now()
			op.Status = status
			op.Message = message
			mergeMetadata(op, metadata)
			op.LockedBy = ""
			op.LockedUntil = nil
			op.UpdatedAt = now
			op.FinishedAt = &now
			return true
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *OperationStore) RetryOperation(ctx context.Context, id, message, lastError string, nextAttemptAt time.Time, metadata map[string]interface{}) error {
	return s.update(ctx, id, func(op *model.Operation) bool {
		op.Status = model.OperationQueued
		op.Message = message
		op.LastError = lastError
		op.NextAttemptAt = nextAttemptAt
		mergeMetadata(op, metadata)
		op.LockedBy = ""
		op.LockedUntil = nil
		op.UpdatedAt = time.Now()
		return true
	})
}

func (s *OperationStore) DeferClaimedOperation(ctx context.Context, id, message string, nextAttemptAt time.Time, metadata map[string]interface{}) error {
	return s.update(ctx, id, func(op *model.Operation) bool {
		if op.Status != model.OperationRunning {
			return false
		}
		op.Status = model.OperationQueued
		op.Message = message
		op.NextAttemptAt = nextAttemptAt
		mergeMetadata(op, metadata)
		if op.Attempts > 0 {
			op.Attempts--
		}
		op.LockedBy = ""
		op.LockedUntil = nil
		op.UpdatedAt = time.Now()
		return true
	})
}

func (s *OperationStore) CancelQueuedOperation(ctx context.Context, id, requestedBy string) (*model.Operation, bool, error) {
	for attempt := 0; attempt < 32; attempt++ {
		op, rev, err := s.load(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil, false, ErrNotFound
		}
		if err != nil {
			return nil, false, err
		}
		if op.Status != model.OperationQueued {
			return op, false, nil
		}
		now := time.Now()
		op.Status = model.OperationCanceled
		op.Message = "operation canceled before execution"
		mergeMetadata(op, map[string]interface{}{"canceledBy": requestedBy})
		op.LockedBy = ""
		op.LockedUntil = nil
		op.UpdatedAt = now
		op.FinishedAt = &now
		ok, err := s.casPut(ctx, op, rev)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return op, true, nil
		}
	}
	return nil, false, fmt.Errorf("cancel %s: exhausted retries under contention", id)
}

func (s *OperationStore) RecoverInFlightOperations(ctx context.Context) error {
	all, err := s.scanAll(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, item := range all {
		op := item.op
		if op.Status != model.OperationRunning || !strings.HasPrefix(op.Kind, "app.") {
			continue
		}
		if op.LockedUntil != nil && op.LockedUntil.After(now) {
			continue
		}
		id := op.ID
		if err := s.update(ctx, id, func(op *model.Operation) bool {
			if op.Status != model.OperationRunning || !strings.HasPrefix(op.Kind, "app.") {
				return false
			}
			if op.LockedUntil != nil && op.LockedUntil.After(time.Now()) {
				return false
			}
			ts := time.Now()
			if op.Attempts < op.MaxAttempts {
				op.Status = model.OperationQueued
				op.Message = "operation recovered after API restart and queued for a safe retry"
				mergeMetadata(op, map[string]interface{}{"recoveredAfterRestart": true})
				op.LockedBy = ""
				op.LockedUntil = nil
				op.NextAttemptAt = ts
				op.UpdatedAt = ts
				return true
			}
			op.Status = model.OperationFailed
			op.Message = "operation interrupted after a non-retryable stage; manual review required"
			op.LastError = "operation executor lease expired"
			mergeMetadata(op, map[string]interface{}{"manualRecoveryRequired": true})
			op.LockedBy = ""
			op.LockedUntil = nil
			op.UpdatedAt = ts
			op.FinishedAt = &ts
			return true
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *OperationStore) RecoverMaintenanceOperations(ctx context.Context) error {
	all, err := s.scanAll(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, item := range all {
		op := item.op
		if op.Status != model.OperationRunning {
			continue
		}
		if !strings.HasPrefix(op.Kind, "platform.") && !strings.HasPrefix(op.Kind, "host.") {
			continue
		}
		if op.LockedUntil != nil && op.LockedUntil.After(now) {
			continue
		}
		id := op.ID
		if err := s.update(ctx, id, func(op *model.Operation) bool {
			if op.Status != model.OperationRunning {
				return false
			}
			if !strings.HasPrefix(op.Kind, "platform.") && !strings.HasPrefix(op.Kind, "host.") {
				return false
			}
			if op.LockedUntil != nil && op.LockedUntil.After(time.Now()) {
				return false
			}
			ts := time.Now()
			op.Status = model.OperationFailed
			op.Message = "maintenance executor stopped before recording completion; inspect host state before retrying"
			op.LastError = "maintenance executor lease expired"
			op.LockedBy = ""
			op.LockedUntil = nil
			op.UpdatedAt = ts
			op.FinishedAt = &ts
			return true
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *OperationStore) OperationMetrics(ctx context.Context) ([]store.OperationMetric, error) {
	all, err := s.scanAll(ctx)
	if err != nil {
		return nil, err
	}
	type key struct {
		kind   string
		status model.OperationStatus
	}
	agg := map[key]*store.OperationMetric{}
	for _, item := range all {
		op := item.op
		k := key{op.Kind, op.Status}
		m, ok := agg[k]
		if !ok {
			m = &store.OperationMetric{Kind: op.Kind, Status: op.Status}
			agg[k] = m
		}
		m.Count++
		if op.FinishedAt != nil {
			m.DurationSeconds += op.FinishedAt.Sub(op.StartedAt).Seconds()
		}
		if started := float64(op.StartedAt.Unix()); started > m.LastStartedUnix {
			m.LastStartedUnix = started
		}
	}
	out := make([]store.OperationMetric, 0, len(agg))
	for _, m := range agg {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Status < out[j].Status
	})
	return out, nil
}
