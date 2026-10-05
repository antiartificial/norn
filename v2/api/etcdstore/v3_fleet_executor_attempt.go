package etcdstore

// Executor-lane attempt admission on etcd (plan-5-7 E2e, ADR 0010), the
// counterpart of store/fleet_executor_attempts.go and mirroring its semantics,
// refusal codes and order of checks. On the executor lane there is no dispatch
// preparation, binding, nonce or claim: attempt start acquires the fence, binds
// it and puts the attempt in ONE Txn.
//
// Compare set of that Txn (every key the decision read; a failed Txn commits
// nothing, so the next round re-reads everything fresh, bounded by
// fleetExecutorStartRounds; a Commit error is indeterminate and never retried):
//
//   - the converge plan's operation key (ModRevision);
//   - the target registry (registryCompare), the target fence and the
//     authority epoch (ModRevision, 0 when absent);
//   - the plan's abandoned key (CreateRevision == 0);
//   - the plan's attempts prefix (bounded by the read revision) and every
//     attempt key read;
//   - the plan's runner plan-state key (ModRevision, 0 when absent);
//   - the recovery source's executor metadata key (ModRevision);
//   - the operation kind-index prefix of each ledger kind that was read
//     (approve, stop attestation, provider evidence), bounded by the read
//     revision, so a row recorded after the read fails the Txn;
//   - for a first start, the whole capacity-plan kind index (bounded by the
//     read revision) and the operation key of each of the resource's plans,
//     so a newer converge plan recorded after the read fails the Txn.
//
// The PG tx stores principal subject, recovery flag and executor metadata in
// fleet_runner_attempts columns; fleet.RunnerAttempt has no such fields, so
// the etcd side keeps them in one immutable sibling record per attempt
// (/v3/fleet-executor-attempts/<plan>/<attempt>) written in the same Txn.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// fleetExecutorStartRounds bounds the re-read retries after a lost Txn.
const fleetExecutorStartRounds = 16

// fleetExecutorHolderDispatchState is HolderFacts.DispatchState of a converge
// plan (no dispatch keys): "bound", as PG's executorHolderDispatchState.
const fleetExecutorHolderDispatchState = "bound"

// ErrFleetExecutorStartContention is returned when every round lost its Txn.
var ErrFleetExecutorStartContention = errors.New("fleet executor attempt start lost its commit race after every retry")

// FleetExecutorStartResult is the admitted attempt. Replayed is true when the
// same runnerAttemptId was already admitted.
type FleetExecutorStartResult struct {
	Attempt  *fleet.RunnerAttempt
	Replayed bool
}

// FleetExecutorAttemptMeta is the executor-only identity stored beside an
// attempt (the PG principal_subject, recovery and metadata columns).
type FleetExecutorAttemptMeta struct {
	PrincipalSubject           string                   `json:"principalSubject"`
	Recovery                   bool                     `json:"recovery,omitempty"`
	Executor                   executor.AttemptMetadata `json:"executor"`
	OperationClass             string                   `json:"operationClass,omitempty"`
	CreatedNodeCount           int                      `json:"createdNodeCount,omitempty"`
	ConsumedApproveOperationID string                   `json:"consumedApproveOperationId,omitempty"`
}

func (s *V3OperationStore) fleetExecutorMetaKey(planID, attemptID string) string {
	return s.prefix + "/v3/fleet-executor-attempts/" + planID + "/" + attemptID
}

// GetFleetExecutorAttemptMeta returns attemptID's executor identity, or
// ErrNotFound for an attempt that is not an executor-lane attempt.
func (s *V3OperationStore) GetFleetExecutorAttemptMeta(ctx context.Context, planID, attemptID string) (*FleetExecutorAttemptMeta, error) {
	meta, _, err := s.loadFleetExecutorMeta(ctx, planID, attemptID)
	return meta, err
}

func (s *V3OperationStore) loadFleetExecutorMeta(ctx context.Context, planID, attemptID string) (*FleetExecutorAttemptMeta, int64, error) {
	response, err := s.kv.Get(ctx, s.fleetExecutorMetaKey(planID, attemptID))
	if err != nil {
		return nil, 0, err
	}
	if len(response.Kvs) == 0 {
		return nil, 0, ErrNotFound
	}
	var meta FleetExecutorAttemptMeta
	if err := decodeV3Record(response.Kvs[0].Value, &meta); err != nil {
		return nil, 0, fmt.Errorf("fleet executor attempt metadata is corrupt: %w", err)
	}
	return &meta, response.Kvs[0].ModRevision, nil
}

func fleetExecutorRefusal(code, format string, args ...interface{}) error {
	return &executor.Error{Code: code, Reason: fmt.Sprintf(format, args...)}
}

// parseFleetConvergePlanPayload returns the converge object, the plan digest
// and the cluster of a fleet.capacity-plan payload. converge is nil for any
// plan that is not a valid converge plan.
func parseFleetConvergePlanPayload(payload map[string]interface{}) (*executor.ConvergePlan, string, string) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, "", ""
	}
	var plan struct {
		Cluster  string                 `json:"cluster"`
		Action   string                 `json:"action"`
		Digest   string                 `json:"digest"`
		Converge *executor.ConvergePlan `json:"converge"`
	}
	if json.Unmarshal(encoded, &plan) != nil {
		return nil, "", ""
	}
	if plan.Action != executor.ActionConverge || plan.Converge == nil || plan.Converge.Validate() != nil {
		return nil, plan.Digest, plan.Cluster
	}
	return plan.Converge, plan.Digest, plan.Cluster
}

// fleetConvergePlanOf returns planID's converge payload (nil when the plan is
// missing or not a converge plan) and the plan operation key's ModRevision
// (0 when absent), for the caller's compare.
func (s *V3OperationStore) fleetConvergePlanOf(ctx context.Context, planID string) (*executor.ConvergePlan, int64, error) {
	record, revision, err := s.load(ctx, planID)
	if errors.Is(err, ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if record.Operation.Kind != "fleet.capacity-plan" {
		return nil, revision, nil
	}
	converge, _, _ := parseFleetConvergePlanPayload(record.Operation.Payload)
	return converge, revision, nil
}

type fleetExecutorLedgerRow struct {
	ID        string
	Payload   map[string]interface{}
	Metadata  map[string]interface{}
	StartedAt time.Time
}

// fleetExecutorLedgerRows reads planID's succeeded ledger operations of kind,
// oldest first (the PG order), and the compare that fails the Txn if any row
// of that kind is recorded after this read. The kind index is not keyed by
// plan, so the whole kind is scanned and filtered by Ref.
func (s *V3OperationStore) fleetExecutorLedgerRows(ctx context.Context, kind, planID string) ([]fleetExecutorLedgerRow, clientv3.Cmp, error) {
	prefix := s.operationKindIndexPrefix(kind)
	response, err := s.kv.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, clientv3.Cmp{}, err
	}
	compare := clientv3.Compare(clientv3.ModRevision(prefix).WithPrefix(), "<", response.Header.Revision+1)
	var rows []fleetExecutorLedgerRow
	for _, kv := range response.Kvs {
		record, _, err := s.load(ctx, string(kv.Value))
		if err != nil {
			return nil, clientv3.Cmp{}, fmt.Errorf("load indexed operation %q: %w", string(kv.Value), err)
		}
		op := record.Operation
		if op.Kind != kind || op.Ref != planID || op.Status != model.OperationSucceeded {
			continue
		}
		rows = append(rows, fleetExecutorLedgerRow{ID: op.ID, Payload: op.Payload, Metadata: op.Metadata, StartedAt: op.StartedAt})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].StartedAt.Equal(rows[j].StartedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].StartedAt.Before(rows[j].StartedAt)
	})
	return rows, compare, nil
}

func decodeFleetLedgerValue(value interface{}, target interface{}) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return json.Unmarshal(encoded, target) == nil
}

// fleetNewestConvergePlan returns the ID of resource's newest recorded
// converge plan ("" when none) and the compares that fail the Txn if any
// capacity plan is recorded afterwards or one of the resource's plans
// changes. Newest is PG's order, (StartedAt, ID) descending
// (store.fleetNewestConvergePlanIDTx), NOT the kind index's acceptance-time
// order: the index time is the acceptance instant, which need not equal the
// operation's StartedAt and ties by ascending ID. The conformance case
// NewestPlanOrder pins it. The kind index is not keyed by resource, so every
// capacity plan is scanned (O(capacity plans); see plan-5-7 hand-offs).
func (s *V3OperationStore) fleetNewestConvergePlan(ctx context.Context, resource string) (string, []clientv3.Cmp, error) {
	prefix := s.operationKindIndexPrefix("fleet.capacity-plan")
	end := clientv3.GetPrefixRangeEnd(prefix)
	start := prefix
	var firstRevision int64
	var compares []clientv3.Cmp
	found, foundAt := "", time.Time{}
	for {
		options := []clientv3.OpOption{clientv3.WithRange(end), clientv3.WithLimit(100), clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend)}
		if firstRevision != 0 {
			options = append(options, clientv3.WithRev(firstRevision))
		}
		response, err := s.kv.Get(ctx, start, options...)
		if err != nil {
			return "", nil, err
		}
		if firstRevision == 0 {
			firstRevision = response.Header.Revision
		}
		for _, kv := range response.Kvs {
			record, revision, err := s.load(ctx, string(kv.Value))
			if err != nil {
				return "", nil, fmt.Errorf("load indexed operation %q: %w", string(kv.Value), err)
			}
			op := record.Operation
			if op.Ref != resource {
				continue
			}
			// A plan of this resource that is not (yet) a succeeded converge
			// plan could become one; compare it too.
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.opKey(op.ID)), "=", revision))
			if op.Status != model.OperationSucceeded {
				continue
			}
			encoded, _ := json.Marshal(op.Payload)
			var plan struct {
				Action   string                 `json:"action"`
				Converge *executor.ConvergePlan `json:"converge"`
			}
			if json.Unmarshal(encoded, &plan) != nil || plan.Action != executor.ActionConverge || plan.Converge == nil || plan.Converge.Resource != resource {
				continue
			}
			if found == "" || op.StartedAt.After(foundAt) || (op.StartedAt.Equal(foundAt) && op.ID > found) {
				found, foundAt = op.ID, op.StartedAt
			}
		}
		if !response.More || len(response.Kvs) == 0 {
			break
		}
		start = string(response.Kvs[len(response.Kvs)-1].Key) + "\x00"
	}
	compares = append(compares, clientv3.Compare(clientv3.ModRevision(prefix).WithPrefix(), "<", firstRevision+1))
	return found, compares, nil
}

func fleetExecutorReplayMatches(item fleet.RunnerAttempt, meta *FleetExecutorAttemptMeta, in store.FleetExecutorStart) bool {
	request := in.Request
	return meta != nil && meta.Executor == request.Executor &&
		item.RunnerAttemptID == request.RunnerAttemptID && item.CommitSHA == request.CommitSHA &&
		item.PlanSHA256 == request.PlanSHA256 && item.WorkflowURL == request.WorkflowURL &&
		meta.Recovery == request.Resume && meta.PrincipalSubject == in.PrincipalSubject &&
		item.HeartbeatTimeoutSeconds == request.HeartbeatTimeoutSeconds &&
		meta.OperationClass == request.OperationClass && meta.CreatedNodeCount == request.CreatedNodeCount
}

// CreateFleetExecutorAttempt admits the first attempt of an executor-lane
// converge plan, or one recovery attempt when in.Request.Resume is set.
// Refusals are *lifecycle.FenceError (fence, epoch, registry, abandon) or
// *executor.Error (everything else), exactly as store.DB.CreateFleetExecutorAttempt.
func (s *V3OperationStore) CreateFleetExecutorAttempt(ctx context.Context, in store.FleetExecutorStart) (*FleetExecutorStartResult, error) {
	if in.PlanID == "" || in.PrincipalSubject == "" || in.Request.HeartbeatTimeoutSeconds <= 0 {
		return nil, fmt.Errorf("plan, principal subject and a normalized heartbeat timeout are required")
	}
	for round := 0; round < fleetExecutorStartRounds; round++ {
		if round > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(round) * 3 * time.Millisecond):
			}
		}
		result, retry, err := s.tryFleetExecutorStart(ctx, in)
		if err != nil {
			return nil, err
		}
		if !retry {
			return result, nil
		}
	}
	return nil, ErrFleetExecutorStartContention
}

func (s *V3OperationStore) tryFleetExecutorStart(ctx context.Context, in store.FleetExecutorStart) (*FleetExecutorStartResult, bool, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC().Truncate(time.Microsecond)
	planID := in.PlanID

	attempts, compares, err := s.fleetResourceAttemptFacts(ctx, planID)
	if err != nil {
		return nil, false, err
	}
	// T6: replay first, before any plan, fence, approval or evidence check.
	for index := range attempts {
		if attempts[index].RunnerAttemptID != in.Request.RunnerAttemptID {
			continue
		}
		meta, _, err := s.loadFleetExecutorMeta(ctx, planID, attempts[index].ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, false, err
		}
		if !fleetExecutorReplayMatches(attempts[index], meta, in) {
			return nil, false, fleetExecutorRefusal(store.CodeFleetExecutorBindingConflict, "runnerAttemptId is already bound to different reviewed input")
		}
		item := attempts[index]
		return &FleetExecutorStartResult{Attempt: &item, Replayed: true}, false, nil
	}

	record, planRevision, err := s.load(ctx, planID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	if errors.Is(err, ErrNotFound) || record.Operation.Kind != "fleet.capacity-plan" {
		return nil, false, fleetExecutorRefusal(executor.CodeFleetPlanNotConverge, "the plan is not a converge plan")
	}
	converge, digest, cluster := parseFleetConvergePlanPayload(record.Operation.Payload)
	if converge == nil {
		return nil, false, fleetExecutorRefusal(executor.CodeFleetPlanNotConverge, "the plan is not a converge plan")
	}
	digestHex := ""
	if len(digest) == len("sha256:")+64 && digest[:7] == "sha256:" {
		digestHex = digest[7:]
	}
	if in.Request.CommitSHA != converge.CommitSHA || in.Request.PlanSHA256 != digestHex {
		return nil, false, fleetExecutorRefusal(executor.CodeFleetConvergeCommitMismatch, "commitSha and planSha256 must equal the converge plan's")
	}
	if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, planID); err != nil {
		return nil, false, err
	} else if abandoned {
		return nil, false, fleetHolderAbandonedError()
	}

	// Registry, epoch and fence. The executor lane requires a registered
	// target, so an empty registry is as refused as an unknown cluster.
	fenceCtx, err := s.loadFleetTargetFenceContext(ctx, cluster, "", true)
	if err != nil {
		return nil, false, err
	}
	if fenceCtx.TargetID == "" {
		return nil, false, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetUnregistered}
	}
	nonce := executor.HolderNonceSHA256(planID)
	stateKey := s.fleetRunnerPlanStateKey(planID)
	stateResponse, err := s.kv.Get(ctx, stateKey)
	if err != nil {
		return nil, false, err
	}
	var state v3FleetRunnerPlanState
	var stateRevision int64
	if len(stateResponse.Kvs) == 1 {
		if err := decodeV3Record(stateResponse.Kvs[0].Value, &state); err != nil || state.Version <= 0 {
			return nil, false, fmt.Errorf("fleet runner plan state is corrupt")
		}
		stateRevision = stateResponse.Kvs[0].ModRevision
	}

	compares = append(compares,
		clientv3.Compare(clientv3.ModRevision(s.opKey(planID)), "=", planRevision),
		s.registryCompare(fenceCtx), s.abandonedCompare(planID),
		clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(fenceCtx.TargetID)), "=", fenceCtx.FenceRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", fenceCtx.EpochRevision),
		clientv3.Compare(clientv3.ModRevision(stateKey), "=", stateRevision),
	)

	attemptID := uuid.NewString()
	item := fleet.RunnerAttempt{
		SchemaVersion: fleet.RunnerAttemptSchemaVersion, ID: attemptID, PlanID: planID, Attempt: len(attempts) + 1,
		RunnerAttemptID: in.Request.RunnerAttemptID, Status: "running", RootAttemptID: attemptID,
		CommitSHA: in.Request.CommitSHA, PlanSHA256: in.Request.PlanSHA256, WorkflowURL: in.Request.WorkflowURL,
		HeartbeatTimeoutSeconds: in.Request.HeartbeatTimeoutSeconds, Revision: 1,
		StartedAt: now, HeartbeatAt: now, HeartbeatExpiresAt: now.Add(time.Duration(in.Request.HeartbeatTimeoutSeconds) * time.Second), UpdatedAt: now,
	}
	meta := FleetExecutorAttemptMeta{
		PrincipalSubject: in.PrincipalSubject, Recovery: in.Request.Resume, Executor: in.Request.Executor,
		OperationClass: in.Request.OperationClass, CreatedNodeCount: in.Request.CreatedNodeCount,
	}
	var nextFence lifecycle.FenceFacts
	var cancelPrevious *fleet.RunnerAttempt
	if !in.Request.Resume {
		if len(attempts) > 0 {
			return nil, false, fleetExecutorRefusal(store.CodeFleetExecutorRetryRequired, "this plan already has runner history; recover the latest attempt")
		}
		// D5 "never older": a first start must be the resource's newest
		// recorded converge plan. Recovery is exempt.
		newest, newestCompares, err := s.fleetNewestConvergePlan(ctx, converge.Resource)
		if err != nil {
			return nil, false, err
		}
		if newest != planID {
			return nil, false, fleetExecutorRefusal(executor.CodeFleetExecutorPlanNotNewest, "a newer converge plan has been recorded for resource %s", converge.Resource)
		}
		compares = append(compares, newestCompares...)
		if converge.RequiresApproval {
			approveCompare, err := s.requireFleetConvergeApproval(ctx, planID, converge.CommitSHA)
			if err != nil {
				return nil, false, err
			}
			compares = append(compares, approveCompare)
		}
		acquired, err := lifecycle.DecideAcquire(fenceCtx.Fence, planID, nonce, record.Operation.StartedAt, fenceCtx.Epoch)
		if err != nil {
			return nil, false, err
		}
		nextFence, err = lifecycle.DecideBind(acquired, planID, nonce, fenceCtx.Epoch)
		if err != nil {
			return nil, false, err
		}
		item.CurrentPhase = lifecycle.Phases(lifecycle.PostgresLegacy, false)[0]
	} else {
		if len(attempts) == 0 {
			return nil, false, fleetExecutorRefusal(store.CodeFleetExecutorRetryRequired, "a durable source runner attempt is required before recovery")
		}
		previous := attempts[len(attempts)-1]
		sourceMeta, sourceMetaRevision, err := s.loadFleetExecutorMeta(ctx, planID, previous.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, false, err
		}
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.fleetExecutorMetaKey(planID, previous.ID)), "=", sourceMetaRevision))
		recoveryCompares, consumed, err := s.authorizeFleetExecutorRecovery(ctx, in, converge, previous, sourceMeta, attempts, now)
		if err != nil {
			return nil, false, err
		}
		compares = append(compares, recoveryCompares...)
		nextFence, err = lifecycle.DecideBind(fenceCtx.Fence, planID, nonce, fenceCtx.Epoch)
		if err != nil {
			return nil, false, err
		}
		item.CurrentPhase, item.RetryOf, item.RootAttemptID = previous.CurrentPhase, previous.ID, attempts[0].RootAttemptID
		meta.ConsumedApproveOperationID = consumed
		if previous.Status == "queued" || previous.Status == "running" {
			canceled := previous
			canceled.Status, canceled.LastError, canceled.FinishedAt = "canceled", "superseded by verified executor recovery", &now
			canceled.Revision, canceled.UpdatedAt = previous.Revision+1, now
			cancelPrevious = &canceled
		}
	}

	fenceRecord, err := json.Marshal(fleetTargetFenceRecord(nextFence))
	if err != nil {
		return nil, false, err
	}
	attemptRecord, err := json.Marshal(item)
	if err != nil {
		return nil, false, err
	}
	metaRecord, err := json.Marshal(meta)
	if err != nil {
		return nil, false, err
	}
	nextState, _ := json.Marshal(v3FleetRunnerPlanState{Version: state.Version + 1})
	puts := []clientv3.Op{
		clientv3.OpPut(s.fleetRunnerAttemptKey(planID, item.ID), string(attemptRecord)),
		clientv3.OpPut(s.fleetExecutorMetaKey(planID, item.ID), string(metaRecord)),
		clientv3.OpPut(stateKey, string(nextState)),
		clientv3.OpPut(s.fleetTargetFenceKey(fenceCtx.TargetID), string(fenceRecord)),
	}
	if cancelPrevious != nil {
		encoded, err := json.Marshal(cancelPrevious)
		if err != nil {
			return nil, false, err
		}
		puts = append(puts, clientv3.OpPut(s.fleetRunnerAttemptKey(planID, cancelPrevious.ID), string(encoded)))
	}
	txn, err := s.kv.Txn(ctx).If(compares...).Then(puts...).Commit()
	if err != nil {
		// Never retried: the Txn may have committed. The caller replays by
		// runnerAttemptId (T6).
		return nil, false, &store.AcceptanceIndeterminateError{Err: err}
	}
	if !txn.Succeeded {
		return nil, true, nil
	}
	return &FleetExecutorStartResult{Attempt: &item}, false, nil
}

// requireFleetConvergeApproval admits a requiresApproval plan only with a
// non-resume approve for exactly this plan and commit.
func (s *V3OperationStore) requireFleetConvergeApproval(ctx context.Context, planID, commitSHA string) (clientv3.Cmp, error) {
	rows, compare, err := s.fleetExecutorLedgerRows(ctx, executor.KindConvergeApprove, planID)
	if err != nil {
		return clientv3.Cmp{}, err
	}
	for _, row := range rows {
		var approve executor.ApproveAdmission
		if decodeFleetLedgerValue(row.Payload, &approve) && approve.PlanID == planID && approve.CommitSHA == commitSHA && !approve.Resume {
			return compare, nil
		}
	}
	return clientv3.Cmp{}, fleetExecutorRefusal(executor.CodeFleetConvergeApprovalRequired, "the plan requires a matching approve")
}

// authorizeFleetExecutorRecovery decides one recovery: a source that may be
// recovered, an unconsumed resume approve and stop proof (a valid attestation
// or admin provider evidence). It returns the compares for the ledger it read
// and the consumed approve's ID.
func (s *V3OperationStore) authorizeFleetExecutorRecovery(ctx context.Context, in store.FleetExecutorStart, converge *executor.ConvergePlan, previous fleet.RunnerAttempt, sourceMeta *FleetExecutorAttemptMeta, attempts []fleet.RunnerAttempt, now time.Time) ([]clientv3.Cmp, string, error) {
	if sourceMeta == nil || sourceMeta.Executor.Lane != executor.LaneMetadataValue {
		return nil, "", fleetExecutorRefusal(store.CodeFleetExecutorBindingConflict, "the source attempt is not an executor-lane attempt")
	}
	if previous.Status == "succeeded" {
		return nil, "", fleetExecutorRefusal(store.CodeFleetExecutorResumeConflict, "the source attempt already succeeded")
	}
	if previous.CommitSHA != in.Request.CommitSHA || previous.PlanSHA256 != in.Request.PlanSHA256 {
		return nil, "", fleetExecutorRefusal(store.CodeFleetExecutorBindingConflict, "recovery does not match the durable source runner attempt")
	}
	// The approve is checked first so a second recovery against one approve is
	// reported as consumed whatever proof it carries.
	approve, approveCompare, err := s.consumeFleetResumeApprove(ctx, in.PlanID, converge.CommitSHA, previous, attempts)
	if err != nil {
		return nil, "", err
	}
	proofCompares, err := s.requireFleetExecutorStopProof(ctx, in, previous, sourceMeta, now)
	if err != nil {
		return nil, "", err
	}
	return append([]clientv3.Cmp{approveCompare}, proofCompares...), approve, nil
}

func (s *V3OperationStore) requireFleetExecutorStopProof(ctx context.Context, in store.FleetExecutorStart, previous fleet.RunnerAttempt, sourceMeta *FleetExecutorAttemptMeta, now time.Time) ([]clientv3.Cmp, error) {
	attestations, attestationCompare, err := s.fleetExecutorLedgerRows(ctx, executor.KindStopAttestation, in.PlanID)
	if err != nil {
		return nil, err
	}
	compares := []clientv3.Cmp{attestationCompare}
	var firstRefusal error
	for index := len(attestations) - 1; index >= 0; index-- {
		var payload executor.StopAttestationPayload
		if !decodeFleetLedgerValue(attestations[index].Payload, &payload) || payload.PlanID != in.PlanID || payload.SourceAttemptID != previous.ID {
			continue
		}
		attester, _ := attestations[index].Metadata[store.FleetExecutorAttesterMetadataKey].(string)
		err := executor.ValidStopAttestation(payload.StopAttestationRequest, sourceMeta.Executor, sourceMeta.PrincipalSubject, attester, now)
		if err == nil {
			// The attestation vouches for this host and this invocation: the
			// recovery must start from the attesting loop itself.
			request := in.Request.Executor
			if in.PrincipalSubject != sourceMeta.PrincipalSubject || request.DropletID != payload.DropletID ||
				request.MachineID != payload.MachineID || request.InvocationID != payload.InvocationID {
				err = fleetExecutorRefusal(executor.CodeFleetExecutorStopAttestationInvalid, "the recovery must start from the attesting droplet, machine and invocation")
			}
		}
		if err == nil {
			return compares, nil
		}
		if firstRefusal == nil {
			firstRefusal = err
		}
	}
	evidence, evidenceCompare, err := s.fleetExecutorProviderEvidence(ctx, in.PlanID, previous.ID, sourceMeta.Executor, now)
	if err != nil {
		return nil, err
	}
	if evidence {
		return append(compares, evidenceCompare), nil
	}
	if firstRefusal != nil {
		return nil, fleetExecutorRefusal(executor.CodeFleetExecutorStopAttestationInvalid, "%s", firstRefusal.Error())
	}
	return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetRecoveryRequiresStoppedSource,
		Reason: "executor recovery needs a valid stop attestation or admin provider evidence"}
}

// fleetExecutorProviderEvidence reports whether the ledger holds an admin
// store.KindFleetExecutorProviderEvidence assertion for planID that names
// exactly the source attempt and its droplet, is still fresh and whose
// server-stored digest equals the digest of the signed evidence. Only that
// kind counts.
func (s *V3OperationStore) fleetExecutorProviderEvidence(ctx context.Context, planID, sourceAttemptID string, source executor.AttemptMetadata, now time.Time) (bool, clientv3.Cmp, error) {
	rows, compare, err := s.fleetExecutorLedgerRows(ctx, store.KindFleetExecutorProviderEvidence, planID)
	if err != nil {
		return false, clientv3.Cmp{}, err
	}
	for _, row := range rows {
		var payload store.FleetExecutorProviderEvidencePayload
		digest, _ := row.Metadata[executor.ProviderEvidenceDigestMetadataKey].(string)
		if !decodeFleetLedgerValue(row.Payload, &payload) || payload.PlanID != planID || payload.SourceAttemptID != sourceAttemptID {
			continue
		}
		evidence := payload.ProviderEvidence
		if evidence.Validate(now) == nil && evidence.DropletID == source.DropletID && digest != "" && evidence.Digest() == digest {
			return true, compare, nil
		}
	}
	return false, compare, nil
}

// consumeFleetResumeApprove finds the resume approve naming previous and not
// yet consumed. An approve is consumed when its source attempt already has a
// recovery child (attempts are compared, so a racing recovery fails the Txn
// and the next round reports the approve consumed).
func (s *V3OperationStore) consumeFleetResumeApprove(ctx context.Context, planID, commitSHA string, previous fleet.RunnerAttempt, attempts []fleet.RunnerAttempt) (string, clientv3.Cmp, error) {
	rows, compare, err := s.fleetExecutorLedgerRows(ctx, executor.KindConvergeApprove, planID)
	if err != nil {
		return "", clientv3.Cmp{}, err
	}
	hasChild := map[string]bool{}
	for _, attempt := range attempts {
		if attempt.RetryOf != "" {
			hasChild[attempt.RetryOf] = true
		}
	}
	consumed := false
	for _, row := range rows {
		var approve executor.ApproveAdmission
		if !decodeFleetLedgerValue(row.Payload, &approve) || approve.PlanID != planID || approve.CommitSHA != commitSHA || !approve.Resume {
			continue
		}
		if hasChild[approve.SourceAttemptID] {
			consumed = true
			continue
		}
		if approve.SourceAttemptID == previous.ID {
			return row.ID, compare, nil
		}
	}
	if consumed {
		return "", clientv3.Cmp{}, fleetExecutorRefusal(executor.CodeFleetConvergeApprovalConsumed, "the resume approve was already consumed by a recovery attempt")
	}
	return "", clientv3.Cmp{}, fleetExecutorRefusal(executor.CodeFleetConvergeApprovalRequired, "recovery requires a resume approve for the latest attempt")
}
