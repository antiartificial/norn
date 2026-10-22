package etcdstore

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

func (s *V3OperationStore) app04DeleteAdmissionKey(planID string) string {
	return s.prefix + "/v3/fleet-app04-delete-admission/" + planID
}

// RecordApp04DeleteAdmission commits the verifier's exact reviewed binding.
// The verifier must establish the saved plan, provider, writer and workload
// facts before calling. Those fields are attestations; Norn does not
// independently inspect provider state or PG. Norn checks its recorded
// converge-plan attempt and held fence.
func (s *V3OperationStore) RecordApp04DeleteAdmission(ctx context.Context, a appdelete.App04Admission, principal string) error {
	if s == nil || a.Validate() != nil || principal != "fleet-app-delete-verifier:work-staging" {
		return appdelete.ErrRefused
	}
	planKey, attemptKey := s.opKey(a.PlanID), s.fleetRunnerAttemptKey(a.PlanID, a.AttemptID)
	metaKey, admissionKey := s.fleetExecutorMetaKey(a.PlanID, a.AttemptID), s.app04DeleteAdmissionKey(a.PlanID)
	first, err := s.kv.Txn(ctx).Then(clientv3.OpGet(planKey), clientv3.OpGet(attemptKey), clientv3.OpGet(metaKey), clientv3.OpGet(admissionKey)).Commit()
	if err != nil {
		return err
	}
	if len(first.Responses) != 4 {
		return appdelete.ErrRefused
	}
	planRows, attemptRows := first.Responses[0].GetResponseRange().Kvs, first.Responses[1].GetResponseRange().Kvs
	metaRows, admissions := first.Responses[2].GetResponseRange().Kvs, first.Responses[3].GetResponseRange().Kvs
	if len(planRows) != 1 || len(attemptRows) != 1 || len(metaRows) != 1 || len(admissions) != 0 {
		return appdelete.ErrRefused
	}
	var plan v3Record
	var attempt fleet.RunnerAttempt
	var meta FleetExecutorAttemptMeta
	if decodeV3Record(planRows[0].Value, &plan) != nil || decodeV3Record(attemptRows[0].Value, &attempt) != nil || decodeV3Record(metaRows[0].Value, &meta) != nil {
		return appdelete.ErrRefused
	}
	converge, digest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.Kind != "fleet.capacity-plan" || converge == nil || converge.CommitSHA != a.SourceCommit ||
		strings.TrimPrefix(digest, "sha256:") != a.PlanSHA256 ||
		attempt.ID != a.AttemptID || attempt.PlanID != a.PlanID || attempt.CommitSHA != converge.CommitSHA ||
		attempt.PlanSHA256 != a.PlanSHA256 || attempt.Status != "running" || attempt.CurrentPhase != "provider_applying" ||
		!attempt.HeartbeatExpiresAt.After(time.Now().UTC()) || meta.Recovery || meta.OperationClass != "unknown" || meta.CreatedNodeCount != 0 {
		return appdelete.ErrRefused
	}
	targetID, empty, _, err := s.ResolveFleetTargetForPlan(ctx, cluster, "")
	if err != nil || empty || targetID != a.TargetID {
		return appdelete.ErrRefused
	}
	fenceKey, epochKey := s.fleetTargetFenceKey(targetID), s.fleetAuthorityEpochKey()
	second, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(epochKey)).Commit()
	if err != nil {
		return err
	}
	if len(second.Responses) != 2 || len(second.Responses[0].GetResponseRange().Kvs) != 1 || len(second.Responses[1].GetResponseRange().Kvs) != 1 {
		return appdelete.ErrRefused
	}
	fenceRow, epochRow := second.Responses[0].GetResponseRange().Kvs[0], second.Responses[1].GetResponseRange().Kvs[0]
	var fence v3FleetTargetFence
	epoch, err := strconv.ParseInt(strings.TrimSpace(string(epochRow.Value)), 10, 64)
	if err != nil || decodeV3Record(fenceRow.Value, &fence) != nil || !fence.Held || fence.HolderPlanID != a.PlanID ||
		fence.HolderNonceSHA256 != executor.HolderNonceSHA256(a.PlanID) || fence.Generation != a.TargetFenceGeneration ||
		fence.AuthorityEpoch != a.AuthorityEpoch || epoch != a.AuthorityEpoch {
		return appdelete.ErrRefused
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	committed, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(planKey), "=", planRows[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(attemptKey), "=", attemptRows[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(metaKey), "=", metaRows[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(admissionKey), "=", 0),
		clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fenceRow.ModRevision),
		clientv3.Compare(clientv3.ModRevision(epochKey), "=", epochRow.ModRevision),
	).Then(clientv3.OpPut(admissionKey, string(raw))).Commit()
	if err != nil {
		return err
	}
	if !committed.Succeeded {
		return appdelete.ErrRefused
	}
	return nil
}

func (s *V3OperationStore) GetApp04DeleteAdmission(ctx context.Context, planID string) (appdelete.App04Admission, error) {
	var a appdelete.App04Admission
	if s == nil {
		return a, appdelete.ErrRefused
	}
	response, err := s.kv.Get(ctx, s.app04DeleteAdmissionKey(planID))
	if err != nil {
		return a, err
	}
	if response.Count != 1 || len(response.Kvs) != 1 {
		return a, appdelete.ErrNotFound
	}
	if json.Unmarshal(response.Kvs[0].Value, &a) != nil || a.Validate() != nil || a.PlanID != planID {
		return appdelete.App04Admission{}, appdelete.ErrRefused
	}
	return a, nil
}
