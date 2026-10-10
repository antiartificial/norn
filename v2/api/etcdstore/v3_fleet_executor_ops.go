package etcdstore

// etcd readers for the executor-lane credential grant, stop attestation,
// provider evidence and release proof (plan-5-7 E5), the counterpart of
// store/fleet_executor_ops.go. They implement store.FleetExecutorOpsReader on
// the V3OperationStore and read only: the decisions themselves are the
// backend-neutral functions in package store.
//
// Executor identity (principal subject, executor metadata) lives in E2e's
// immutable sibling record per attempt (GetFleetExecutorAttemptMeta); an
// attempt without one is a GitHub-lane attempt (Executor false). Devices live
// in the auth keyspace under the same prefix and are read directly.

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/store"
)

var (
	_ store.FleetExecutorOpsReader          = (*V3OperationStore)(nil)
	_ store.FleetExecutorConvergePlanReader = (*V3OperationStore)(nil)
)

func (s *V3OperationStore) fleetExecutorAttemptFact(ctx context.Context, item fleet.RunnerAttempt) (store.FleetExecutorAttemptFact, error) {
	fact := store.FleetExecutorAttemptFact{
		ID: item.ID, PlanID: item.PlanID, RunnerAttemptID: item.RunnerAttemptID, Attempt: item.Attempt, Status: item.Status,
		CurrentPhase: item.CurrentPhase, RetryOf: item.RetryOf, StartedAt: item.StartedAt,
		HeartbeatExpiresAt: item.HeartbeatExpiresAt, FinishedAt: item.FinishedAt,
	}
	meta, err := s.GetFleetExecutorAttemptMeta(ctx, item.PlanID, item.ID)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return fact, err
	default:
		fact.Executor, fact.PrincipalSubject, fact.Metadata = meta.Executor.Lane == executor.LaneMetadataValue, meta.PrincipalSubject, meta.Executor
	}
	return fact, nil
}

// FleetExecutorAttempts returns planID's attempts, oldest first.
func (s *V3OperationStore) FleetExecutorAttempts(ctx context.Context, planID string) ([]store.FleetExecutorAttemptFact, error) {
	items, err := s.ListFleetRunnerAttempts(ctx, planID)
	if err != nil {
		return nil, err
	}
	facts := make([]store.FleetExecutorAttemptFact, 0, len(items))
	for _, item := range items {
		fact, err := s.fleetExecutorAttemptFact(ctx, item)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].Attempt < facts[j].Attempt })
	return facts, nil
}

// FleetExecutorAttemptByID finds an executor-lane attempt by ID through the
// sibling metadata keyspace (the only attempts the executor routes name).
func (s *V3OperationStore) FleetExecutorAttemptByID(ctx context.Context, attemptID string) (*store.FleetExecutorAttemptFact, error) {
	prefix := s.prefix + "/v3/fleet-executor-attempts/"
	response, err := s.kv.Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithKeysOnly())
	if err != nil {
		return nil, err
	}
	for _, kv := range response.Kvs {
		planID, id, ok := strings.Cut(strings.TrimPrefix(string(kv.Key), prefix), "/")
		if !ok || id != attemptID {
			continue
		}
		item, err := s.GetFleetRunnerAttempt(ctx, planID, attemptID)
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		fact, err := s.fleetExecutorAttemptFact(ctx, *item)
		if err != nil {
			return nil, err
		}
		return &fact, nil
	}
	return nil, nil
}

// FleetExecutorDevice reads a device from the auth keyspace.
func (s *V3OperationStore) FleetExecutorDevice(ctx context.Context, deviceID string) (store.FleetExecutorDeviceFact, error) {
	if deviceID == "" {
		return store.FleetExecutorDeviceFact{}, nil
	}
	device, _, err := NewAuthStore(s.kv, s.prefix).loadDevice(ctx, deviceID)
	if errors.Is(err, ErrNotFound) {
		return store.FleetExecutorDeviceFact{}, nil
	}
	if err != nil {
		return store.FleetExecutorDeviceFact{}, err
	}
	return store.FleetExecutorDeviceFact{Exists: true, Revoked: device.RevokedAt != nil, Platform: device.Platform}, nil
}

func (s *V3OperationStore) FleetExecutorPlanIsConverge(ctx context.Context, planID string) (bool, error) {
	converge, _, err := s.fleetConvergePlanOf(ctx, planID)
	return converge != nil, err
}

func (s *V3OperationStore) FleetExecutorConvergePlan(ctx context.Context, planID string) (*store.FleetExecutorConvergePlanFact, error) {
	record, _, err := s.load(ctx, planID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if record.Operation.Kind != "fleet.capacity-plan" {
		return nil, nil
	}
	converge, digest, _ := parseFleetConvergePlanPayload(record.Operation.Payload)
	if converge == nil {
		return nil, nil
	}
	return &store.FleetExecutorConvergePlanFact{Resource: converge.Resource, CommitSHA: converge.CommitSHA, PlanSHA256: strings.TrimPrefix(digest, "sha256:")}, nil
}

func (s *V3OperationStore) FleetExecutorLedgerRows(ctx context.Context, kind, planID string) ([]store.FleetExecutorLedgerRow, error) {
	rows, _, err := s.fleetExecutorLedgerRows(ctx, kind, planID)
	if err != nil {
		return nil, err
	}
	out := make([]store.FleetExecutorLedgerRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.FleetExecutorLedgerRow{ID: row.ID, Payload: row.Payload, Metadata: row.Metadata, StartedAt: row.StartedAt})
	}
	return out, nil
}

// fleetExecutorLaneProof is the etcd release hook, the counterpart of
// store's fleetExecutorLaneProofTx: it returns the proof to decide with and
// whether planID is an executor-lane holder. The Txn that commits the release
// re-pins every revision the release decision itself reads, so a recovery
// attempt started after this read is refused there as a live attempt.
func (s *V3OperationStore) fleetExecutorLaneProof(ctx context.Context, planID string, evidence *executor.ProviderEvidence, base *lifecycle.TerminalProof, now time.Time) (*lifecycle.TerminalProof, bool, error) {
	if planID == "" {
		return base, false, nil
	}
	attempts, err := s.FleetExecutorAttempts(ctx, planID)
	if err != nil {
		return nil, false, err
	}
	var rows []store.FleetExecutorLedgerRow
	var device store.FleetExecutorDeviceFact
	if store.FleetExecutorLane(attempts) {
		if rows, err = s.FleetExecutorLedgerRows(ctx, executor.KindStopAttestation, planID); err != nil {
			return nil, false, err
		}
		latest := attempts[len(attempts)-1]
		if device, err = s.FleetExecutorDevice(ctx, store.FleetExecutorDeviceID(latest.PrincipalSubject)); err != nil {
			return nil, false, err
		}
	}
	return store.FleetExecutorReleaseProof(attempts, rows, evidence, device, base, now)
}
