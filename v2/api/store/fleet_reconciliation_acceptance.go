package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

func normalizeFleetReconciliationAcceptance(acceptance *OperationAcceptance) error {
	admission := acceptance.FleetReconciliation
	if admission == nil {
		if acceptance.Operation.Kind == "fleet.reconciliation" || acceptance.Identity.Kind == "fleet.reconciliation" {
			return &AcceptanceValidationError{Reason: "fleet reconciliation operations require typed transaction admission"}
		}
		return nil
	}
	admission.PlanID = strings.TrimSpace(admission.PlanID)
	admission.AttemptID = strings.TrimSpace(admission.AttemptID)
	admission.RunnerAttemptID = strings.TrimSpace(admission.RunnerAttemptID)
	admission.WorkflowURL = strings.TrimSpace(admission.WorkflowURL)
	if acceptance.Operation.Kind != "fleet.reconciliation" || acceptance.Identity.Kind != "fleet.reconciliation" || admission.PlanID == "" || acceptance.Identity.Resource != admission.PlanID || acceptance.Operation.Ref != admission.PlanID {
		return &AcceptanceValidationError{Reason: "fleet reconciliation admission must match operation plan identity"}
	}
	if _, err := uuid.Parse(admission.PlanID); err != nil {
		return &AcceptanceValidationError{Reason: "fleet reconciliation plan ID must be a UUID"}
	}
	payloadAttempt, _ := acceptance.Operation.Payload["attemptId"].(string)
	if strings.TrimSpace(payloadAttempt) != admission.AttemptID {
		return &AcceptanceValidationError{Reason: "fleet reconciliation attempt admission must match operation payload"}
	}
	if admission.AttemptID != "" {
		if _, err := uuid.Parse(admission.AttemptID); err != nil {
			return &AcceptanceValidationError{Reason: "fleet reconciliation attempt ID must be a UUID"}
		}
	}
	if admission.RequireActiveAttempt && (admission.AttemptID == "" || admission.RunnerAttemptID == "" || admission.WorkflowURL == "") {
		return &AcceptanceValidationError{Reason: "active fleet reconciliation admission requires verified attempt ownership"}
	}
	if len(admission.RunnerAttemptID) > 1024 || len(admission.WorkflowURL) > 2048 {
		return &AcceptanceValidationError{Reason: "fleet reconciliation attempt ownership exceeds configured bounds"}
	}
	request, err := FleetReconciliationRequestFromOperation(acceptance.Operation)
	if err != nil {
		return err
	}
	validPhase := false
	for _, phase := range []string{"prechange_verified", "provider_applying", "infrastructure_applied", "inventory_generated", "nodes_configured", "nodes_enrolled", "readiness_verified", "old_nodes_drained", "complete"} {
		validPhase = validPhase || request.Phase == phase
	}
	if !validPhase || (request.Status != "succeeded" && request.Status != "failed") {
		return &AcceptanceValidationError{Reason: "fleet reconciliation phase or status is invalid"}
	}
	wantStatus := model.OperationSucceeded
	if request.Status == "failed" {
		wantStatus = model.OperationFailed
	}
	if acceptance.Operation.Status != wantStatus {
		return &AcceptanceValidationError{Reason: "fleet reconciliation operation status must match evidence status"}
	}
	return nil
}

// NormalizeFleetReconciliationAcceptance validates the typed reconciliation
// portion before a backend-specific aggregate appends its signed evidence.
// Backends must still bind the plan, runner attempt, and complete history in
// the same atomic transaction as the receipt.
func NormalizeFleetReconciliationAcceptance(acceptance *OperationAcceptance) error {
	return normalizeFleetReconciliationAcceptance(acceptance)
}

func enforceFleetReconciliationAdmission(ctx context.Context, tx pgx.Tx, acceptance OperationAcceptance) error {
	admission := acceptance.FleetReconciliation
	if admission == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `/* fleet-reconciliation-admission-lock */ SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+admission.PlanID); err != nil {
		return err
	}
	var planKind string
	var planStatus model.OperationStatus
	var rawPlan []byte
	if err := tx.QueryRow(ctx, `SELECT kind,status,payload FROM operations WHERE id=$1 FOR SHARE`, admission.PlanID).Scan(&planKind, &planStatus, &rawPlan); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fleetReconciliationAdmissionError("fleet_plan_not_found", "fleet capacity plan not found")
		}
		return err
	}
	if planKind != "fleet.capacity-plan" || planStatus != model.OperationSucceeded {
		return fleetReconciliationAdmissionError("fleet_plan_invalid", "fleet capacity plan is not a successful immutable plan")
	}
	var planPayload map[string]interface{}
	if err := json.Unmarshal(rawPlan, &planPayload); err != nil {
		return fleetReconciliationAdmissionError("fleet_plan_invalid", "fleet capacity plan payload is invalid")
	}
	request, err := FleetReconciliationRequestFromOperation(acceptance.Operation)
	if err != nil {
		return err
	}
	if admission.AttemptID != "" {
		attempt, err := scanFleetRunnerAttempt(tx.QueryRow(ctx, `SELECT `+fleetRunnerAttemptColumns+` FROM fleet_runner_attempts WHERE id=$1 AND plan_id=$2 FOR UPDATE`, admission.AttemptID, admission.PlanID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fleetReconciliationAdmissionError("fleet_reconciliation_attempt_not_found", "reconciliation evidence must name a runner attempt for this plan")
		}
		if err != nil {
			return err
		}
		if attempt.CommitSHA != request.CommitSHA || attempt.PlanSHA256 != request.PlanSHA256 {
			return fleetReconciliationAdmissionError("fleet_reconciliation_attempt_binding_mismatch", "reconciliation evidence does not match its runner attempt binding")
		}
		if admission.RequireActiveAttempt {
			var databaseNow time.Time
			if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
				return err
			}
			if attempt.Status != "queued" && attempt.Status != "running" || !attempt.HeartbeatExpiresAt.After(databaseNow) {
				return fleetReconciliationAdmissionError("fleet_reconciliation_attempt_not_current", "runner attempt is not active with a live heartbeat lease")
			}
			if attempt.RunnerAttemptID != admission.RunnerAttemptID || attempt.WorkflowURL != admission.WorkflowURL {
				return fleetReconciliationAdmissionError("fleet_reconciliation_identity_mismatch", "fleet workload token is not bound to this runner attempt")
			}
			if request.Phase != attempt.CurrentPhase {
				return fleetReconciliationAdmissionError("fleet_reconciliation_attempt_not_current", fmt.Sprintf("reconciliation phase %s does not match current runner phase %s", request.Phase, attempt.CurrentPhase))
			}
		}
	} else if admission.RequireActiveAttempt {
		return fleetReconciliationAdmissionError("fleet_reconciliation_attempt_required", "fleet workload evidence must name its runner attempt")
	}

	existing, err := loadFleetReconciliationHistory(ctx, tx, admission.PlanID)
	if err != nil {
		return err
	}
	plan := &model.Operation{ID: admission.PlanID, Kind: planKind, Status: planStatus, Payload: planPayload}
	if err := ValidateFleetReconciliationAdmissionTransition(plan, existing, request); err != nil {
		return fleetReconciliationAdmissionError("fleet_reconciliation_out_of_order", err.Error())
	}
	return nil
}

// FleetReconciliationRequestFromOperation decodes the canonical checkpoint
// payload shared by the PostgreSQL and etcd reconciliation aggregates.
func FleetReconciliationRequestFromOperation(operation model.Operation) (fleet.ReconciliationRequest, error) {
	encoded, err := json.Marshal(operation.Payload)
	if err != nil {
		return fleet.ReconciliationRequest{}, &AcceptanceValidationError{Reason: "encode fleet reconciliation payload: " + err.Error()}
	}
	var request fleet.ReconciliationRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		return fleet.ReconciliationRequest{}, &AcceptanceValidationError{Reason: "decode fleet reconciliation payload: " + err.Error()}
	}
	if request.SchemaVersion != fleet.ReconciliationSchemaVersion || request.Phase == "" || request.Status == "" || request.CommitSHA == "" || request.PlanSHA256 == "" || request.EvidenceDigest == "" {
		return fleet.ReconciliationRequest{}, &AcceptanceValidationError{Reason: "fleet reconciliation payload is incomplete"}
	}
	return request, nil
}

func loadFleetReconciliationHistory(ctx context.Context, tx pgx.Tx, planID string) ([]model.Operation, error) {
	rows, err := tx.Query(ctx, `SELECT status,payload FROM operations WHERE kind='fleet.reconciliation' AND ref=$1 ORDER BY started_at,id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var operations []model.Operation
	for rows.Next() {
		var operation model.Operation
		var payload []byte
		if err := rows.Scan(&operation.Status, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &operation.Payload); err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

// ValidateFleetReconciliationAdmissionTransition enforces the append-only
// checkpoint lineage. The caller supplies the complete durable history.
func ValidateFleetReconciliationAdmissionTransition(plan *model.Operation, existing []model.Operation, request fleet.ReconciliationRequest) error {
	succeeded := map[string]bool{}
	for _, operation := range existing {
		commit, _ := operation.Payload["commitSha"].(string)
		planSHA, _ := operation.Payload["planSha256"].(string)
		if commit != request.CommitSHA || planSHA != request.PlanSHA256 {
			return fmt.Errorf("checkpoint binding differs from the existing reconciliation")
		}
		if operation.Status == model.OperationSucceeded {
			phase, _ := operation.Payload["phase"].(string)
			succeeded[phase] = true
		}
	}
	if request.Status == "failed" || succeeded[request.Phase] {
		return nil
	}
	predecessor := map[string]string{
		"provider_applying": "prechange_verified", "infrastructure_applied": "provider_applying",
		"inventory_generated": "infrastructure_applied", "nodes_configured": "inventory_generated",
		"nodes_enrolled": "nodes_configured", "readiness_verified": "nodes_enrolled", "old_nodes_drained": "readiness_verified",
	}
	if request.Phase == "complete" {
		predecessor[request.Phase] = "readiness_verified"
		if fleetAcceptancePlanRequiresDrain(plan) {
			predecessor[request.Phase] = "old_nodes_drained"
		}
	}
	if required := predecessor[request.Phase]; required != "" && !succeeded[required] {
		if request.Phase == "infrastructure_applied" && request.AttemptID == "" && len(existing) == 0 {
			return nil
		}
		return fmt.Errorf("phase %s requires successful %s evidence", request.Phase, required)
	}
	return nil
}

func fleetAcceptancePlanRequiresDrain(plan *model.Operation) bool {
	action, _ := plan.Payload["action"].(string)
	if action == "replace" {
		return true
	}
	current, _ := plan.Payload["current"].(map[string]interface{})
	proposed, _ := plan.Payload["proposed"].(map[string]interface{})
	return action == "scale" && fleetAcceptanceNumber(proposed["desired"]) < fleetAcceptanceNumber(current["desired"])
}

func fleetAcceptanceNumber(value interface{}) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float64:
		return typed
	default:
		return 0
	}
}

func fleetReconciliationAdmissionError(code, reason string) error {
	return &FleetReconciliationAdmissionError{Code: code, Reason: reason}
}
