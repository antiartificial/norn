package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/store"
)

// FleetAttemptStore is an etcd-backed store.FleetAttemptStore. Each attempt is a
// JSON value under prefix/fleet/<planID>/<attemptID>. Application-level revision
// CAS (the RunnerAttempt.Revision field) gates every update and is enforced with
// an etcd compare-and-swap on the record's ModRevision, and reads first sweep
// expired heartbeat leases to "abandoned". It passes the shared conformance
// suite (revision CAS + lease fencing).
type FleetAttemptStore struct {
	kv     clientv3.KV
	prefix string
}

// NewFleetAttemptStore returns an etcd Fleet-attempt store rooted at prefix.
func NewFleetAttemptStore(kv clientv3.KV, prefix string) *FleetAttemptStore {
	return &FleetAttemptStore{kv: kv, prefix: prefix}
}

var _ store.FleetAttemptStore = (*FleetAttemptStore)(nil)

func (s *FleetAttemptStore) planPrefix(planID string) string {
	return s.prefix + "/fleet/" + planID + "/"
}
func (s *FleetAttemptStore) attemptKey(planID, id string) string {
	return s.planPrefix(planID) + id
}

type attemptRev struct {
	attempt *fleet.RunnerAttempt
	rev     int64
}

func decodeAttempt(raw []byte, modRev int64) (attemptRev, error) {
	var a fleet.RunnerAttempt
	if err := json.Unmarshal(raw, &a); err != nil {
		return attemptRev{}, err
	}
	a.SchemaVersion = fleet.RunnerAttemptSchemaVersion
	return attemptRev{attempt: &a, rev: modRev}, nil
}

func (s *FleetAttemptStore) scanPlan(ctx context.Context, planID string) ([]attemptRev, error) {
	resp, err := s.kv.Get(ctx, s.planPrefix(planID), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]attemptRev, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		item, err := decodeAttempt(kv.Value, kv.ModRevision)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *FleetAttemptStore) casPut(ctx context.Context, a *fleet.RunnerAttempt, rev int64) (bool, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	resp, err := s.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(s.attemptKey(a.PlanID, a.ID)), "=", rev)).
		Then(clientv3.OpPut(s.attemptKey(a.PlanID, a.ID), string(raw))).
		Commit()
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}

// sweepExpired abandons any live attempt of a plan whose heartbeat lease has
// expired, mirroring the PostgreSQL adapter's read-time sweep.
func (s *FleetAttemptStore) sweepExpired(ctx context.Context, planID string) error {
	items, err := s.scanPlan(ctx, planID)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, item := range items {
		a := item.attempt
		if a.Status != "queued" && a.Status != "running" {
			continue
		}
		if !a.HeartbeatExpiresAt.Before(now) {
			continue
		}
		a.Status = "abandoned"
		a.LastError = "heartbeat lease expired"
		a.Revision++
		finished := time.Now()
		a.FinishedAt = &finished
		a.UpdatedAt = finished
		if _, err := s.casPut(ctx, a, item.rev); err != nil {
			return err
		}
	}
	return nil
}

func (s *FleetAttemptStore) ListFleetRunnerAttempts(ctx context.Context, planID string) ([]fleet.RunnerAttempt, error) {
	if err := s.sweepExpired(ctx, planID); err != nil {
		return nil, err
	}
	items, err := s.scanPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	out := make([]fleet.RunnerAttempt, 0, len(items))
	for _, item := range items {
		out = append(out, *item.attempt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Attempt > out[j].Attempt })
	return out, nil
}

func (s *FleetAttemptStore) GetFleetRunnerAttempt(ctx context.Context, planID, attemptID string) (*fleet.RunnerAttempt, error) {
	if err := s.sweepExpired(ctx, planID); err != nil {
		return nil, err
	}
	resp, err := s.kv.Get(ctx, s.attemptKey(planID, attemptID))
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, ErrNotFound
	}
	item, err := decodeAttempt(resp.Kvs[0].Value, resp.Kvs[0].ModRevision)
	if err != nil {
		return nil, err
	}
	return item.attempt, nil
}

func (s *FleetAttemptStore) CreateFleetRunnerAttempt(ctx context.Context, item fleet.RunnerAttempt) (*fleet.RunnerAttempt, error) {
	if err := s.sweepExpired(ctx, item.PlanID); err != nil {
		return nil, err
	}
	existing, err := s.scanPlan(ctx, item.PlanID)
	if err != nil {
		return nil, err
	}
	maxAttempt := 0
	var firstRoot string
	firstAttempt := int(^uint(0) >> 1)
	for _, e := range existing {
		if e.attempt.Status == "queued" || e.attempt.Status == "running" {
			return nil, errors.New("fleet plan already has a live runner attempt")
		}
		if e.attempt.Attempt > maxAttempt {
			maxAttempt = e.attempt.Attempt
		}
		if e.attempt.Attempt < firstAttempt {
			firstAttempt = e.attempt.Attempt
			firstRoot = e.attempt.RootAttemptID
		}
	}
	item.Attempt = maxAttempt + 1
	if item.Attempt == 1 {
		item.RootAttemptID = item.ID
	} else {
		if firstRoot == "" {
			return nil, errors.New("fleet runner attempt root is missing")
		}
		item.RootAttemptID = firstRoot
	}
	now := time.Now().UTC()
	item.StartedAt, item.UpdatedAt, item.HeartbeatAt = now, now, now
	item.HeartbeatExpiresAt = now.Add(time.Duration(item.HeartbeatTimeoutSeconds) * time.Second)
	item.Status = "queued"
	if item.CurrentPhase == "" {
		item.CurrentPhase = "provider_applying"
	}
	item.Revision = 1
	item.SchemaVersion = fleet.RunnerAttemptSchemaVersion
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	if _, err := s.kv.Put(ctx, s.attemptKey(item.PlanID, item.ID), string(raw)); err != nil {
		return nil, err
	}
	return &item, nil
}

// UpdateFleetRunnerAttempt applies action only when revision matches the
// persisted record (and, for heartbeat/advance, the attempt is still live and
// its lease unexpired), then bumps the revision. A stale revision or ineligible
// state matches nothing and returns ErrNotFound, mirroring the PostgreSQL
// adapter's pgx.ErrNoRows.
func (s *FleetAttemptStore) UpdateFleetRunnerAttempt(ctx context.Context, planID, id string, revision int64, action string, values ...interface{}) (*fleet.RunnerAttempt, error) {
	for attempt := 0; attempt < 32; attempt++ {
		resp, err := s.kv.Get(ctx, s.attemptKey(planID, id))
		if err != nil {
			return nil, err
		}
		if len(resp.Kvs) == 0 {
			return nil, ErrNotFound
		}
		item, err := decodeAttempt(resp.Kvs[0].Value, resp.Kvs[0].ModRevision)
		if err != nil {
			return nil, err
		}
		a := item.attempt
		if a.Revision != revision {
			return nil, ErrNotFound
		}
		live := a.Status == "queued" || a.Status == "running"
		unexpired := !a.HeartbeatExpiresAt.Before(time.Now())
		now := time.Now().UTC()
		switch action {
		case "heartbeat":
			if !live || !unexpired {
				return nil, ErrNotFound
			}
			seq, ok := values[0].(int64)
			msg, ok2 := values[1].(string)
			if !ok || !ok2 {
				return nil, fmt.Errorf("heartbeat requires (int64 sequence, string message)")
			}
			a.Status = "running"
			a.HeartbeatSequence = seq
			a.HeartbeatAt = now
			a.HeartbeatExpiresAt = now.Add(time.Duration(a.HeartbeatTimeoutSeconds) * time.Second)
			a.LastError = msg
		case "advance":
			if !live || !unexpired {
				return nil, ErrNotFound
			}
			phase, ok := values[0].(string)
			if !ok {
				return nil, fmt.Errorf("advance requires (string phase)")
			}
			a.CurrentPhase = phase
			if phase == "complete" {
				a.Status = "succeeded"
				finished := now
				a.FinishedAt = &finished
			} else {
				a.Status = "running"
			}
		case "cancel":
			if !live {
				return nil, ErrNotFound
			}
			msg, ok := values[0].(string)
			if !ok {
				return nil, fmt.Errorf("cancel requires (string message)")
			}
			a.Status = "canceled"
			a.LastError = msg
			finished := now
			a.FinishedAt = &finished
		default:
			return nil, fmt.Errorf("unsupported runner attempt update")
		}
		a.Revision++
		a.UpdatedAt = now
		ok, err := s.casPut(ctx, a, item.rev)
		if err != nil {
			return nil, err
		}
		if ok {
			return a, nil
		}
	}
	return nil, fmt.Errorf("update fleet attempt %s: exhausted retries under contention", id)
}
