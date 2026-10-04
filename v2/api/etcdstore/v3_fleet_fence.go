package etcdstore

// This file wires the per-target mutation fence (etcdstore/v3_fleet_targets.go,
// built on fleet/lifecycle/fence.go) into the etcd dispatch and attempt paths
// (docs/v3/fleet-controller/plan.md §2.2, WP8b). Every fence-relevant
// transition — acquire (AcceptFleetGitHubDispatch), bind (acceptFleetRunnerAttempt),
// evidence writes (UpdateFleetRunnerAttempt, acceptFleetReconciliation) and
// release on complete (UpdateFleetRunnerAttempt's advance-to-complete) — reads
// the registry, fence and epoch fresh in the same call that decides the
// mutation (T1) and commits any fence write atomically with it (T3).
//
// Abandonment (B1/H7) is not visible to the pure lifecycle.Decide* functions:
// every one of those call sites also compares
// CreateRevision(fleet-target-abandoned/<planID>)==0 in its own Txn and, for
// a cheap fail-fast refusal before any other work, reads it once up front.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/store"
)

// fleetTargetFenceContext is the fence-relevant state for one plan's target,
// read fresh by loadFleetTargetFenceContext. TargetID is "" whenever the
// registry has never had a target registered, or (only when strict=false)
// the plan's cluster does not resolve to one: callers must then behave
// exactly as they did before the fence domain existed (Q1).
type fleetTargetFenceContext struct {
	TargetID         string
	RegistryRevision int64
	Fence            lifecycle.FenceFacts
	FenceRevision    int64
	Epoch            int64
	EpochRevision    int64
}

// loadFleetTargetFenceContext resolves cluster/environment to a target and,
// if one exists, reads its fence and the singleton authority epoch.
//
// strict=true propagates a resolution refusal (unregistered cluster, alias
// conflict) as-is: the Acquire and Bind transitions must refuse admission for
// an unregistered or conflicting target (Q1/M1).
//
// strict=false downgrades a resolution refusal to "no fence": every
// evidence-write path (heartbeat, advance, checkpoint, release-on-complete)
// must never newly block an attempt that was already admitted before its
// target existed, was registered, or whose cluster/environment mapping
// changed underneath it. Those paths were already fenced at acquire/bind
// time if a fence applies to them at all.
func (s *V3OperationStore) loadFleetTargetFenceContext(ctx context.Context, cluster, environment string, strict bool) (fleetTargetFenceContext, error) {
	targetID, registryEmpty, registryRevision, err := s.ResolveFleetTargetForPlan(ctx, cluster, environment)
	if err != nil {
		if strict {
			return fleetTargetFenceContext{}, err
		}
		return fleetTargetFenceContext{RegistryRevision: registryRevision}, nil
	}
	out := fleetTargetFenceContext{RegistryRevision: registryRevision}
	if registryEmpty || targetID == "" {
		return out, nil
	}
	out.TargetID = targetID
	fence, fenceRevision, err := s.GetFleetTargetFence(ctx, targetID)
	if err != nil {
		return fleetTargetFenceContext{}, err
	}
	out.Fence, out.FenceRevision = fence, fenceRevision
	epoch, epochRevision, err := s.fleetAuthorityEpochWithRevision(ctx)
	if err != nil {
		return fleetTargetFenceContext{}, err
	}
	out.Epoch, out.EpochRevision = epoch, epochRevision
	return out, nil
}

// registryCompare is the Txn compare every fence-relevant admission includes
// (M1): the registry-empty decision (or the registry generation this plan's
// target was resolved against) stays valid at commit time, or the whole
// mutation is rejected and must be re-decided from fresh reads.
func (s *V3OperationStore) registryCompare(c fleetTargetFenceContext) clientv3.Cmp {
	return clientv3.Compare(clientv3.ModRevision(s.fleetTargetRegistryKey()), "=", c.RegistryRevision)
}

// abandonedCompare is the Txn compare every path B1/H7 lists must include:
// "AND NOT EXISTS abandoned" (plan.md §2.2, "What abandonment refuses").
func (s *V3OperationStore) abandonedCompare(planID string) clientv3.Cmp {
	return clientv3.Compare(clientv3.CreateRevision(s.fleetTargetAbandonedKey(planID)), "=", 0)
}

// checkFleetTargetPlanAbandoned is the cheap up-front read every fence-gated
// path uses to fail fast and report lifecycle.CodeFleetTargetHolderAbandoned
// before doing any other work, rather than surfacing the abandonedCompare
// Txn failure as a generic, unexplained conflict.
func (s *V3OperationStore) checkFleetTargetPlanAbandoned(ctx context.Context, planID string) (bool, error) {
	response, err := s.kv.Get(ctx, s.fleetTargetAbandonedKey(planID), clientv3.WithCountOnly())
	if err != nil {
		return false, err
	}
	return response.Count > 0, nil
}

// fleetHolderAbandonedError is the stable refusal every abandoned-plan check
// returns (plan.md §2.2's CodeFleetTargetHolderAbandoned).
func fleetHolderAbandonedError() *lifecycle.FenceError {
	return &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetHolderAbandoned, Reason: "fleet target holder plan is permanently abandoned"}
}

// fleetTargetOccupiedByOther re-reads targetID's fence fresh (never trusting
// a read taken before a Txn commit decision) and reports whether it is held
// by a plan other than planID — the m10 race-loser re-read every acquire
// path uses when its own Txn's compares fail for a reason other than its own
// identity replaying.
func (s *V3OperationStore) fleetTargetOccupiedByOther(ctx context.Context, targetID, planID string) bool {
	if targetID == "" {
		return false
	}
	fresh, _, err := s.GetFleetTargetFence(ctx, targetID)
	return err == nil && fresh.Held && fresh.HolderPlanID != planID
}

// CheckFleetTargetPlanAbandoned is the exported form of
// checkFleetTargetPlanAbandoned, for callers outside this package (the etcd
// normal-router dispatch handler's m11 pre-check).
func (s *V3OperationStore) CheckFleetTargetPlanAbandoned(ctx context.Context, planID string) (bool, error) {
	return s.checkFleetTargetPlanAbandoned(ctx, planID)
}

// FleetTargetOccupiedForPlan is the non-locking occupancy pre-check (m11) the
// etcd dispatch submit route runs before its signed reservation, so a
// refused request leaves no queued reservation behind. It resolves cluster
// and environment to a target exactly as the real acquire step will, and
// reports occupied only when a different plan already holds that target's
// fence. A resolution refusal (unregistered cluster, alias conflict) is left
// for the locked acquire inside AcceptFleetGitHubDispatch, which stays
// authoritative; this is a display-and-fail-fast check only, so it never
// itself reports an error for that case.
func (s *V3OperationStore) FleetTargetOccupiedForPlan(ctx context.Context, cluster, environment, planID string) (bool, error) {
	targetID, registryEmpty, _, resolveErr := s.ResolveFleetTargetForPlan(ctx, cluster, environment)
	if resolveErr != nil || registryEmpty || targetID == "" {
		return false, nil
	}
	fence, _, err := s.GetFleetTargetFence(ctx, targetID)
	if err != nil {
		return false, err
	}
	return fence.Held && fence.HolderPlanID != planID, nil
}

// loadFleetTargetHolderFacts builds the lifecycle.HolderFacts a fence
// Outcome or Release decision needs for planID, re-derived fresh from the
// attempt, preparation and binding keys exactly as production admission
// does (the fence is a lock, not a ledger: m18). An empty planID (no
// holder) returns the zero value.
func (s *V3OperationStore) loadFleetTargetHolderFacts(ctx context.Context, planID string) (lifecycle.HolderFacts, error) {
	if planID == "" {
		return lifecycle.HolderFacts{}, nil
	}
	attempts, err := s.ListFleetRunnerAttempts(ctx, planID)
	if err != nil {
		return lifecycle.HolderFacts{}, err
	}
	abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, planID)
	if err != nil {
		return lifecycle.HolderFacts{}, err
	}
	facts := lifecycle.HolderFacts{Attempts: attempts, Abandoned: abandoned}
	if preparationResp, err := s.kv.Get(ctx, s.fleetGitHubDispatchPreparationKey(planID)); err != nil {
		return lifecycle.HolderFacts{}, err
	} else if len(preparationResp.Kvs) == 1 {
		var preparation v3FleetGitHubDispatchPreparation
		if err := decodeV3Record(preparationResp.Kvs[0].Value, &preparation); err != nil {
			return lifecycle.HolderFacts{}, err
		}
		facts.DispatchState, facts.DispatchCreatedAt = "prepared", preparation.CreatedAt
	}
	if bindingResp, err := s.kv.Get(ctx, s.fleetRunnerDispatchKey(planID)); err != nil {
		return lifecycle.HolderFacts{}, err
	} else if len(bindingResp.Kvs) == 1 {
		var binding v3FleetRunnerDispatch
		if err := decodeV3Record(bindingResp.Kvs[0].Value, &binding); err != nil {
			return lifecycle.HolderFacts{}, err
		}
		facts.DispatchState, facts.SubmissionStartedAt = "bound", binding.CreatedAt
	}
	return facts, nil
}

// FleetTargetOutcome is the read-only occupancy derivation (M3; plan.md
// §2.2) behind the future GET targets/{id} route (WP9b). Admission never
// calls this; it is display-only.
func (s *V3OperationStore) FleetTargetOutcome(ctx context.Context, targetID string, now time.Time) (lifecycle.Occupancy, string, error) {
	fence, _, err := s.GetFleetTargetFence(ctx, targetID)
	if err != nil {
		return "", "", err
	}
	epoch, err := s.FleetAuthorityEpoch(ctx)
	if err != nil {
		return "", "", err
	}
	holder, err := s.loadFleetTargetHolderFacts(ctx, fence.HolderPlanID)
	if err != nil {
		return "", "", err
	}
	occupancy, reason := lifecycle.Outcome(fence, holder, epoch, now)
	return occupancy, reason, nil
}

// planFleetTargetRelease is the internal release implementation behind the
// signed fleet.target.fence-release operation (plan.md §2.5; the exported
// unsigned callers are test hooks only). It applies lifecycle.DecideRelease
// for mode terminal or abandon, gathering HolderFacts fresh exactly as every
// other fence transition does (T1), and returns the compares and puts that
// commit the fence write atomically with the holder plan's permanent
// abandonment record (m16: a terminal release also permanently abandons the
// holder plan, same as abandon mode). The plan-state ModRevision read before
// the holder facts is part of the compares, so any attempt change between the
// proof check and the commit fails the Txn instead of releasing on stale
// proof. A generation mismatch returns store.CodeFleetTargetExpectedGenerationMismatch.
func (s *V3OperationStore) planFleetTargetRelease(ctx context.Context, targetID string, expectedGeneration int64, mode lifecycle.ReleaseMode, proof *lifecycle.TerminalProof, operationID string, now time.Time) (fleetTargetMutationPlan, error) {
	if target, err := s.GetFleetTarget(ctx, targetID); err != nil {
		return fleetTargetMutationPlan{}, err
	} else if target == nil {
		return fleetTargetMutationPlan{}, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetUnregistered, Reason: "fleet target is not registered"}
	}
	fence, fenceRevision, err := s.GetFleetTargetFence(ctx, targetID)
	if err != nil {
		return fleetTargetMutationPlan{}, err
	}
	if fence.Generation != expectedGeneration {
		return fleetTargetMutationPlan{}, &lifecycle.FenceError{Code: store.CodeFleetTargetExpectedGenerationMismatch, Reason: "expected generation does not match the current fence generation"}
	}
	holderPlanID, holderNonceSHA256 := fence.HolderPlanID, fence.HolderNonceSHA256
	stateCompare, err := s.fleetPlanStateCompare(ctx, holderPlanID)
	if err != nil {
		return fleetTargetMutationPlan{}, err
	}
	holder, err := s.loadFleetTargetHolderFacts(ctx, holderPlanID)
	if err != nil {
		return fleetTargetMutationPlan{}, err
	}
	next, decideErr := lifecycle.DecideRelease(fence, holder, mode, proof, now)
	if decideErr != nil {
		return fleetTargetMutationPlan{}, decideErr
	}
	record, err := json.Marshal(fleetTargetFenceRecord(next))
	if err != nil {
		return fleetTargetMutationPlan{}, err
	}
	plan := fleetTargetMutationPlan{
		compares: []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(targetID)), "=", fenceRevision), stateCompare},
		puts:     []clientv3.Op{clientv3.OpPut(s.fleetTargetFenceKey(targetID), string(record))},
	}
	if mode == lifecycle.ReleaseModeTerminal || mode == lifecycle.ReleaseModeAbandon {
		plan.compares = append(plan.compares, s.abandonedCompare(holderPlanID))
		abandonedRecord, err := json.Marshal(v3FleetTargetAbandoned{PlanID: holderPlanID, NonceSHA256: holderNonceSHA256, TargetID: targetID, OperationID: operationID, AbandonedAt: now})
		if err != nil {
			return fleetTargetMutationPlan{}, err
		}
		plan.puts = append(plan.puts, clientv3.OpPut(s.fleetTargetAbandonedKey(holderPlanID), string(abandonedRecord)))
	}
	return plan, nil
}

// fleetPlanStateCompare returns a compare pinning planID's runner plan-state
// revision (0 when the plan has none), which every attempt write bumps.
func (s *V3OperationStore) fleetPlanStateCompare(ctx context.Context, planID string) (clientv3.Cmp, error) {
	key := s.fleetRunnerPlanStateKey(planID)
	response, err := s.kv.Get(ctx, key)
	if err != nil {
		return clientv3.Cmp{}, err
	}
	var revision int64
	if len(response.Kvs) == 1 {
		revision = response.Kvs[0].ModRevision
	}
	return clientv3.Compare(clientv3.ModRevision(key), "=", revision), nil
}

// planFleetTargetAbandon is the internal H7 break-glass abandon behind the
// signed fleet.target.abandon-plan operation, keyed by plan rather than
// target, so it works whether or not the plan's cluster is registered. It
// applies lifecycle.AbandonMinimumAge and the listing-snapshot requirement
// exactly as DecideRelease's abandon mode does (by constructing the same
// decision over a synthetic held-by-this-plan fence), and, only if the plan's
// target is registered and that target's real fence is currently held by this
// exact plan, also releases that fence in the same Txn.
func (s *V3OperationStore) planFleetTargetAbandon(ctx context.Context, planID, nonceSHA256, cluster, environment, operationID, listingSnapshotSHA256 string, now time.Time) (fleetTargetMutationPlan, error) {
	if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, planID); err != nil {
		return fleetTargetMutationPlan{}, err
	} else if abandoned {
		return fleetTargetMutationPlan{}, fleetHolderAbandonedError()
	}
	stateCompare, err := s.fleetPlanStateCompare(ctx, planID)
	if err != nil {
		return fleetTargetMutationPlan{}, err
	}
	holder, err := s.loadFleetTargetHolderFacts(ctx, planID)
	if err != nil {
		return fleetTargetMutationPlan{}, err
	}
	proof := &lifecycle.TerminalProof{ListingSnapshotSHA256: listingSnapshotSHA256}
	synthetic := lifecycle.FenceFacts{Held: true, HolderPlanID: planID, HolderNonceSHA256: nonceSHA256}
	if _, decideErr := lifecycle.DecideRelease(synthetic, holder, lifecycle.ReleaseModeAbandon, proof, now); decideErr != nil {
		return fleetTargetMutationPlan{}, decideErr
	}
	abandonedRecord, err := json.Marshal(v3FleetTargetAbandoned{PlanID: planID, NonceSHA256: nonceSHA256, OperationID: operationID, AbandonedAt: now})
	if err != nil {
		return fleetTargetMutationPlan{}, err
	}
	plan := fleetTargetMutationPlan{
		compares: []clientv3.Cmp{s.abandonedCompare(planID), stateCompare},
		puts:     []clientv3.Op{clientv3.OpPut(s.fleetTargetAbandonedKey(planID), string(abandonedRecord))},
	}
	// The resolved target's fence revision is pinned whether or not this
	// plan holds it, so an acquire that commits between this read and the
	// Txn fails the Txn (the next round then releases the fence it took)
	// instead of leaving a fence held by an abandoned plan. Only an
	// unregistered cluster or alias conflict (a *lifecycle.FenceError) means
	// "no target"; any other resolve or read error fails closed.
	targetID, registryEmpty, _, resolveErr := s.ResolveFleetTargetForPlan(ctx, cluster, environment)
	var fenceErr *lifecycle.FenceError
	if resolveErr != nil && !errors.As(resolveErr, &fenceErr) {
		return fleetTargetMutationPlan{}, resolveErr
	}
	if resolveErr == nil && !registryEmpty && targetID != "" {
		fence, fenceRevision, err := s.GetFleetTargetFence(ctx, targetID)
		if err != nil {
			return fleetTargetMutationPlan{}, err
		}
		plan.compares = append(plan.compares, clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(targetID)), "=", fenceRevision))
		if fence.Held && fence.HolderPlanID == planID {
			next := fence
			next.Held, next.HolderPlanID, next.HolderNonceSHA256, next.Revision = false, "", "", fence.Revision+1
			next.LastRelease = &lifecycle.Release{PlanID: planID, Reason: "abandoned", At: now}
			record, err := json.Marshal(fleetTargetFenceRecord(next))
			if err != nil {
				return fleetTargetMutationPlan{}, err
			}
			plan.puts = append(plan.puts, clientv3.OpPut(s.fleetTargetFenceKey(targetID), string(record)))
		}
	}
	return plan, nil
}
