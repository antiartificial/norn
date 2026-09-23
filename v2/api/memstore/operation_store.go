package memstore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// ErrNotFound mirrors the "no such row" result the PostgreSQL adapter surfaces
// (pgx.ErrNoRows) for single-record lookups that miss.
var ErrNotFound = errors.New("memstore: not found")

// OperationStore is an in-memory implementation of store.OperationStore. It
// reproduces the same claim-exclusivity, lease-fencing, idempotency, terminal
// and interrupted-operation-recovery invariants as the PostgreSQL adapter, and
// passes the same conformance suite — proving the hardest control boundary is
// backend-neutral, not just the event log. It is a test double / explicitly-
// non-durable local backend, not a production store.
type OperationStore struct {
	mu       sync.Mutex
	ops      map[string]*model.Operation
	appLocks map[string]bool
}

// NewOperationStore returns an empty in-memory operation store.
func NewOperationStore() *OperationStore {
	return &OperationStore{ops: map[string]*model.Operation{}, appLocks: map[string]bool{}}
}

// Compile-time proof that the in-memory adapter satisfies the operations boundary.
var _ store.OperationStore = (*OperationStore)(nil)

func cloneOp(op *model.Operation) *model.Operation {
	if op == nil {
		return nil
	}
	clone := *op
	clone.Payload = cloneMap(op.Payload)
	clone.Metadata = cloneMap(op.Metadata)
	if op.LockedUntil != nil {
		t := *op.LockedUntil
		clone.LockedUntil = &t
	}
	if op.FinishedAt != nil {
		t := *op.FinishedAt
		clone.FinishedAt = &t
	}
	return &clone
}

func cloneMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergeMetadata(op *model.Operation, patch map[string]interface{}) {
	if op.Metadata == nil {
		op.Metadata = map[string]interface{}{}
	}
	for k, v := range patch {
		op.Metadata[k] = v
	}
}

func (s *OperationStore) AcquireAppOperationLock(_ context.Context, app string) (func(), bool, error) {
	if strings.TrimSpace(app) == "" {
		return func() {}, false, errors.New("app operation lock is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appLocks[app] {
		return func() {}, false, nil
	}
	s.appLocks[app] = true
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.appLocks, app)
	}, true, nil
}

func (s *OperationStore) InsertOperation(_ context.Context, op *model.Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := cloneOp(op)
	normalizeOperation(stored)
	s.ops[stored.ID] = stored
	return nil
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

func (s *OperationStore) InsertCompletedOperation(_ context.Context, op *model.Operation) error {
	if op == nil {
		return errors.New("operation is required")
	}
	if !op.Status.Terminal() {
		return errors.New("completed operation must have a terminal status")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := cloneOp(op)
	normalizeOperation(stored)
	if stored.FinishedAt == nil {
		finished := stored.StartedAt
		stored.FinishedAt = &finished
	}
	s.ops[stored.ID] = stored
	return nil
}

func (s *OperationStore) GetOperation(_ context.Context, id string) (*model.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.ops[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneOp(op), nil
}

func (s *OperationStore) GetOperationByIdempotencyKey(_ context.Context, key string) (*model.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range s.ops {
		if v, ok := op.Metadata["idempotencyKey"]; ok {
			if str, ok := v.(string); ok && str == key {
				return cloneOp(op), nil
			}
		}
	}
	return nil, ErrNotFound
}

func (s *OperationStore) GetOperationByPromotionQualificationID(_ context.Context, qualificationID string) (*model.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range s.ops {
		if op.Kind != "app.deploy" {
			continue
		}
		if q, ok := op.Metadata["promotionQualification"].(map[string]interface{}); ok {
			if id, ok := q["id"].(string); ok && id == qualificationID {
				return cloneOp(op), nil
			}
		}
	}
	return nil, ErrNotFound
}

func (s *OperationStore) GetReleaseOperationByDeploymentID(_ context.Context, deploymentID string) (*model.Operation, error) {
	return s.findByDeploymentID(deploymentID, false)
}

func (s *OperationStore) GetPromotionOperationByDeploymentID(_ context.Context, deploymentID string) (*model.Operation, error) {
	return s.findByDeploymentID(deploymentID, true)
}

func (s *OperationStore) findByDeploymentID(deploymentID string, promotion bool) (*model.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var matches []*model.Operation
	for _, op := range s.ops {
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
	return cloneOp(matches[0]), nil
}

func (s *OperationStore) ListOperations(_ context.Context, filter store.OperationFilter) ([]model.Operation, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var matched []*model.Operation
	for _, op := range s.ops {
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
		out = append(out, *cloneOp(op))
	}
	return out, nil
}

func (s *OperationStore) ClaimNextOperation(_ context.Context, workerID string, lease time.Duration, kinds []string) (*model.Operation, error) {
	now := time.Now()
	kindSet := map[string]bool{}
	for _, k := range kinds {
		kindSet[k] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var candidate *model.Operation
	for _, op := range s.ops {
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
		if candidate == nil || op.StartedAt.Before(candidate.StartedAt) {
			candidate = op
		}
	}
	if candidate == nil {
		return nil, nil
	}
	candidate.Status = model.OperationRunning
	candidate.Attempts++
	candidate.LockedBy = workerID
	until := now.Add(lease)
	candidate.LockedUntil = &until
	candidate.UpdatedAt = now
	return cloneOp(candidate), nil
}

func (s *OperationStore) RenewOperationLease(_ context.Context, id, workerID string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.ops[id]
	if !ok || op.Status != model.OperationRunning || op.LockedBy != workerID {
		return nil
	}
	lease := until
	op.LockedUntil = &lease
	op.UpdatedAt = time.Now()
	return nil
}

func (s *OperationStore) FinishOperation(_ context.Context, id string, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.ops[id]
	if !ok {
		return nil
	}
	now := time.Now()
	op.Status = status
	op.Message = message
	mergeMetadata(op, metadata)
	op.LockedBy = ""
	op.LockedUntil = nil
	op.UpdatedAt = now
	op.FinishedAt = &now
	return nil
}

func (s *OperationStore) FinishOperationBySaga(_ context.Context, sagaID string, status model.OperationStatus, message string, metadata map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, op := range s.ops {
		if op.SagaID != sagaID {
			continue
		}
		if op.Status != model.OperationQueued && op.Status != model.OperationRunning {
			continue
		}
		op.Status = status
		op.Message = message
		mergeMetadata(op, metadata)
		op.LockedBy = ""
		op.LockedUntil = nil
		op.UpdatedAt = now
		op.FinishedAt = &now
	}
	return nil
}

func (s *OperationStore) RetryOperation(_ context.Context, id, message, lastError string, nextAttemptAt time.Time, metadata map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.ops[id]
	if !ok {
		return nil
	}
	op.Status = model.OperationQueued
	op.Message = message
	op.LastError = lastError
	op.NextAttemptAt = nextAttemptAt
	mergeMetadata(op, metadata)
	op.LockedBy = ""
	op.LockedUntil = nil
	op.UpdatedAt = time.Now()
	return nil
}

func (s *OperationStore) DeferClaimedOperation(_ context.Context, id, message string, nextAttemptAt time.Time, metadata map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.ops[id]
	if !ok || op.Status != model.OperationRunning {
		return nil
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
	return nil
}

func (s *OperationStore) CancelQueuedOperation(_ context.Context, id, requestedBy string) (*model.Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.ops[id]
	if !ok {
		return nil, false, ErrNotFound
	}
	if op.Status != model.OperationQueued {
		return cloneOp(op), false, nil
	}
	now := time.Now()
	op.Status = model.OperationCanceled
	op.Message = "operation canceled before execution"
	mergeMetadata(op, map[string]interface{}{"canceledBy": requestedBy})
	op.LockedBy = ""
	op.LockedUntil = nil
	op.UpdatedAt = now
	op.FinishedAt = &now
	return cloneOp(op), true, nil
}

func (s *OperationStore) RecoverInFlightOperations(_ context.Context) error {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range s.ops {
		if op.Status != model.OperationRunning || !strings.HasPrefix(op.Kind, "app.") {
			continue
		}
		if op.LockedUntil != nil && op.LockedUntil.After(now) {
			continue
		}
		// Safe to requeue when attempts remain (memstore has no deployment-step
		// history, so app.deploy is treated as pre-mutable-stage).
		if op.Attempts < op.MaxAttempts {
			op.Status = model.OperationQueued
			op.Message = "operation recovered after API restart and queued for a safe retry"
			mergeMetadata(op, map[string]interface{}{"recoveredAfterRestart": true})
			op.LockedBy = ""
			op.LockedUntil = nil
			op.NextAttemptAt = now
			op.UpdatedAt = now
			continue
		}
		op.Status = model.OperationFailed
		op.Message = "operation interrupted after a non-retryable stage; manual review required"
		op.LastError = "operation executor lease expired"
		mergeMetadata(op, map[string]interface{}{"manualRecoveryRequired": true})
		op.LockedBy = ""
		op.LockedUntil = nil
		op.UpdatedAt = now
		op.FinishedAt = &now
	}
	return nil
}

func (s *OperationStore) RecoverMaintenanceOperations(_ context.Context) error {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range s.ops {
		if op.Status != model.OperationRunning {
			continue
		}
		if !strings.HasPrefix(op.Kind, "platform.") && !strings.HasPrefix(op.Kind, "host.") {
			continue
		}
		if op.LockedUntil != nil && op.LockedUntil.After(now) {
			continue
		}
		op.Status = model.OperationFailed
		op.Message = "maintenance executor stopped before recording completion; inspect host state before retrying"
		op.LastError = "maintenance executor lease expired"
		op.LockedBy = ""
		op.LockedUntil = nil
		op.UpdatedAt = now
		op.FinishedAt = &now
	}
	return nil
}

func (s *OperationStore) OperationMetrics(_ context.Context) ([]store.OperationMetric, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type key struct {
		kind   string
		status model.OperationStatus
	}
	agg := map[key]*store.OperationMetric{}
	for _, op := range s.ops {
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
