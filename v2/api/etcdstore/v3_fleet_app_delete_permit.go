package etcdstore

// This deliberately unwired co-permit consumes only Norn's live executor
// attempt and target fence. Fleet's separate PostgreSQL verifier and writer
// reservation must establish provider and workload authority.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/appdelete"
	"norn/v2/api/fleet/executor"
)

func (s *V3OperationStore) appDeleteCoPermitKey(planID string) string {
	// One key per plan, rather than per attempt, also refuses recovery replay.
	return s.prefix + "/v3/fleet-app-delete-co-permits/" + planID
}

// ConsumeAppDeleteCoPermit validates all Norn-owned facts and consumes the
// plan's only permit in the same etcd compare-and-swap. A lost commit reply is
// uncertain and cannot be retried; even an identical second call is refused.
func (s *V3OperationStore) ConsumeAppDeleteCoPermit(ctx context.Context, r appdelete.Request, principal string) error {
	if s == nil || r.Validate() != nil || principal != "fleet-attempt:"+r.AttemptID {
		return appdelete.ErrRefused
	}
	attemptKey := s.fleetRunnerAttemptKey(r.PlanID, r.AttemptID)
	metaKey := s.fleetExecutorMetaKey(r.PlanID, r.AttemptID)
	permitKey := s.appDeleteCoPermitKey(r.PlanID)
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(attemptKey), clientv3.OpGet(metaKey),
		clientv3.OpGet(s.opKey(r.PlanID)), clientv3.OpGet(permitKey),
		clientv3.OpGet(s.fleetTargetAbandonedKey(r.PlanID)),
	).Commit()
	if err != nil {
		return err
	}
	if len(response.Responses) != 5 {
		return appdelete.ErrRefused
	}
	attemptRows := response.Responses[0].GetResponseRange().Kvs
	metaRows := response.Responses[1].GetResponseRange().Kvs
	planRows := response.Responses[2].GetResponseRange().Kvs
	if len(attemptRows) != 1 || len(metaRows) != 1 || len(planRows) != 1 ||
		len(response.Responses[3].GetResponseRange().Kvs) != 0 ||
		len(response.Responses[4].GetResponseRange().Kvs) != 0 {
		return appdelete.ErrRefused
	}
	var attempt fleet.RunnerAttempt
	var meta FleetExecutorAttemptMeta
	var plan v3Record
	if decodeV3Record(attemptRows[0].Value, &attempt) != nil ||
		decodeV3Record(metaRows[0].Value, &meta) != nil ||
		decodeV3Record(planRows[0].Value, &plan) != nil {
		return appdelete.ErrRefused
	}
	now := time.Now().UTC()
	converge, digest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if attempt.ID != r.AttemptID || attempt.PlanID != r.PlanID ||
		attempt.Status != "running" || attempt.CurrentPhase != "provider_applying" ||
		!attempt.HeartbeatExpiresAt.After(now) ||
		meta.Recovery || meta.PrincipalSubject == "" ||
		plan.Operation.Kind != "fleet.capacity-plan" || converge == nil ||
		attempt.CommitSHA != converge.CommitSHA ||
		"sha256:"+attempt.PlanSHA256 != digest ||
		meta.OperationClass != "unknown" || meta.CreatedNodeCount != 0 {
		return appdelete.ErrRefused
	}
	// Executor attempts acquire a target fence at start, but unlike protected
	// GitHub attempts they do not serialize a FenceBinding on RunnerAttempt.
	// Resolve the plan's registered cluster and compare the live fence/epoch.
	targetID, empty, _, err := s.ResolveFleetTargetForPlan(ctx, cluster, "")
	if err != nil || empty || targetID == "" {
		return appdelete.ErrRefused
	}
	fenceKey := s.fleetTargetFenceKey(targetID)
	epochKey := s.fleetAuthorityEpochKey()
	second, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(epochKey),
		clientv3.OpGet(s.fleetTargetKey(targetID)),
		clientv3.OpGet(s.fleetTargetRegistryKey())).Commit()
	if err != nil {
		return err
	}
	if len(second.Responses) != 4 {
		return appdelete.ErrRefused
	}
	fences := second.Responses[0].GetResponseRange().Kvs
	epochs := second.Responses[1].GetResponseRange().Kvs
	targets := second.Responses[2].GetResponseRange().Kvs
	if len(fences) != 1 || len(epochs) != 1 || len(targets) != 1 {
		return appdelete.ErrRefused
	}
	var fence v3FleetTargetFence
	if decodeV3Record(fences[0].Value, &fence) != nil {
		return appdelete.ErrRefused
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(string(epochs[0].Value)), 10, 64)
	if err != nil || !fence.Held || fence.HolderPlanID != r.PlanID ||
		fence.HolderNonceSHA256 != executor.HolderNonceSHA256(r.PlanID) ||
		fence.Generation <= 0 || fence.AuthorityEpoch != epoch {
		return appdelete.ErrRefused
	}
	// No fake PostgreSQL or workload facts enter this record. The opaque tuple
	// makes a consumed reply inspectable while all retries still fail closed.
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(attemptKey), "=", attemptRows[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(metaKey), "=", metaRows[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(r.PlanID)), "=", planRows[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(permitKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetTargetAbandonedKey(r.PlanID)), "=", 0),
		clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fences[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(epochKey), "=", epochs[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetKey(targetID)), "=", targets[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetRegistryKey()), "=", 0),
	}
	registryRows := second.Responses[3].GetResponseRange().Kvs
	if len(registryRows) == 1 {
		compares[len(compares)-1] = clientv3.Compare(clientv3.ModRevision(s.fleetTargetRegistryKey()), "=", registryRows[0].ModRevision)
	}
	if len(registryRows) > 1 {
		return appdelete.ErrRefused
	}
	committed, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(permitKey, string(raw))).Commit()
	if err != nil {
		return err
	}
	if !committed.Succeeded {
		return appdelete.ErrRefused
	}
	return nil
}
