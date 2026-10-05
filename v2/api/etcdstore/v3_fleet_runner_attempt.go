package etcdstore

// The Fleet runner aggregate is deliberately kept separate from the normal
// operation records.  A runner attempt is a lease-protected execution fact,
// while its operation receipt is immutable and signed.  The per-plan state
// record is the compare-and-swap fence that makes creation, recovery, and
// phase updates one-winner operations across API processes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type FleetRunnerDispatchBinding struct {
	PlanID              string
	PlanSHA256          string
	ApprovedHeadSHA     string
	DispatchNonceSHA256 string
	RunID               int64
	WorkflowURL         string
}

type v3FleetRunnerDispatch struct {
	FleetRunnerDispatchBinding
	CreatedAt time.Time `json:"createdAt"`
}

type v3FleetRunnerPlanState struct {
	Version int64 `json:"version"`
}

func (s *V3OperationStore) fleetRunnerDispatchKey(planID string) string {
	return s.prefix + "/v3/fleet-runner-dispatches/" + planID
}
func (s *V3OperationStore) fleetRunnerPlanStateKey(planID string) string {
	return s.prefix + "/v3/fleet-runner-plan-state/" + planID
}
func (s *V3OperationStore) fleetRunnerAttemptKey(planID, attemptID string) string {
	return s.prefix + "/v3/fleet-runner-attempts/" + planID + "/" + attemptID
}
func (s *V3OperationStore) fleetRunnerAttemptPrefix(planID string) string {
	return s.prefix + "/v3/fleet-runner-attempts/" + planID + "/"
}

// BindFleetRunnerDispatch is retained only as a fail-closed compatibility
// boundary. A runner binding must be created with its signed dispatch receipt
// by FinishFleetGitHubDispatch.
func (s *V3OperationStore) BindFleetRunnerDispatch(context.Context, FleetRunnerDispatchBinding) error {
	return fmt.Errorf("unsigned fleet runner dispatch binding is no longer supported; use FinishFleetGitHubDispatch")
}

func (s *V3OperationStore) loadFleetRunnerDispatch(ctx context.Context, planID string) (v3FleetRunnerDispatch, error) {
	response, err := s.kv.Get(ctx, s.fleetRunnerDispatchKey(planID))
	if err != nil {
		return v3FleetRunnerDispatch{}, err
	}
	if len(response.Kvs) != 1 {
		return v3FleetRunnerDispatch{}, ErrNotFound
	}
	var item v3FleetRunnerDispatch
	if err := decodeV3Record(response.Kvs[0].Value, &item); err != nil {
		return v3FleetRunnerDispatch{}, err
	}
	if item.PlanID != planID || !fleetLowerHex(item.PlanSHA256, 64) || !fleetLowerHex(item.ApprovedHeadSHA, 40) || !fleetLowerHex(item.DispatchNonceSHA256, 64) || item.RunID <= 0 || !fleetWorkflowURL(item.WorkflowURL) {
		return v3FleetRunnerDispatch{}, fmt.Errorf("fleet runner dispatch binding is corrupt")
	}
	return item, nil
}

// GetFleetRunnerDispatch returns the immutable protected-dispatch binding for
// read-only plan inspection. It never exposes a raw dispatch nonce.
func (s *V3OperationStore) GetFleetRunnerDispatch(ctx context.Context, planID string) (FleetRunnerDispatchBinding, error) {
	planID = strings.TrimSpace(planID)
	if _, err := uuid.Parse(planID); err != nil {
		return FleetRunnerDispatchBinding{}, fmt.Errorf("fleet runner dispatch plan ID must be a UUID")
	}
	item, err := s.loadFleetRunnerDispatch(ctx, planID)
	if err != nil {
		return FleetRunnerDispatchBinding{}, err
	}
	return item.FleetRunnerDispatchBinding, nil
}

func (s *V3OperationStore) acceptFleetRunnerAttempt(ctx context.Context, input store.OperationAcceptance) (store.AcceptedOperation, error) {
	if err := store.NormalizeFleetRunnerAttemptAcceptance(&input); err != nil {
		return store.AcceptedOperation{}, err
	}
	if s.policy.ReplayTTL > 0 {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "etcd runner-attempt replay expiry is not yet qualified"}
	}
	// B1/H7: a plan the operator has permanently abandoned can never admit a
	// new attempt (first attempt or recovery), checked before replay
	// resolution so a fresh identity never slips through as if accepted.
	if input.FleetRunnerAttempt != nil {
		if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, input.FleetRunnerAttempt.PlanID); err != nil {
			return store.AcceptedOperation{}, err
		} else if abandoned {
			return store.AcceptedOperation{}, fleetAttemptError(lifecycle.CodeFleetTargetHolderAbandoned, "fleet target holder plan is permanently abandoned")
		}
	}
	acceptance, err := s.normalize(input)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	key := s.acceptanceKey(acceptance.Identity)
	if existing, err := s.loadAcceptance(ctx, key); err == nil {
		return s.replay(ctx, key, existing, acceptance.Identity, acceptance.Fingerprint)
	} else if !errors.Is(err, ErrNotFound) {
		return store.AcceptedOperation{}, err
	}

	admission := acceptance.FleetRunnerAttempt
	plan, planRevision, err := s.load(ctx, admission.PlanID)
	if err != nil {
		return store.AcceptedOperation{}, fleetAttemptError("fleet_plan_not_found", "fleet capacity plan not found")
	}
	if plan.Operation.Kind != "fleet.capacity-plan" || plan.Operation.Status != model.OperationSucceeded {
		return store.AcceptedOperation{}, fleetAttemptError("fleet_plan_invalid", "fleet capacity plan is not a successful immutable plan")
	}
	dispatchResponse, err := s.kv.Get(ctx, s.fleetRunnerDispatchKey(admission.PlanID))
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if len(dispatchResponse.Kvs) != 1 {
		return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_dispatch_mismatch", "runner attempt has no protected dispatch binding")
	}
	var dispatch v3FleetRunnerDispatch
	if err := decodeV3Record(dispatchResponse.Kvs[0].Value, &dispatch); err != nil {
		return store.AcceptedOperation{}, err
	}
	if dispatch.PlanSHA256 != admission.PlanSHA256 || dispatch.ApprovedHeadSHA != admission.CommitSHA || dispatch.DispatchNonceSHA256 != admission.DispatchNonceSHA256 || admission.SourceDispatchRunID != strconv.FormatInt(dispatch.RunID, 10) {
		return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_dispatch_mismatch", "runner attempt does not match protected dispatch")
	}
	if err := s.VerifyFleetGitHubDispatchCompletion(ctx, dispatch.FleetRunnerDispatchBinding); err != nil {
		return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_dispatch_mismatch", "runner attempt dispatch receipt is not signed and complete")
	}
	if admission.WorkloadIntent == "apply" && (admission.WorkloadRunID != strconv.FormatInt(dispatch.RunID, 10) || admission.WorkloadSHA != dispatch.ApprovedHeadSHA) {
		return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_identity_mismatch", "apply workload does not match protected dispatch run")
	}
	stateResponse, err := s.kv.Get(ctx, s.fleetRunnerPlanStateKey(admission.PlanID))
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if len(stateResponse.Kvs) != 1 {
		return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_dispatch_mismatch", "runner attempt state is unavailable")
	}
	var state v3FleetRunnerPlanState
	if err := decodeV3Record(stateResponse.Kvs[0].Value, &state); err != nil || state.Version <= 0 {
		return store.AcceptedOperation{}, fmt.Errorf("fleet runner plan state is corrupt")
	}
	attempts, attemptRevisions, err := s.listFleetRunnerAttempts(ctx, admission.PlanID)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if err := fleetValidateLineage(attempts); err != nil {
		return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_lineage_invalid", err.Error())
	}
	var preparationResponse *clientv3.GetResponse
	if len(attempts) > 0 {
		preparationResponse, err = s.kv.Get(ctx, s.fleetGitHubDispatchPreparationKey(admission.PlanID))
		if err != nil || len(preparationResponse.Kvs) != 1 {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_dispatch_mismatch", "runner attempt dispatch preparation is unavailable")
		}
	}
	for _, item := range attempts {
		if item.RunnerAttemptID == admission.RunnerAttemptID {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_identity_conflict", "runner attempt identity already accepted")
		}
		if item.CommitSHA != admission.CommitSHA || item.PlanSHA256 != admission.PlanSHA256 {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_binding_mismatch", "fleet plan attempts use different reviewed input")
		}
	}
	if len(attempts) == 0 {
		if admission.Resume || admission.WorkloadIntent != "apply" || admission.ExpectedPredecessorID != "" {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_resume_invalid", "fleet recovery requires a prior runner attempt")
		}
	} else {
		predecessor := &attempts[len(attempts)-1]
		if admission.ExpectedPredecessorID != predecessor.ID {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_stale", "selected predecessor changed")
		}
		if !admission.Resume || admission.WorkloadIntent != "recover" {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_resume_required", "successor must be a protected recovery")
		}
		if len(attempts) != 1 || predecessor.Attempt != 1 {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_recovery_limit", "only one protected recovery successor is supported")
		}
		if predecessor.Status == "succeeded" {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_complete", "fleet plan already succeeded")
		}
		proof := admission.PredecessorStop
		if proof == nil || !store.ValidateFleetRunnerPredecessorStopEvidence(*proof, predecessor.ID, admission.SourceDispatchRunID) || proof.WorkflowURL != dispatch.WorkflowURL || !strings.HasSuffix(predecessor.RunnerAttemptID, ":"+admission.SourceDispatchRunID+":"+strconv.FormatInt(proof.RunAttempt, 10)) {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_external_stop_unproven", "predecessor workflow termination or reconciliation is unproven")
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	item := fleet.RunnerAttempt{SchemaVersion: fleet.RunnerAttemptSchemaVersion, ID: admission.AttemptID, PlanID: admission.PlanID, Attempt: len(attempts) + 1, RunnerAttemptID: admission.RunnerAttemptID, Status: "queued", CurrentPhase: fleetInitialPhase(plan.Operation.Payload), CommitSHA: admission.CommitSHA, PlanSHA256: admission.PlanSHA256, WorkflowURL: admission.WorkflowURL, WorkloadSHA: admission.WorkloadSHA, RootAttemptID: admission.AttemptID, HeartbeatTimeoutSeconds: admission.HeartbeatTimeoutSeconds, Revision: 1, StartedAt: now, HeartbeatAt: now, HeartbeatExpiresAt: now.Add(time.Duration(admission.HeartbeatTimeoutSeconds) * time.Second), UpdatedAt: now}
	if len(attempts) == 1 {
		predecessor := attempts[0]
		item.RootAttemptID, item.RetryOf = predecessor.RootAttemptID, predecessor.ID
		// Destructive/drain paths re-prove prechange before mutation. A proven
		// non-destructive successor resumes the first unproven predecessor phase.
		if !fleetPlanRequiresDrain(plan.Operation.Payload) {
			item.CurrentPhase = predecessor.CurrentPhase
		}
	}
	puts := make([]clientv3.Op, 0, 6)
	compares := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(s.opKey(admission.PlanID)), "=", planRevision), clientv3.Compare(clientv3.ModRevision(s.fleetRunnerDispatchKey(admission.PlanID)), "=", dispatchResponse.Kvs[0].ModRevision), clientv3.Compare(clientv3.ModRevision(s.fleetRunnerPlanStateKey(admission.PlanID)), "=", stateResponse.Kvs[0].ModRevision), clientv3.Compare(clientv3.CreateRevision(key), "=", 0), clientv3.Compare(clientv3.CreateRevision(s.opKey(acceptance.Operation.ID)), "=", 0), clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptKey(admission.PlanID, item.ID)), "=", 0)}
	if preparationResponse != nil {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.fleetGitHubDispatchPreparationKey(admission.PlanID)), "=", preparationResponse.Kvs[0].ModRevision))
	}
	// Bind the per-target mutation fence atomically with this attempt
	// (plan.md §2.2 "Bind"; WP8b), for both the first attempt and recovery.
	// M13: if the fence was acquired or last bound under an older epoch,
	// bind re-binds it (Generation+1, current epoch) instead of stranding
	// the plan.
	preparation, err := s.GetFleetGitHubDispatchPreparation(ctx, admission.PlanID)
	if err != nil {
		return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_dispatch_mismatch", "runner attempt dispatch preparation is unavailable")
	}
	cluster, _ := plan.Operation.Payload["cluster"].(string)
	fenceCtx, err := s.loadFleetTargetFenceContext(ctx, cluster, preparation.FleetEnvironment, true)
	if err != nil {
		return store.AcceptedOperation{}, wrapFleetFenceError(err)
	}
	var fencePut clientv3.Op
	hasFencePut := false
	if fenceCtx.TargetID != "" {
		nextFence, decideErr := lifecycle.DecideBind(fenceCtx.Fence, admission.PlanID, admission.DispatchNonceSHA256, fenceCtx.Epoch)
		if decideErr != nil {
			return store.AcceptedOperation{}, wrapFleetFenceError(decideErr)
		}
		fenceRecord, marshalErr := json.Marshal(fleetTargetFenceRecord(nextFence))
		if marshalErr != nil {
			return store.AcceptedOperation{}, marshalErr
		}
		compares = append(compares,
			clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(fenceCtx.TargetID)), "=", fenceCtx.FenceRevision),
			clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", fenceCtx.EpochRevision),
		)
		fencePut, hasFencePut = clientv3.OpPut(s.fleetTargetFenceKey(fenceCtx.TargetID), string(fenceRecord)), true
	}
	compares = append(compares, s.registryCompare(fenceCtx), s.abandonedCompare(admission.PlanID))
	store.BindAcceptedFleetRunnerAttempt(&acceptance, &item)
	acceptedAt := now
	identityID, intentID := uuid.NewString(), uuid.NewString()
	intent, err := store.SealOperationAcceptance(ctx, s.signer, acceptance, identityID, intentID, acceptedAt)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	accepted := store.AcceptedOperation{Operation: acceptance.Operation, RequestIdentityID: identityID, AcceptanceIntentID: intentID, Intent: intent, FleetRunnerAttempt: &item}
	acceptanceRecord, err := json.Marshal(v3Acceptance{Identity: acceptance.Identity, Accepted: accepted})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	operationRecord, err := json.Marshal(v3Record{Operation: acceptance.Operation})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	attemptRecord, err := json.Marshal(item)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	nextState, _ := json.Marshal(v3FleetRunnerPlanState{Version: state.Version + 1})
	puts = append(puts, clientv3.OpPut(key, string(acceptanceRecord)), clientv3.OpPut(s.opKey(acceptance.Operation.ID), string(operationRecord)), clientv3.OpPut(s.operationKindIndexKey(acceptance.Operation.Kind, acceptedAt, acceptance.Operation.ID), acceptance.Operation.ID), clientv3.OpPut(s.operationAcceptanceIndexKey(acceptance.Operation.ID), key), clientv3.OpPut(s.fleetRunnerAttemptKey(admission.PlanID, item.ID), string(attemptRecord)), clientv3.OpPut(s.fleetRunnerPlanStateKey(admission.PlanID), string(nextState)))
	if hasFencePut {
		puts = append(puts, fencePut)
	}
	if len(attempts) == 1 {
		predecessor := attempts[0]
		predecessor.Status, predecessor.LastError, predecessor.Revision, predecessor.UpdatedAt, predecessor.FinishedAt = "failed", "server-observed GitHub workflow "+admission.PredecessorStop.Conclusion, predecessor.Revision+1, now, &now
		encodedPredecessor, marshalErr := json.Marshal(predecessor)
		if marshalErr != nil {
			return store.AcceptedOperation{}, marshalErr
		}
		puts = append(puts, clientv3.OpPut(s.fleetRunnerAttemptKey(admission.PlanID, predecessor.ID), string(encodedPredecessor)))
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.fleetRunnerAttemptKey(admission.PlanID, predecessor.ID)), "=", attemptRevisions[predecessor.ID]))
	}
	txn, err := s.kv.Txn(ctx).If(compares...).Then(puts...).Commit()
	if err != nil {
		return store.AcceptedOperation{}, &store.AcceptanceIndeterminateError{Err: err}
	}
	if txn.Succeeded {
		return accepted, nil
	}
	if existing, err := s.loadAcceptance(ctx, key); err == nil {
		return s.replay(ctx, key, existing, acceptance.Identity, acceptance.Fingerprint)
	}
	return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_stale", "fleet runner admission changed concurrently")
}

func (s *V3OperationStore) listFleetRunnerAttempts(ctx context.Context, planID string) ([]fleet.RunnerAttempt, map[string]int64, error) {
	response, err := s.kv.Get(ctx, s.fleetRunnerAttemptPrefix(planID), clientv3.WithPrefix())
	if err != nil {
		return nil, nil, err
	}
	items, revisions := make([]fleet.RunnerAttempt, 0, len(response.Kvs)), map[string]int64{}
	for _, kv := range response.Kvs {
		var item fleet.RunnerAttempt
		if err := decodeV3Record(kv.Value, &item); err != nil {
			return nil, nil, err
		}
		if item.PlanID != planID || item.ID == "" {
			return nil, nil, fmt.Errorf("fleet runner attempt is corrupt")
		}
		item.SchemaVersion = fleet.RunnerAttemptSchemaVersion
		items = append(items, item)
		revisions[item.ID] = kv.ModRevision
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Attempt < items[j].Attempt })
	return items, revisions, nil
}

func (s *V3OperationStore) GetFleetRunnerAttempt(ctx context.Context, planID, attemptID string) (*fleet.RunnerAttempt, error) {
	response, err := s.kv.Get(ctx, s.fleetRunnerAttemptKey(planID, attemptID))
	if err != nil {
		return nil, err
	}
	if len(response.Kvs) != 1 {
		return nil, ErrNotFound
	}
	var item fleet.RunnerAttempt
	if err := decodeV3Record(response.Kvs[0].Value, &item); err != nil {
		return nil, err
	}
	if item.PlanID != planID || item.ID != attemptID {
		return nil, fmt.Errorf("fleet runner attempt identity is corrupt")
	}
	item.SchemaVersion = fleet.RunnerAttemptSchemaVersion
	projected := fleetProjectExpiredAttempt(item, time.Now().UTC())
	return &projected, nil
}

func (s *V3OperationStore) ListFleetRunnerAttempts(ctx context.Context, planID string) ([]fleet.RunnerAttempt, error) {
	items, _, err := s.listFleetRunnerAttempts(ctx, planID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range items {
		items[i] = fleetProjectExpiredAttempt(items[i], now)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Attempt > items[j].Attempt })
	return items, nil
}

// UpdateFleetRunnerAttempt provides the same revision-CAS heartbeat, phase,
// and cancel transitions as PostgreSQL. All mutable transitions fence on the
// plan state revision, so a concurrent recovery cannot be overwritten.
func (s *V3OperationStore) UpdateFleetRunnerAttempt(ctx context.Context, planID, id string, revision int64, action string, values ...interface{}) (*fleet.RunnerAttempt, error) {
	for retry := 0; retry < 8; retry++ {
		response, err := s.kv.Get(ctx, s.fleetRunnerAttemptKey(planID, id))
		if err != nil {
			return nil, err
		}
		if len(response.Kvs) != 1 {
			return nil, ErrNotFound
		}
		var item fleet.RunnerAttempt
		if err := decodeV3Record(response.Kvs[0].Value, &item); err != nil {
			return nil, err
		}
		if item.PlanID != planID || item.ID != id {
			return nil, fmt.Errorf("fleet runner attempt identity is corrupt")
		}
		stateResponse, err := s.kv.Get(ctx, s.fleetRunnerPlanStateKey(planID))
		if err != nil {
			return nil, err
		}
		if len(stateResponse.Kvs) != 1 {
			return nil, fmt.Errorf("fleet runner attempt state is unavailable")
		}
		var state v3FleetRunnerPlanState
		if err := decodeV3Record(stateResponse.Kvs[0].Value, &state); err != nil || state.Version <= 0 {
			return nil, fmt.Errorf("fleet runner attempt state is corrupt")
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		if item.Revision != revision || (item.Status != "queued" && item.Status != "running") {
			return nil, ErrNotFound
		}
		// Epoch compares (Q10; plan.md §2.2 "Evidence writes"): heartbeat and
		// advance are refused once the fence's recorded authority epoch falls
		// behind current; cancel is always allowed. strict=false: a plan
		// already admitted stays ungated if its cluster/environment no
		// longer resolve the same way (it was already fenced at bind time if
		// a fence applies to it at all).
		cluster, err := s.fleetCapacityPlanCluster(ctx, planID)
		if err != nil {
			return nil, err
		}
		var environment string
		if preparation, prepErr := s.GetFleetGitHubDispatchPreparation(ctx, planID); prepErr == nil {
			environment = preparation.FleetEnvironment
		}
		fenceCtx, err := s.loadFleetTargetFenceContext(ctx, cluster, environment, false)
		if err != nil {
			return nil, err
		}
		var releaseNextFence *lifecycle.FenceFacts
		switch action {
		case "heartbeat":
			if len(values) != 2 {
				return nil, fmt.Errorf("heartbeat requires sequence and message")
			}
			if err := lifecycle.DecideEvidenceWrite(fenceCtx.Fence, planID, fenceCtx.Epoch, lifecycle.EvidenceHeartbeat); err != nil {
				return nil, err
			}
			sequence, ok := values[0].(int64)
			if !ok || sequence != item.HeartbeatSequence+1 || !item.HeartbeatExpiresAt.After(now) {
				return nil, ErrNotFound
			}
			message, ok := values[1].(string)
			if !ok {
				return nil, fmt.Errorf("heartbeat message is invalid")
			}
			item.Status, item.HeartbeatSequence, item.LastError = "running", sequence, message
			item.HeartbeatAt, item.HeartbeatExpiresAt = now, now.Add(time.Duration(item.HeartbeatTimeoutSeconds)*time.Second)
		case "advance":
			if len(values) != 1 {
				return nil, fmt.Errorf("advance requires phase")
			}
			if err := lifecycle.DecideEvidenceWrite(fenceCtx.Fence, planID, fenceCtx.Epoch, lifecycle.EvidenceAdvance); err != nil {
				return nil, err
			}
			phase, ok := values[0].(string)
			if !ok || !fleetValidPhase(phase) || !item.HeartbeatExpiresAt.After(now) {
				return nil, ErrNotFound
			}
			proven, err := s.hasSignedFleetReconciliationEvidence(ctx, planID, item)
			if err != nil {
				return nil, err
			}
			if !proven {
				return nil, fleetReconciliationError("fleet_reconciliation_evidence_required", "runner phase advance requires signed successful reconciliation evidence for its current phase")
			}
			item.CurrentPhase = phase
			item.Status = "running"
			if phase == "complete" {
				item.Status = "succeeded"
				item.FinishedAt = &now
				// Release on complete (plan.md §2.2 "Release: succeeded"; WP8b).
				if fenceCtx.TargetID != "" {
					// Never free a fence some other plan holds: DecideRelease
					// checks only Held, not the holder's identity.
					if fenceCtx.Fence.Held && fenceCtx.Fence.HolderPlanID != planID {
						return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetExecutionOccupied, Reason: "fleet target fence is held by another plan"}
					}
					attempts, _, attErr := s.listFleetRunnerAttempts(ctx, planID)
					if attErr != nil {
						return nil, attErr
					}
					holderAttempts := make([]fleet.RunnerAttempt, 0, len(attempts))
					for _, a := range attempts {
						if a.ID == item.ID {
							a = item
						}
						holderAttempts = append(holderAttempts, a)
					}
					nextFence, relErr := lifecycle.DecideRelease(fenceCtx.Fence, lifecycle.HolderFacts{Attempts: holderAttempts}, lifecycle.ReleaseModeSucceeded, nil, now)
					if relErr != nil {
						return nil, relErr
					}
					releaseNextFence = &nextFence
				}
			}
		case "cancel":
			if len(values) != 1 {
				return nil, fmt.Errorf("cancel requires message")
			}
			message, ok := values[0].(string)
			if !ok {
				return nil, fmt.Errorf("cancel message is invalid")
			}
			item.Status, item.LastError, item.FinishedAt = "canceled", message, &now
		default:
			return nil, fmt.Errorf("unsupported runner attempt update")
		}
		item.Revision++
		item.UpdatedAt = now
		item.SchemaVersion = fleet.RunnerAttemptSchemaVersion
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		nextState, _ := json.Marshal(v3FleetRunnerPlanState{Version: state.Version + 1})
		compares := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(s.fleetRunnerAttemptKey(planID, id)), "=", response.Kvs[0].ModRevision), clientv3.Compare(clientv3.ModRevision(s.fleetRunnerPlanStateKey(planID)), "=", stateResponse.Kvs[0].ModRevision)}
		puts := []clientv3.Op{clientv3.OpPut(s.fleetRunnerAttemptKey(planID, id), string(encoded)), clientv3.OpPut(s.fleetRunnerPlanStateKey(planID), string(nextState))}
		if action == "advance" && item.Status == "succeeded" {
			pointerCompares, pointerPuts, err := s.activeFleetIngressTransition(ctx, planID, item)
			if err != nil {
				return nil, err
			}
			compares = append(compares, pointerCompares...)
			puts = append(puts, pointerPuts...)
		}
		// T1: heartbeat and advance were decided against the fence and epoch
		// read above, so both must still hold at commit (a concurrent epoch
		// advance otherwise lets a superseded heartbeat land). Cancel is
		// always allowed (Q10) and takes no fence compare.
		if fenceCtx.TargetID != "" && action != "cancel" {
			compares = append(compares,
				clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(fenceCtx.TargetID)), "=", fenceCtx.FenceRevision),
				clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", fenceCtx.EpochRevision),
			)
		}
		if releaseNextFence != nil {
			fenceRecord, marshalErr := json.Marshal(fleetTargetFenceRecord(*releaseNextFence))
			if marshalErr != nil {
				return nil, marshalErr
			}
			compares = append(compares, s.registryCompare(fenceCtx), s.abandonedCompare(planID))
			puts = append(puts, clientv3.OpPut(s.fleetTargetFenceKey(fenceCtx.TargetID), string(fenceRecord)))
		}
		txn, err := s.kv.Txn(ctx).If(compares...).Then(puts...).Commit()
		if err != nil {
			return nil, err
		}
		if txn.Succeeded {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("fleet runner attempt update exhausted contention retries")
}

// hasSignedFleetReconciliationEvidence accepts only an immutable reconciliation
// receipt whose signed operation matches this runner attempt's current phase.
// The caller fences its read with the same plan-state CAS used for the phase
// mutation, so a concurrent reconciliation append causes this update to lose.
func (s *V3OperationStore) hasSignedFleetReconciliationEvidence(ctx context.Context, planID string, attempt fleet.RunnerAttempt) (bool, error) {
	history, _, err := s.listFleetReconciliations(ctx, planID)
	if err != nil {
		return false, err
	}
	for _, operation := range history {
		if operation.Status != model.OperationSucceeded {
			continue
		}
		request, err := store.FleetReconciliationRequestFromOperation(operation)
		if err != nil {
			return false, fmt.Errorf("decode fleet reconciliation evidence: %w", err)
		}
		if request.Status != "succeeded" || request.Phase != attempt.CurrentPhase || request.AttemptID != attempt.ID || request.CommitSHA != attempt.CommitSHA || request.PlanSHA256 != attempt.PlanSHA256 {
			continue
		}
		index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(operation.ID))
		if err != nil {
			return false, err
		}
		if len(index.Kvs) != 1 || strings.TrimSpace(string(index.Kvs[0].Value)) == "" {
			return false, fmt.Errorf("fleet reconciliation evidence acceptance link is missing")
		}
		key := string(index.Kvs[0].Value)
		loaded, err := s.loadAcceptance(ctx, key)
		if err != nil {
			return false, fmt.Errorf("load fleet reconciliation acceptance: %w", err)
		}
		accepted, err := s.replay(ctx, key, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
		if err != nil {
			return false, fmt.Errorf("verify fleet reconciliation acceptance: %w", err)
		}
		if accepted.Operation.ID != operation.ID || accepted.Operation.Kind != "fleet.reconciliation" || accepted.Operation.Ref != planID {
			return false, fmt.Errorf("fleet reconciliation signed receipt differs from history")
		}
		return true, nil
	}
	return false, nil
}

func (s *V3OperationStore) verifyFleetRunnerAttemptReplay(ctx context.Context, accepted store.AcceptedOperation) (*fleet.RunnerAttempt, error) {
	if accepted.FleetRunnerAttempt == nil {
		return nil, nil
	}
	item, err := s.GetFleetRunnerAttempt(ctx, accepted.FleetRunnerAttempt.PlanID, accepted.FleetRunnerAttempt.ID)
	if err != nil {
		return nil, err
	}
	lineage, _, err := s.listFleetRunnerAttempts(ctx, item.PlanID)
	if err != nil {
		return nil, err
	}
	if err := store.VerifyFleetRunnerAttemptEvidence(&store.FleetRunnerAttemptAdmission{PlanID: accepted.FleetRunnerAttempt.PlanID, RunnerAttemptID: accepted.FleetRunnerAttempt.RunnerAttemptID, CommitSHA: accepted.FleetRunnerAttempt.CommitSHA, PlanSHA256: accepted.FleetRunnerAttempt.PlanSHA256, WorkflowURL: accepted.FleetRunnerAttempt.WorkflowURL, HeartbeatTimeoutSeconds: accepted.FleetRunnerAttempt.HeartbeatTimeoutSeconds}, accepted.Intent.CanonicalBytes, item, fleetValidateLineage(lineage) == nil); err != nil {
		return nil, err
	}
	return item, nil
}

func fleetAttemptError(code, reason string) error {
	return &store.FleetRunnerAttemptAdmissionError{Code: code, Reason: reason}
}

// wrapFleetFenceError rewraps a *lifecycle.FenceError (fence/epoch
// resolution and Decide* refusals, which carry no store-level type of their
// own) as the same store.FleetRunnerAttemptAdmissionError shape every other
// bind refusal in this file already uses, so it reaches
// handler/control_protocol.go's existing generic admission-error mapping
// instead of falling through to a 500. Any other error is returned as-is.
func wrapFleetFenceError(err error) error {
	var fenceErr *lifecycle.FenceError
	if errors.As(err, &fenceErr) {
		return fleetAttemptError(fenceErr.Code, fenceErr.Error())
	}
	return err
}
func fleetLowerHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
func fleetWorkflowURL(value string) bool {
	return strings.HasPrefix(value, "https://") && len(value) <= 2048
}
func fleetValidPhase(phase string) bool {
	return lifecycle.ValidPhase(phase)
}
func fleetPlanRequiresDrain(payload map[string]interface{}) bool {
	action, _ := payload["action"].(string)
	current, _ := payload["current"].(map[string]interface{})
	proposed, _ := payload["proposed"].(map[string]interface{})
	number := func(value interface{}) float64 {
		switch typed := value.(type) {
		case float64:
			return typed
		case json.Number:
			n, _ := typed.Float64()
			return n
		}
		return 0
	}
	return lifecycle.RequiresDrain(action, number(current["desired"]), number(proposed["desired"]))
}
func fleetInitialPhase(_ map[string]interface{}) string {
	// Reconciliation admission requires successful prechange evidence before
	// provider work for every plan shape. Starting later would make that
	// mandatory checkpoint impossible to append while the attempt is current.
	return "prechange_verified"
}
func fleetProjectExpiredAttempt(item fleet.RunnerAttempt, now time.Time) fleet.RunnerAttempt {
	return lifecycle.ProjectExpiry(item, now)
}
func fleetValidateLineage(items []fleet.RunnerAttempt) error {
	// Callers rely on items ending up sorted ascending by Attempt after this
	// call (e.g. attempts[len(attempts)-1] as the latest attempt), so the
	// in-place sort stays here; lifecycle.ValidateLineage itself is
	// order-insensitive and does not mutate its argument.
	sort.Slice(items, func(i, j int) bool { return items[i].Attempt < items[j].Attempt })
	return lifecycle.ValidateLineage(items)
}
