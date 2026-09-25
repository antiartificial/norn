package etcdstore

// Fleet reconciliation evidence is an append-only aggregate. The plan-state
// revision is the per-plan fence shared with runner-attempt creation, recovery,
// heartbeat, phase, and cancellation updates. It makes a snapshot of the
// attempt, complete reconciliation history, and signed receipt one CAS write.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func (s *V3OperationStore) fleetReconciliationKey(planID, operationID string) string {
	return s.prefix + "/v3/fleet-reconciliations/" + planID + "/" + operationID
}

func (s *V3OperationStore) fleetReconciliationPrefix(planID string) string {
	return s.prefix + "/v3/fleet-reconciliations/" + planID + "/"
}

func (s *V3OperationStore) acceptFleetReconciliation(ctx context.Context, input store.OperationAcceptance) (store.AcceptedOperation, error) {
	if err := store.NormalizeFleetReconciliationAcceptance(&input); err != nil {
		return store.AcceptedOperation{}, err
	}
	if s.policy.ReplayTTL > 0 {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "etcd fleet reconciliation replay expiry is not yet qualified"}
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

	admission := acceptance.FleetReconciliation
	plan, planRevision, err := s.load(ctx, admission.PlanID)
	if err != nil {
		return store.AcceptedOperation{}, fleetReconciliationError("fleet_plan_not_found", "fleet capacity plan not found")
	}
	if plan.Operation.Kind != "fleet.capacity-plan" || plan.Operation.Status != model.OperationSucceeded {
		return store.AcceptedOperation{}, fleetReconciliationError("fleet_plan_invalid", "fleet capacity plan is not a successful immutable plan")
	}
	request, err := store.FleetReconciliationRequestFromOperation(acceptance.Operation)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	stateResponse, err := s.kv.Get(ctx, s.fleetRunnerPlanStateKey(admission.PlanID))
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if len(stateResponse.Kvs) != 1 {
		return store.AcceptedOperation{}, fleetReconciliationError("fleet_reconciliation_attempt_not_found", "fleet runner attempt state is unavailable")
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
		return store.AcceptedOperation{}, fleetReconciliationError("fleet_reconciliation_attempt_not_found", err.Error())
	}
	if err := validateFleetReconciliationAttempt(admission, request, attempts); err != nil {
		return store.AcceptedOperation{}, err
	}
	history, historyRevisions, err := s.listFleetReconciliations(ctx, admission.PlanID)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if err := store.ValidateFleetReconciliationAdmissionTransition(&plan.Operation, history, request); err != nil {
		return store.AcceptedOperation{}, fleetReconciliationError("fleet_reconciliation_out_of_order", err.Error())
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	identityID, intentID := uuid.NewString(), uuid.NewString()
	intent, err := store.SealOperationAcceptance(ctx, s.signer, acceptance, identityID, intentID, now)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	accepted := store.AcceptedOperation{Operation: acceptance.Operation, RequestIdentityID: identityID, AcceptanceIntentID: intentID, Intent: intent}
	acceptanceRecord, err := json.Marshal(v3Acceptance{Identity: acceptance.Identity, Accepted: accepted})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	operationRecord, err := json.Marshal(v3Record{Operation: acceptance.Operation})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	nextState, err := json.Marshal(v3FleetRunnerPlanState{Version: state.Version + 1})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	reconciliationKey := s.fleetReconciliationKey(admission.PlanID, acceptance.Operation.ID)
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.opKey(admission.PlanID)), "=", planRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetRunnerPlanStateKey(admission.PlanID)), "=", stateResponse.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.opKey(acceptance.Operation.ID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(reconciliationKey), "=", 0),
	}
	for attemptID, revision := range attemptRevisions {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.fleetRunnerAttemptKey(admission.PlanID, attemptID)), "=", revision))
	}
	for operationID, revision := range historyRevisions {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.fleetReconciliationKey(admission.PlanID, operationID)), "=", revision))
	}
	puts := []clientv3.Op{
		clientv3.OpPut(key, string(acceptanceRecord)),
		clientv3.OpPut(s.opKey(acceptance.Operation.ID), string(operationRecord)),
		clientv3.OpPut(s.operationKindIndexKey(acceptance.Operation.Kind, now, acceptance.Operation.ID), acceptance.Operation.ID),
		clientv3.OpPut(s.operationAcceptanceIndexKey(acceptance.Operation.ID), key),
		clientv3.OpPut(reconciliationKey, string(operationRecord)),
		clientv3.OpPut(s.fleetRunnerPlanStateKey(admission.PlanID), string(nextState)),
	}
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
	return store.AcceptedOperation{}, fleetReconciliationError("fleet_reconciliation_stale", "fleet reconciliation admission changed concurrently")
}

func validateFleetReconciliationAttempt(admission *store.FleetReconciliationAdmission, request fleet.ReconciliationRequest, attempts []fleet.RunnerAttempt) error {
	if admission.AttemptID == "" {
		if admission.RequireActiveAttempt {
			return fleetReconciliationError("fleet_reconciliation_attempt_required", "fleet workload evidence must name its runner attempt")
		}
		return nil
	}
	var attempt *fleet.RunnerAttempt
	for index := range attempts {
		if attempts[index].ID == admission.AttemptID {
			attempt = &attempts[index]
			break
		}
	}
	if attempt == nil {
		return fleetReconciliationError("fleet_reconciliation_attempt_not_found", "reconciliation evidence must name a runner attempt for this plan")
	}
	if attempt.CommitSHA != request.CommitSHA || attempt.PlanSHA256 != request.PlanSHA256 {
		return fleetReconciliationError("fleet_reconciliation_attempt_binding_mismatch", "reconciliation evidence does not match its runner attempt binding")
	}
	if !admission.RequireActiveAttempt {
		return nil
	}
	now := time.Now().UTC()
	if (attempt.Status != "queued" && attempt.Status != "running") || !attempt.HeartbeatExpiresAt.After(now) {
		return fleetReconciliationError("fleet_reconciliation_attempt_not_current", "runner attempt is not active with a live heartbeat lease")
	}
	if attempt.RunnerAttemptID != admission.RunnerAttemptID || attempt.WorkflowURL != admission.WorkflowURL {
		return fleetReconciliationError("fleet_reconciliation_identity_mismatch", "fleet workload token is not bound to this runner attempt")
	}
	if request.Phase != attempt.CurrentPhase {
		return fleetReconciliationError("fleet_reconciliation_attempt_not_current", fmt.Sprintf("reconciliation phase %s does not match current runner phase %s", request.Phase, attempt.CurrentPhase))
	}
	return nil
}

func (s *V3OperationStore) listFleetReconciliations(ctx context.Context, planID string) ([]model.Operation, map[string]int64, error) {
	response, err := s.kv.Get(ctx, s.fleetReconciliationPrefix(planID), clientv3.WithPrefix())
	if err != nil {
		return nil, nil, err
	}
	operations := make([]model.Operation, 0, len(response.Kvs))
	revisions := make(map[string]int64, len(response.Kvs))
	for _, item := range response.Kvs {
		var record v3Record
		if err := decodeV3Record(item.Value, &record); err != nil {
			return nil, nil, err
		}
		if record.Operation.ID == "" || record.Operation.Kind != "fleet.reconciliation" || record.Operation.Ref != planID {
			return nil, nil, fmt.Errorf("fleet reconciliation history is corrupt")
		}
		operations = append(operations, record.Operation)
		revisions[record.Operation.ID] = item.ModRevision
	}
	return operations, revisions, nil
}

// ListFleetReconciliations returns at most 100 append-only reconciliation
// receipts for one plan. It is intentionally a narrow read surface for Fleet
// status; callers needing admission must use the complete private history.
func (s *V3OperationStore) ListFleetReconciliations(ctx context.Context, planID string) ([]model.Operation, error) {
	planID = strings.TrimSpace(planID)
	if _, err := uuid.Parse(planID); err != nil {
		return nil, fmt.Errorf("fleet reconciliation plan ID must be a UUID")
	}
	response, err := s.kv.Get(ctx, s.fleetReconciliationPrefix(planID), clientv3.WithPrefix(), clientv3.WithLimit(101), clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend))
	if err != nil {
		return nil, err
	}
	if len(response.Kvs) > 100 || response.More {
		return nil, fmt.Errorf("fleet reconciliation history exceeds read limit")
	}
	operations := make([]model.Operation, 0, len(response.Kvs))
	for _, item := range response.Kvs {
		var record v3Record
		if err := decodeV3Record(item.Value, &record); err != nil {
			return nil, err
		}
		if record.Operation.ID == "" || record.Operation.Kind != "fleet.reconciliation" || record.Operation.Ref != planID {
			return nil, fmt.Errorf("fleet reconciliation history is corrupt")
		}
		operations = append(operations, record.Operation)
	}
	sort.SliceStable(operations, func(i, j int) bool {
		if operations[i].StartedAt.Equal(operations[j].StartedAt) {
			return operations[i].ID < operations[j].ID
		}
		return operations[i].StartedAt.Before(operations[j].StartedAt)
	})
	return operations, nil
}

func fleetReconciliationError(code, reason string) error {
	return &store.FleetReconciliationAdmissionError{Code: code, Reason: strings.TrimSpace(reason)}
}
