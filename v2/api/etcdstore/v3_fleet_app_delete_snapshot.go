package etcdstore

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/appdelete"
)

// GetAppDeleteSnapshot projects Norn-owned facts from a single linearizable
// etcd transaction. Preliminary reads discover the target key; the final
// transaction rechecks the plan and alias at its own revision.
func (s *V3OperationStore) GetAppDeleteSnapshot(ctx context.Context, planID, attemptID string) (appdelete.Snapshot, error) {
	var zero appdelete.Snapshot
	if s == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil {
		return zero, appdelete.ErrRefused
	}
	planKey := s.opKey(planID)
	pre, err := s.kv.Get(ctx, planKey)
	if err != nil {
		return zero, err
	}
	if len(pre.Kvs) != 1 {
		return zero, appdelete.ErrNotFound
	}
	var initial v3Record
	if decodeV3Record(pre.Kvs[0].Value, &initial) != nil {
		return zero, appdelete.ErrRefused
	}
	_, _, cluster := parseFleetConvergePlanPayload(initial.Operation.Payload)
	if initial.Operation.Kind != "fleet.capacity-plan" || cluster == "" {
		return zero, appdelete.ErrRefused
	}
	targetID, empty, _, err := s.ResolveFleetTargetForPlan(ctx, cluster, "")
	if err != nil || empty || targetID == "" {
		return zero, appdelete.ErrRefused
	}
	keys := []string{planKey, s.fleetRunnerAttemptKey(planID, attemptID), s.fleetExecutorMetaKey(planID, attemptID),
		s.fleetTargetAliasKey("cluster:" + cluster), s.fleetTargetKey(targetID), s.fleetTargetFenceKey(targetID),
		s.fleetAuthorityEpochKey(), s.app04DeleteAdmissionKey(planID), s.appDeleteCoPermitKey(planID),
		s.fleetTargetAbandonedKey(planID), s.fleetTargetRegistryKey()}
	ops := make([]clientv3.Op, 0, len(keys))
	for _, key := range keys {
		ops = append(ops, clientv3.OpGet(key))
	}
	result, err := s.kv.Txn(ctx).Then(ops...).Commit()
	if err != nil {
		return zero, err
	}
	if len(result.Responses) != len(keys) {
		return zero, appdelete.ErrRefused
	}
	row := func(i int) []byte {
		kvs := result.Responses[i].GetResponseRange().Kvs
		if len(kvs) != 1 {
			return nil
		}
		return kvs[0].Value
	}
	for _, i := range []int{0, 1, 2, 3, 4, 5, 6, 10} {
		if row(i) == nil {
			return zero, appdelete.ErrRefused
		}
	}
	if row(9) != nil || string(row(3)) != targetID {
		return zero, appdelete.ErrRefused
	}
	var plan v3Record
	var attempt fleet.RunnerAttempt
	var meta FleetExecutorAttemptMeta
	var target FleetTarget
	var fence v3FleetTargetFence
	var registry v3FleetTargetRegistry
	if decodeV3Record(row(0), &plan) != nil || decodeV3Record(row(1), &attempt) != nil ||
		decodeV3Record(row(2), &meta) != nil || decodeV3Record(row(4), &target) != nil || target.TargetID != targetID ||
		decodeV3Record(row(5), &fence) != nil || decodeV3Record(row(10), &registry) != nil || registry.Generation <= 0 {
		return zero, appdelete.ErrRefused
	}
	converge, digest, actualCluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.Kind != "fleet.capacity-plan" || converge == nil || actualCluster != cluster ||
		attempt.ID != attemptID || attempt.PlanID != planID || attempt.CommitSHA != converge.CommitSHA || meta.PrincipalSubject == "" ||
		"sha256:"+attempt.PlanSHA256 != digest || meta.Recovery || meta.OperationClass != "unknown" || meta.CreatedNodeCount != 0 {
		return zero, appdelete.ErrRefused
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(string(row(6))), 10, 64)
	if err != nil || epoch <= 0 || fence.AuthorityEpoch <= 0 || fence.Generation <= 0 {
		return zero, appdelete.ErrRefused
	}
	out := appdelete.Snapshot{SchemaVersion: appdelete.SnapshotSchema, PlanID: planID, AttemptID: attemptID,
		EtcdRevision: result.Header.Revision, SourceCommit: converge.CommitSHA, PlanSHA256: attempt.PlanSHA256,
		AttemptStatus: attempt.Status, AttemptPhase: attempt.CurrentPhase, AttemptRevision: attempt.Revision,
		HeartbeatExpiresAt:     attempt.HeartbeatExpiresAt.UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
		ExecutorOperationClass: meta.OperationClass, ExecutorRecovery: meta.Recovery, CreatedNodeCount: meta.CreatedNodeCount,
		TargetID: targetID, TargetFenceHeld: fence.Held, TargetFenceHolderPlanID: fence.HolderPlanID,
		TargetFenceGeneration: fence.Generation, TargetFenceAuthorityEpoch: fence.AuthorityEpoch, CurrentAuthorityEpoch: epoch}
	if raw := row(7); raw != nil {
		var admission appdelete.App04Admission
		if json.Unmarshal(raw, &admission) != nil || admission.Validate() != nil || admission.PlanID != planID || admission.AttemptID != attemptID {
			return zero, appdelete.ErrRefused
		}
		out.App04Admission = &admission
	}
	if raw := row(8); raw != nil {
		var permit appDeleteConsumedPermit
		if json.Unmarshal(raw, &permit) != nil || permit.SchemaVersion != appDeleteConsumedPermitSchema || permit.Request.Validate() != nil ||
			permit.Request.PlanID != planID || permit.Request.AttemptID != attemptID || permit.TargetID != targetID {
			return zero, appdelete.ErrRefused
		}
		out.ConsumedPermit = &permit.Request
	}
	return out, nil
}
