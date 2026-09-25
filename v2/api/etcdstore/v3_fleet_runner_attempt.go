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

// BindFleetRunnerDispatch records the protected GitHub dispatch outcome that
// a future runner must match. It never accepts a raw nonce. The binding is
// immutable: retrying identical evidence is safe, while any rewrite fails.
func (s *V3OperationStore) BindFleetRunnerDispatch(ctx context.Context, input FleetRunnerDispatchBinding) error {
	input.PlanID = strings.TrimSpace(input.PlanID)
	input.PlanSHA256 = strings.TrimSpace(input.PlanSHA256)
	input.ApprovedHeadSHA = strings.TrimSpace(input.ApprovedHeadSHA)
	input.DispatchNonceSHA256 = strings.TrimSpace(input.DispatchNonceSHA256)
	input.WorkflowURL = strings.TrimSpace(input.WorkflowURL)
	if _, err := uuid.Parse(input.PlanID); err != nil || !fleetLowerHex(input.PlanSHA256, 64) || !fleetLowerHex(input.ApprovedHeadSHA, 40) || !fleetLowerHex(input.DispatchNonceSHA256, 64) || input.RunID <= 0 || !fleetWorkflowURL(input.WorkflowURL) {
		return fmt.Errorf("fleet runner dispatch binding is invalid")
	}
	plan, planRevision, err := s.load(ctx, input.PlanID)
	if err != nil {
		return fmt.Errorf("load fleet plan: %w", err)
	}
	if plan.Operation.Kind != "fleet.capacity-plan" || plan.Operation.Status != model.OperationSucceeded {
		return fmt.Errorf("fleet runner dispatch requires a successful immutable fleet plan")
	}
	encoded, err := json.Marshal(v3FleetRunnerDispatch{FleetRunnerDispatchBinding: input, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)})
	if err != nil {
		return err
	}
	state, err := json.Marshal(v3FleetRunnerPlanState{Version: 1})
	if err != nil {
		return err
	}
	dispatchKey, stateKey := s.fleetRunnerDispatchKey(input.PlanID), s.fleetRunnerPlanStateKey(input.PlanID)
	txn, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.opKey(input.PlanID)), "=", planRevision),
		clientv3.Compare(clientv3.CreateRevision(dispatchKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(stateKey), "=", 0),
	).Then(clientv3.OpPut(dispatchKey, string(encoded)), clientv3.OpPut(stateKey, string(state))).Commit()
	if err != nil {
		return err
	}
	if txn.Succeeded {
		return nil
	}
	existing, err := s.loadFleetRunnerDispatch(ctx, input.PlanID)
	if err == nil && existing.FleetRunnerDispatchBinding == input {
		return nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return fmt.Errorf("fleet runner dispatch binding already exists or fleet plan changed")
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

func (s *V3OperationStore) acceptFleetRunnerAttempt(ctx context.Context, input store.OperationAcceptance) (store.AcceptedOperation, error) {
	if err := store.NormalizeFleetRunnerAttemptAcceptance(&input); err != nil {
		return store.AcceptedOperation{}, err
	}
	if s.policy.ReplayTTL > 0 {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "etcd runner-attempt replay expiry is not yet qualified"}
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
	for _, item := range attempts {
		if item.RunnerAttemptID == admission.RunnerAttemptID {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_identity_conflict", "runner attempt identity already accepted")
		}
		if item.CommitSHA != admission.CommitSHA || item.PlanSHA256 != admission.PlanSHA256 {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_binding_mismatch", "fleet plan attempts use different reviewed input")
		}
	}
	var predecessor *fleet.RunnerAttempt
	if len(attempts) == 0 {
		if admission.Resume || admission.WorkloadIntent != "apply" || admission.ExpectedPredecessorID != "" {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_resume_invalid", "fleet recovery requires a prior runner attempt")
		}
	} else {
		predecessor = &attempts[len(attempts)-1]
		if admission.ExpectedPredecessorID != predecessor.ID {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_stale", "selected predecessor changed")
		}
		if !admission.Resume || admission.WorkloadIntent != "recover" {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_resume_required", "successor must be a protected recovery")
		}
		if predecessor.Status == "succeeded" {
			return store.AcceptedOperation{}, fleetAttemptError("fleet_runner_attempt_complete", "fleet plan already succeeded")
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	item := fleet.RunnerAttempt{SchemaVersion: fleet.RunnerAttemptSchemaVersion, ID: admission.AttemptID, PlanID: admission.PlanID, Attempt: len(attempts) + 1, RunnerAttemptID: admission.RunnerAttemptID, Status: "queued", CurrentPhase: fleetInitialPhase(plan.Operation.Payload), CommitSHA: admission.CommitSHA, PlanSHA256: admission.PlanSHA256, WorkflowURL: admission.WorkflowURL, RootAttemptID: admission.AttemptID, HeartbeatTimeoutSeconds: admission.HeartbeatTimeoutSeconds, Revision: 1, StartedAt: now, HeartbeatAt: now, HeartbeatExpiresAt: now.Add(time.Duration(admission.HeartbeatTimeoutSeconds) * time.Second), UpdatedAt: now}
	puts := make([]clientv3.Op, 0, 6)
	compares := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(s.opKey(admission.PlanID)), "=", planRevision), clientv3.Compare(clientv3.ModRevision(s.fleetRunnerDispatchKey(admission.PlanID)), "=", dispatchResponse.Kvs[0].ModRevision), clientv3.Compare(clientv3.ModRevision(s.fleetRunnerPlanStateKey(admission.PlanID)), "=", stateResponse.Kvs[0].ModRevision), clientv3.Compare(clientv3.CreateRevision(key), "=", 0), clientv3.Compare(clientv3.CreateRevision(s.opKey(acceptance.Operation.ID)), "=", 0), clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptKey(admission.PlanID, item.ID)), "=", 0)}
	if predecessor != nil {
		item.RootAttemptID, item.RetryOf = predecessor.RootAttemptID, predecessor.ID
		if !fleetPlanRequiresDrain(plan.Operation.Payload) {
			item.CurrentPhase = predecessor.CurrentPhase
		}
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.fleetRunnerAttemptKey(admission.PlanID, predecessor.ID)), "=", attemptRevisions[predecessor.ID]))
		if predecessor.Status == "queued" || predecessor.Status == "running" {
			// This is intentionally parity with the PostgreSQL aggregate. The
			// resulting message is a release gate: the control record is fenced,
			// but external runner termination remains unproven until the Fleet
			// workflow supplies an independently qualified cancellation proof.
			copy := *predecessor
			if !copy.HeartbeatExpiresAt.After(now) {
				copy.Status, copy.LastError = "abandoned", "heartbeat lease expired; external execution termination unproven"
			} else {
				copy.Status, copy.LastError = "canceled", "etcd lease superseded by protected recovery; external execution termination unproven"
			}
			copy.Revision++
			copy.UpdatedAt = now
			copy.FinishedAt = &now
			encoded, _ := json.Marshal(copy)
			puts = append(puts, clientv3.OpPut(s.fleetRunnerAttemptKey(admission.PlanID, copy.ID), string(encoded)))
		}
	}
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
	txn, err := s.kv.Txn(ctx).If(compares...).Then(puts...).Commit()
	if err != nil {
		return store.AcceptedOperation{}, err
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
		switch action {
		case "heartbeat":
			if len(values) != 2 {
				return nil, fmt.Errorf("heartbeat requires sequence and message")
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
			phase, ok := values[0].(string)
			if !ok || !fleetValidPhase(phase) || !item.HeartbeatExpiresAt.After(now) {
				return nil, ErrNotFound
			}
			item.CurrentPhase = phase
			item.Status = "running"
			if phase == "complete" {
				item.Status = "succeeded"
				item.FinishedAt = &now
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
		txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.fleetRunnerAttemptKey(planID, id)), "=", response.Kvs[0].ModRevision), clientv3.Compare(clientv3.ModRevision(s.fleetRunnerPlanStateKey(planID)), "=", stateResponse.Kvs[0].ModRevision)).Then(clientv3.OpPut(s.fleetRunnerAttemptKey(planID, id), string(encoded)), clientv3.OpPut(s.fleetRunnerPlanStateKey(planID), string(nextState))).Commit()
		if err != nil {
			return nil, err
		}
		if txn.Succeeded {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("fleet runner attempt update exhausted contention retries")
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
	for _, allowed := range []string{"prechange_verified", "provider_applying", "infrastructure_applied", "inventory_generated", "nodes_configured", "nodes_enrolled", "readiness_verified", "old_nodes_drained", "complete"} {
		if phase == allowed {
			return true
		}
	}
	return false
}
func fleetPlanRequiresDrain(payload map[string]interface{}) bool {
	action, _ := payload["action"].(string)
	if action == "replace" {
		return true
	}
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
	return action == "scale" && number(proposed["desired"]) < number(current["desired"])
}
func fleetInitialPhase(_ map[string]interface{}) string {
	// Reconciliation admission requires successful prechange evidence before
	// provider work for every plan shape. Starting later would make that
	// mandatory checkpoint impossible to append while the attempt is current.
	return "prechange_verified"
}
func fleetProjectExpiredAttempt(item fleet.RunnerAttempt, now time.Time) fleet.RunnerAttempt {
	if (item.Status == "queued" || item.Status == "running") && item.HeartbeatExpiresAt.Before(now) {
		item.Status, item.LastError = "abandoned", "heartbeat lease expired; external execution termination unproven"
		finished := item.HeartbeatExpiresAt
		item.FinishedAt = &finished
	}
	return item
}
func fleetValidateLineage(items []fleet.RunnerAttempt) error {
	if len(items) == 0 {
		return nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Attempt < items[j].Attempt })
	root := items[0]
	if root.Attempt != 1 || root.RootAttemptID != root.ID || root.RetryOf != "" {
		return fmt.Errorf("fleet runner-attempt root lineage is corrupt")
	}
	previous := root.ID
	for index := 1; index < len(items); index++ {
		item := items[index]
		if item.Attempt != index+1 || item.RootAttemptID != root.ID || item.RetryOf != previous {
			return fmt.Errorf("fleet runner-attempt retry lineage is corrupt")
		}
		previous = item.ID
	}
	return nil
}
