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

	"go.etcd.io/etcd/api/v3/mvccpb"
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

func (s *V3OperationStore) appDeleteFinalizeKey(planID string) string {
	return s.prefix + "/v3/fleet-app-delete-finalized/" + planID
}

// FinalizeAppDelete closes the exact terminal-evidenced attempt and its original
// target fence in one CAS. It never accepts an executor-supplied success claim.
func (s *V3OperationStore) FinalizeAppDelete(ctx context.Context, r appdelete.FinalizeRequest, principal string) (appdelete.FinalizeReceipt, error) {
	var zero appdelete.FinalizeReceipt
	if s == nil || r.Validate() != nil || principal != "fleet-app-delete-verifier:work-staging" {
		return zero, appdelete.ErrRefused
	}
	permitKey, terminalKey, finalizeKey := s.appDeleteCoPermitKey(r.PlanID), s.appDeleteTerminalKey(r.PlanID), s.appDeleteFinalizeKey(r.PlanID)
	attemptKey, planKey, stateKey := s.fleetRunnerAttemptKey(r.PlanID, r.AttemptID), s.opKey(r.PlanID), s.fleetRunnerPlanStateKey(r.PlanID)
	first, err := s.kv.Txn(ctx).Then(clientv3.OpGet(permitKey), clientv3.OpGet(terminalKey), clientv3.OpGet(finalizeKey), clientv3.OpGet(attemptKey), clientv3.OpGet(planKey), clientv3.OpGet(stateKey), clientv3.OpGet(s.fleetTargetAbandonedKey(r.PlanID))).Commit()
	if err != nil {
		return zero, err
	}
	if len(first.Responses) != 7 {
		return zero, appdelete.ErrRefused
	}
	get := func(i int) []*mvccpb.KeyValue { return first.Responses[i].GetResponseRange().Kvs }
	if len(get(0)) != 1 || len(get(1)) != 1 || len(get(2)) != 0 || len(get(3)) != 1 || len(get(4)) != 1 || len(get(5)) != 1 || len(get(6)) != 0 {
		return zero, appdelete.ErrRefused
	}
	var permit appDeleteConsumedPermit
	var terminal appdelete.TerminalReceipt
	var attempt fleet.RunnerAttempt
	var plan v3Record
	var state v3FleetRunnerPlanState
	if json.Unmarshal(get(0)[0].Value, &permit) != nil || json.Unmarshal(get(1)[0].Value, &terminal) != nil || decodeV3Record(get(3)[0].Value, &attempt) != nil || decodeV3Record(get(4)[0].Value, &plan) != nil || decodeV3Record(get(5)[0].Value, &state) != nil || state.Version <= 0 {
		return zero, appdelete.ErrRefused
	}
	if permit.SchemaVersion != appDeleteConsumedPermitSchema || terminal.SchemaVersion != appdelete.TerminalReceiptSchema || !terminal.TerminalEvidenceRecorded || terminal.Evidence.Validate() != nil || terminal.Evidence.Permit() != permit.Request || terminal.Evidence.PlanID != r.PlanID || terminal.Evidence.AttemptID != r.AttemptID || appdelete.TerminalReceiptSHA256(terminal) != r.TerminalReceiptSHA256 || attempt.ID != r.AttemptID || attempt.PlanID != r.PlanID || attempt.Status != "running" || attempt.CurrentPhase != "provider_applying" || permit.TargetID == "" || permit.TargetFenceGeneration <= 0 || permit.AuthorityEpoch <= 0 {
		return zero, appdelete.ErrRefused
	}
	converge, digest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.Kind != "fleet.capacity-plan" || converge == nil || attempt.CommitSHA != converge.CommitSHA || "sha256:"+attempt.PlanSHA256 != digest {
		return zero, appdelete.ErrRefused
	}
	targetID, empty, _, err := s.ResolveFleetTargetForPlan(ctx, cluster, "")
	if err != nil || empty || targetID != permit.TargetID {
		return zero, appdelete.ErrRefused
	}
	fenceKey, epochKey := s.fleetTargetFenceKey(targetID), s.fleetAuthorityEpochKey()
	second, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(epochKey), clientv3.OpGet(s.fleetTargetKey(targetID)), clientv3.OpGet(s.fleetTargetRegistryKey())).Commit()
	if err != nil {
		return zero, err
	}
	if len(second.Responses) != 4 {
		return zero, appdelete.ErrRefused
	}
	fences, epochs, targets, registry := second.Responses[0].GetResponseRange().Kvs, second.Responses[1].GetResponseRange().Kvs, second.Responses[2].GetResponseRange().Kvs, second.Responses[3].GetResponseRange().Kvs
	if len(fences) != 1 || len(epochs) != 1 || len(targets) != 1 || len(registry) > 1 {
		return zero, appdelete.ErrRefused
	}
	var fence v3FleetTargetFence
	epoch, epochErr := strconv.ParseInt(strings.TrimSpace(string(epochs[0].Value)), 10, 64)
	if decodeV3Record(fences[0].Value, &fence) != nil || epochErr != nil || !fence.Held || fence.HolderPlanID != r.PlanID || fence.HolderNonceSHA256 != executor.HolderNonceSHA256(r.PlanID) || fence.Generation != permit.TargetFenceGeneration || fence.AuthorityEpoch != permit.AuthorityEpoch || epoch != permit.AuthorityEpoch {
		return zero, appdelete.ErrRefused
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	attempt.Status, attempt.CurrentPhase, attempt.FinishedAt, attempt.UpdatedAt = "succeeded", "complete", &now, now
	attempt.Revision++
	fence.Held, fence.HolderPlanID, fence.HolderNonceSHA256 = false, "", ""
	fence.LastReleasePlanID, fence.LastReleaseReason, fence.LastReleaseAt = r.PlanID, "succeeded", &now
	fence.Revision++
	receipt := appdelete.FinalizeReceipt{SchemaVersion: appdelete.FinalizeReceiptSchema, PlanID: r.PlanID, AttemptID: r.AttemptID, TerminalReceiptSHA256: r.TerminalReceiptSHA256, TargetID: targetID, TargetFenceGeneration: permit.TargetFenceGeneration, AttemptCompleted: true, FenceReleased: true}
	attemptRaw, err := json.Marshal(attempt)
	if err != nil {
		return zero, err
	}
	fenceRaw, err := json.Marshal(fence)
	if err != nil {
		return zero, err
	}
	stateRaw, err := json.Marshal(v3FleetRunnerPlanState{Version: state.Version + 1})
	if err != nil {
		return zero, err
	}
	receiptRaw, err := json.Marshal(receipt)
	if err != nil {
		return zero, err
	}
	ingressCompares, ingressOps, err := s.activeFleetIngressTransition(ctx, r.PlanID, attempt)
	if err != nil {
		return zero, err
	}
	registryCompare := clientv3.Compare(clientv3.CreateRevision(s.fleetTargetRegistryKey()), "=", 0)
	if len(registry) == 1 {
		registryCompare = clientv3.Compare(clientv3.ModRevision(s.fleetTargetRegistryKey()), "=", registry[0].ModRevision)
	}
	compares := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(permitKey), "=", get(0)[0].ModRevision), clientv3.Compare(clientv3.ModRevision(terminalKey), "=", get(1)[0].ModRevision), clientv3.Compare(clientv3.CreateRevision(finalizeKey), "=", 0), clientv3.Compare(clientv3.ModRevision(attemptKey), "=", get(3)[0].ModRevision), clientv3.Compare(clientv3.ModRevision(planKey), "=", get(4)[0].ModRevision), clientv3.Compare(clientv3.ModRevision(stateKey), "=", get(5)[0].ModRevision), clientv3.Compare(clientv3.CreateRevision(s.fleetTargetAbandonedKey(r.PlanID)), "=", 0), clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fences[0].ModRevision), clientv3.Compare(clientv3.ModRevision(epochKey), "=", epochs[0].ModRevision), clientv3.Compare(clientv3.ModRevision(s.fleetTargetKey(targetID)), "=", targets[0].ModRevision), registryCompare}
	compares = append(compares, ingressCompares...)
	puts := append([]clientv3.Op{clientv3.OpPut(attemptKey, string(attemptRaw)), clientv3.OpPut(stateKey, string(stateRaw)), clientv3.OpPut(fenceKey, string(fenceRaw)), clientv3.OpPut(finalizeKey, string(receiptRaw))}, ingressOps...)
	committed, err := s.kv.Txn(ctx).If(compares...).Then(puts...).Commit()
	if err != nil {
		return zero, err
	}
	if !committed.Succeeded {
		return zero, appdelete.ErrRefused
	}
	return receipt, nil
}

func (s *V3OperationStore) GetAppDeleteFinalize(ctx context.Context, planID string) (appdelete.FinalizeReceipt, error) {
	var receipt appdelete.FinalizeReceipt
	response, err := s.kv.Get(ctx, s.appDeleteFinalizeKey(planID))
	if err != nil {
		return receipt, err
	}
	if len(response.Kvs) != 1 {
		return receipt, appdelete.ErrNotFound
	}
	if json.Unmarshal(response.Kvs[0].Value, &receipt) != nil || receipt.SchemaVersion != appdelete.FinalizeReceiptSchema || receipt.PlanID != planID || receipt.AttemptID == "" || receipt.TargetID == "" || receipt.TargetFenceGeneration <= 0 || !receipt.AttemptCompleted || !receipt.FenceReleased {
		return appdelete.FinalizeReceipt{}, appdelete.ErrRefused
	}
	return receipt, nil
}
