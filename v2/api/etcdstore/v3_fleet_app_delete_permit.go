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

type appDeleteConsumedPermit struct {
	SchemaVersion         string            `json:"schemaVersion"`
	Request               appdelete.Request `json:"request"`
	TargetID              string            `json:"targetId"`
	TargetFenceGeneration int64             `json:"targetFenceGeneration"`
	AuthorityEpoch        int64             `json:"authorityEpoch"`
}

const appDeleteConsumedPermitSchema = "norn.fleet-app-delete-consumed-permit/v1"

func (s *V3OperationStore) appDeleteCoPermitKey(planID string) string {
	// One key per plan, rather than per attempt, also refuses recovery replay.
	return s.prefix + "/v3/fleet-app-delete-co-permits/" + planID
}

func (s *V3OperationStore) appDeleteTerminalKey(planID string) string {
	return s.prefix + "/v3/fleet-app-delete-terminal/" + planID
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
	consumed := appDeleteConsumedPermit{SchemaVersion: appDeleteConsumedPermitSchema, Request: r,
		TargetID: targetID, TargetFenceGeneration: fence.Generation, AuthorityEpoch: epoch}
	raw, err := json.Marshal(consumed)
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

// RecordAppDeleteTerminal records verifier-owned digests against the consumed
// permit. It deliberately does not change the attempt or target fence.
func (s *V3OperationStore) RecordAppDeleteTerminal(ctx context.Context, r appdelete.TerminalRequest, principal string) (appdelete.TerminalReceipt, error) {
	var zero appdelete.TerminalReceipt
	if s == nil || r.Validate() != nil || principal != "fleet-app-delete-verifier:work-staging" {
		return zero, appdelete.ErrRefused
	}
	permitKey, terminalKey := s.appDeleteCoPermitKey(r.PlanID), s.appDeleteTerminalKey(r.PlanID)
	attemptKey, planKey := s.fleetRunnerAttemptKey(r.PlanID, r.AttemptID), s.opKey(r.PlanID)
	first, err := s.kv.Txn(ctx).Then(clientv3.OpGet(permitKey), clientv3.OpGet(terminalKey), clientv3.OpGet(attemptKey), clientv3.OpGet(planKey)).Commit()
	if err != nil {
		return zero, err
	}
	if len(first.Responses) != 4 {
		return zero, appdelete.ErrRefused
	}
	permits, terminals := first.Responses[0].GetResponseRange().Kvs, first.Responses[1].GetResponseRange().Kvs
	attempts, plans := first.Responses[2].GetResponseRange().Kvs, first.Responses[3].GetResponseRange().Kvs
	if len(permits) != 1 || len(terminals) != 0 || len(attempts) != 1 || len(plans) != 1 {
		return zero, appdelete.ErrRefused
	}
	var permit appDeleteConsumedPermit
	var attempt fleet.RunnerAttempt
	var plan v3Record
	if json.Unmarshal(permits[0].Value, &permit) != nil || permit.SchemaVersion != appDeleteConsumedPermitSchema ||
		permit.Request != r.Permit() || permit.TargetID == "" || permit.TargetFenceGeneration <= 0 || permit.AuthorityEpoch <= 0 ||
		decodeV3Record(attempts[0].Value, &attempt) != nil || decodeV3Record(plans[0].Value, &plan) != nil {
		return zero, appdelete.ErrRefused
	}
	converge, _, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if attempt.ID != r.AttemptID || attempt.PlanID != r.PlanID || attempt.Status != "running" || attempt.CurrentPhase != "provider_applying" || converge == nil {
		return zero, appdelete.ErrRefused
	}
	targetID, empty, _, err := s.ResolveFleetTargetForPlan(ctx, cluster, "")
	if err != nil || empty || targetID == "" || targetID != permit.TargetID {
		return zero, appdelete.ErrRefused
	}
	fenceKey, epochKey := s.fleetTargetFenceKey(targetID), s.fleetAuthorityEpochKey()
	second, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(epochKey)).Commit()
	if err != nil {
		return zero, err
	}
	if len(second.Responses) != 2 || len(second.Responses[0].GetResponseRange().Kvs) != 1 || len(second.Responses[1].GetResponseRange().Kvs) != 1 {
		return zero, appdelete.ErrRefused
	}
	fenceRow, epochRow := second.Responses[0].GetResponseRange().Kvs[0], second.Responses[1].GetResponseRange().Kvs[0]
	var fence v3FleetTargetFence
	epoch, epochErr := strconv.ParseInt(strings.TrimSpace(string(epochRow.Value)), 10, 64)
	if decodeV3Record(fenceRow.Value, &fence) != nil || epochErr != nil || !fence.Held || fence.HolderPlanID != r.PlanID || fence.HolderNonceSHA256 != executor.HolderNonceSHA256(r.PlanID) ||
		fence.Generation != permit.TargetFenceGeneration || fence.AuthorityEpoch != permit.AuthorityEpoch || epoch != permit.AuthorityEpoch {
		return zero, appdelete.ErrRefused
	}
	receipt := appdelete.ReceiptFromTerminal(r)
	raw, err := json.Marshal(receipt)
	if err != nil {
		return zero, err
	}
	committed, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(permitKey), "=", permits[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(terminalKey), "=", 0),
		clientv3.Compare(clientv3.ModRevision(attemptKey), "=", attempts[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(planKey), "=", plans[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fenceRow.ModRevision),
		clientv3.Compare(clientv3.ModRevision(epochKey), "=", epochRow.ModRevision),
	).Then(clientv3.OpPut(terminalKey, string(raw))).Commit()
	if err != nil {
		return zero, err
	}
	if !committed.Succeeded {
		return zero, appdelete.ErrRefused
	}
	return receipt, nil
}

func (s *V3OperationStore) GetAppDeleteTerminal(ctx context.Context, planID string) (appdelete.TerminalReceipt, error) {
	var receipt appdelete.TerminalReceipt
	response, err := s.kv.Get(ctx, s.appDeleteTerminalKey(planID))
	if err != nil {
		return receipt, err
	}
	if response.Count != 1 || len(response.Kvs) != 1 {
		return receipt, appdelete.ErrNotFound
	}
	if json.Unmarshal(response.Kvs[0].Value, &receipt) != nil || receipt.SchemaVersion != appdelete.TerminalReceiptSchema || !receipt.TerminalEvidenceRecorded || receipt.Evidence.Validate() != nil || receipt.Evidence.PlanID != planID {
		return appdelete.TerminalReceipt{}, appdelete.ErrRefused
	}
	return receipt, nil
}
