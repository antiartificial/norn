package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

func normalizeFleetRunnerAttemptAcceptance(acceptance *OperationAcceptance) error {
	admission := acceptance.FleetRunnerAttempt
	if admission == nil {
		if acceptance.Operation.Kind == "fleet.runner-attempt" || acceptance.Identity.Kind == "fleet.runner-attempt" {
			return &AcceptanceValidationError{Reason: "fleet runner-attempt operations require typed transaction admission"}
		}
		return nil
	}
	if acceptance.FleetReconciliation != nil {
		return &AcceptanceValidationError{Reason: "fleet acceptance cannot combine runner-attempt and reconciliation admission"}
	}
	trimFleetRunnerAttemptAdmission(admission)
	if acceptance.Operation.Kind != "fleet.runner-attempt" || acceptance.Identity.Kind != "fleet.runner-attempt" || acceptance.Identity.Resource != admission.PlanID || acceptance.Operation.Ref != admission.PlanID {
		return &AcceptanceValidationError{Reason: "fleet runner-attempt admission must match operation plan identity"}
	}
	if acceptance.Operation.Status != model.OperationSucceeded {
		return &AcceptanceValidationError{Reason: "fleet runner-attempt acceptance operation must be a completed receipt"}
	}
	if _, err := uuid.Parse(admission.PlanID); err != nil {
		return &AcceptanceValidationError{Reason: "fleet runner-attempt plan ID must be a UUID"}
	}
	if _, err := uuid.Parse(admission.AttemptID); err != nil {
		return &AcceptanceValidationError{Reason: "fleet runner-attempt ID must be a UUID"}
	}
	if admission.ExpectedPredecessorID != "" {
		if _, err := uuid.Parse(admission.ExpectedPredecessorID); err != nil {
			return &AcceptanceValidationError{Reason: "fleet runner-attempt predecessor ID must be a UUID"}
		}
	}
	if admission.RunnerAttemptID == "" || len(admission.RunnerAttemptID) > 200 || !lowerHex(admission.CommitSHA, 40) || !lowerHex(admission.PlanSHA256, 64) || !lowerHex(admission.DispatchNonceSHA256, 64) {
		return &AcceptanceValidationError{Reason: "fleet runner-attempt binding is invalid"}
	}
	workflowURL, err := url.Parse(admission.WorkflowURL)
	if err != nil || workflowURL.Scheme != "https" || workflowURL.Host == "" || workflowURL.User != nil || len(admission.WorkflowURL) > 2048 {
		return &AcceptanceValidationError{Reason: "fleet runner-attempt workflow URL is invalid"}
	}
	if !positiveDecimal(admission.SourceDispatchRunID) || !positiveDecimal(admission.WorkloadRunID) || (admission.WorkloadIntent != "apply" && admission.WorkloadIntent != "recover") || !lowerHex(admission.WorkloadSHA, 40) {
		return &AcceptanceValidationError{Reason: "fleet runner-attempt workload evidence is invalid"}
	}
	if admission.HeartbeatTimeoutSeconds < 30 || admission.HeartbeatTimeoutSeconds > 900 {
		return &AcceptanceValidationError{Reason: "fleet runner-attempt heartbeat timeout is invalid"}
	}
	if admission.WorkloadIntent == "apply" && (admission.Resume || admission.WorkloadRunID != admission.SourceDispatchRunID || admission.WorkloadSHA != admission.CommitSHA) {
		return &AcceptanceValidationError{Reason: "initial fleet runner attempt must be bound to the dispatched apply workload"}
	}
	if admission.WorkloadIntent == "recover" && !admission.Resume {
		return &AcceptanceValidationError{Reason: "fleet recovery workload must explicitly resume a prior attempt"}
	}
	if value, _ := acceptance.Operation.Payload["attemptId"].(string); value != admission.AttemptID {
		return &AcceptanceValidationError{Reason: "fleet runner-attempt operation payload must link its attempt"}
	}
	if value, _ := acceptance.Operation.Payload["dispatchNonceSha256"].(string); value != admission.DispatchNonceSHA256 {
		return &AcceptanceValidationError{Reason: "fleet runner-attempt operation payload must bind the nonce digest"}
	}
	expectedPayload := map[string]interface{}{
		"planId": admission.PlanID, "runnerAttemptId": admission.RunnerAttemptID,
		"commitSha": admission.CommitSHA, "planSha256": admission.PlanSHA256, "workflowUrl": admission.WorkflowURL,
		"sourceDispatchRunId": admission.SourceDispatchRunID, "resume": admission.Resume,
		"heartbeatTimeoutSeconds": admission.HeartbeatTimeoutSeconds,
	}
	for key, expected := range expectedPayload {
		if !jsonEqual(acceptance.Operation.Payload[key], expected) {
			return &AcceptanceValidationError{Reason: "fleet runner-attempt operation payload differs from typed admission"}
		}
	}
	if _, exists := acceptance.Operation.Payload["dispatchNonce"]; exists || containsJSONKey(acceptance.Semantics, "dispatchNonce") {
		return &AcceptanceValidationError{Reason: "raw fleet dispatch nonce cannot enter signed or public acceptance data"}
	}
	return nil
}

// NormalizeFleetRunnerAttemptAcceptance validates the typed runner-attempt
// portion of an operation acceptance before a backend-specific atomic
// aggregate is assembled.  Backends must still enforce plan, dispatch, and
// lineage state atomically with the signed receipt.
func NormalizeFleetRunnerAttemptAcceptance(acceptance *OperationAcceptance) error {
	return normalizeFleetRunnerAttemptAcceptance(acceptance)
}

// BindAcceptedFleetRunnerAttempt attaches the server-derived attempt identity
// to the acceptance envelope before it is signed.  Callers must only use an
// attempt they have created in the same atomic admission transaction.
func BindAcceptedFleetRunnerAttempt(acceptance *OperationAcceptance, attempt *fleet.RunnerAttempt) {
	if acceptance != nil {
		acceptance.acceptedFleetRunnerAttempt = attempt
	}
}

func trimFleetRunnerAttemptAdmission(admission *FleetRunnerAttemptAdmission) {
	admission.PlanID = strings.TrimSpace(admission.PlanID)
	admission.AttemptID = strings.TrimSpace(admission.AttemptID)
	admission.ExpectedPredecessorID = strings.TrimSpace(admission.ExpectedPredecessorID)
	admission.RunnerAttemptID = strings.TrimSpace(admission.RunnerAttemptID)
	admission.CommitSHA = strings.TrimSpace(admission.CommitSHA)
	admission.PlanSHA256 = strings.TrimSpace(admission.PlanSHA256)
	admission.WorkflowURL = strings.TrimSpace(admission.WorkflowURL)
	admission.DispatchNonceSHA256 = strings.TrimSpace(admission.DispatchNonceSHA256)
	admission.SourceDispatchRunID = strings.TrimSpace(admission.SourceDispatchRunID)
	admission.WorkloadIntent = strings.TrimSpace(admission.WorkloadIntent)
	admission.WorkloadRunID = strings.TrimSpace(admission.WorkloadRunID)
	admission.WorkloadSHA = strings.TrimSpace(admission.WorkloadSHA)
}

func lowerHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func positiveDecimal(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == value
}

func containsJSONKey(value interface{}, key string) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return true
	}
	var normalized interface{}
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return true
	}
	return normalizedContainsJSONKey(normalized, key)
}

func normalizedContainsJSONKey(value interface{}, key string) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		for current, child := range typed {
			if current == key || normalizedContainsJSONKey(child, key) {
				return true
			}
		}
	case []interface{}:
		for _, child := range typed {
			if normalizedContainsJSONKey(child, key) {
				return true
			}
		}
	}
	return false
}

func acceptFleetRunnerAttempt(ctx context.Context, tx pgx.Tx, acceptance OperationAcceptance) (*fleet.RunnerAttempt, error) {
	admission := acceptance.FleetRunnerAttempt
	if _, err := tx.Exec(ctx, `/* fleet-runner-attempt-acceptance-lock */ SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+admission.PlanID); err != nil {
		return nil, err
	}
	var planKind string
	var planStatus model.OperationStatus
	var rawPlan []byte
	if err := tx.QueryRow(ctx, `SELECT kind,status,payload FROM operations WHERE id=$1 FOR SHARE`, admission.PlanID).Scan(&planKind, &planStatus, &rawPlan); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fleetRunnerAttemptAdmissionError("fleet_plan_not_found", "fleet capacity plan not found")
		}
		return nil, err
	}
	if planKind != "fleet.capacity-plan" || planStatus != model.OperationSucceeded {
		return nil, fleetRunnerAttemptAdmissionError("fleet_plan_invalid", "fleet capacity plan is not a successful immutable plan")
	}
	var planPayload map[string]interface{}
	if err := json.Unmarshal(rawPlan, &planPayload); err != nil {
		return nil, fleetRunnerAttemptAdmissionError("fleet_plan_invalid", "fleet capacity plan payload is invalid")
	}
	dispatch, err := scanFleetGitHubDispatch(tx.QueryRow(ctx, `SELECT `+fleetGitHubDispatchColumns+` FROM fleet_github_dispatches WHERE plan_id=$1 FOR UPDATE`, admission.PlanID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fleetRunnerAttemptAdmissionError("fleet_runner_attempt_dispatch_mismatch", "runner attempt has no protected dispatch binding")
	}
	if err != nil {
		return nil, err
	}
	if dispatch.PlanSHA256 != admission.PlanSHA256 || dispatch.ApprovedHeadSHA != admission.CommitSHA || dispatch.DispatchNonceSHA256 != admission.DispatchNonceSHA256 || dispatch.RunID <= 0 || admission.SourceDispatchRunID != strconv.FormatInt(dispatch.RunID, 10) {
		return nil, fleetRunnerAttemptAdmissionError("fleet_runner_attempt_dispatch_mismatch", "runner attempt does not match the protected dispatch binding")
	}
	if admission.WorkloadIntent == "apply" && (admission.WorkloadRunID != strconv.FormatInt(dispatch.RunID, 10) || admission.WorkloadSHA != dispatch.ApprovedHeadSHA) {
		return nil, fleetRunnerAttemptAdmissionError("fleet_runner_attempt_identity_mismatch", "apply workload does not match the protected dispatch run")
	}

	rows, err := tx.Query(ctx, `SELECT `+fleetRunnerAttemptColumns+` FROM fleet_runner_attempts WHERE plan_id=$1 ORDER BY attempt DESC FOR UPDATE`, admission.PlanID)
	if err != nil {
		return nil, err
	}
	var existing []fleet.RunnerAttempt
	for rows.Next() {
		item, scanErr := scanFleetRunnerAttempt(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		existing = append(existing, *item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return nil, err
	}
	for _, item := range existing {
		if item.RunnerAttemptID == admission.RunnerAttemptID {
			return nil, fleetRunnerAttemptAdmissionError("fleet_runner_attempt_identity_conflict", "runner attempt identity is already accepted under another request")
		}
		if item.CommitSHA != admission.CommitSHA || item.PlanSHA256 != admission.PlanSHA256 {
			return nil, fleetRunnerAttemptAdmissionError("fleet_runner_attempt_binding_mismatch", "fleet plan attempts are bound to different reviewed input")
		}
	}
	if err := validateFleetRunnerAttemptLineage(existing); err != nil {
		return nil, fleetRunnerAttemptAdmissionError("fleet_runner_attempt_lineage_invalid", err.Error())
	}
	if len(existing) == 0 {
		if admission.Resume || admission.WorkloadIntent != "apply" || admission.ExpectedPredecessorID != "" {
			return nil, fleetRunnerAttemptAdmissionError("fleet_runner_attempt_resume_invalid", "fleet recovery requires a prior runner attempt")
		}
	} else {
		if admission.ExpectedPredecessorID != existing[0].ID {
			return nil, fleetRunnerAttemptAdmissionError("fleet_runner_attempt_stale", "the selected predecessor runner attempt changed before recovery acceptance")
		}
		if !admission.Resume || admission.WorkloadIntent != "recover" {
			return nil, fleetRunnerAttemptAdmissionError("fleet_runner_attempt_resume_required", "a successor runner must be an explicit protected recovery")
		}
		if existing[0].Status == "succeeded" {
			return nil, fleetRunnerAttemptAdmissionError("fleet_runner_attempt_complete", "fleet plan already has a successful runner attempt")
		}
	}

	attemptNumber := 1
	if len(existing) > 0 {
		attemptNumber = existing[0].Attempt + 1
	}
	rootAttemptID := admission.AttemptID
	retryOf := ""
	phase := fleetRunnerAttemptInitialPhaseFromPlan(planPayload)
	if len(existing) > 0 {
		predecessor := existing[0]
		rootAttemptID = predecessor.RootAttemptID
		retryOf = predecessor.ID
		if !fleetAcceptancePlanRequiresDrain(&model.Operation{Payload: planPayload}) {
			phase = predecessor.CurrentPhase
		}
		if predecessor.Status == "queued" || predecessor.Status == "running" {
			status := "canceled"
			message := "database lease superseded by protected recovery; external execution termination unproven"
			if !predecessor.HeartbeatExpiresAt.After(databaseNow) {
				status = "abandoned"
				message = "heartbeat lease expired; external execution termination unproven"
			}
			result, err := tx.Exec(ctx, `UPDATE fleet_runner_attempts SET status=$4,message=$5,revision=revision+1,updated_at=$6,finished_at=$6 WHERE plan_id=$1 AND id=$2 AND revision=$3 AND status IN ('queued','running')`, admission.PlanID, predecessor.ID, predecessor.Revision, status, message, databaseNow)
			if err != nil {
				return nil, err
			}
			if result.RowsAffected() != 1 {
				return nil, fleetRunnerAttemptAdmissionError("fleet_runner_attempt_stale", "the predecessor runner attempt changed during recovery")
			}
		}
	}

	item := &fleet.RunnerAttempt{
		SchemaVersion: fleet.RunnerAttemptSchemaVersion, ID: admission.AttemptID, PlanID: admission.PlanID,
		Attempt: attemptNumber, RunnerAttemptID: admission.RunnerAttemptID, Status: "queued", CurrentPhase: phase,
		CommitSHA: admission.CommitSHA, PlanSHA256: admission.PlanSHA256, WorkflowURL: admission.WorkflowURL,
		RootAttemptID: rootAttemptID, RetryOf: retryOf, HeartbeatTimeoutSeconds: admission.HeartbeatTimeoutSeconds,
		Revision: 1, StartedAt: databaseNow, HeartbeatAt: databaseNow, HeartbeatExpiresAt: databaseNow.Add(time.Duration(admission.HeartbeatTimeoutSeconds) * time.Second), UpdatedAt: databaseNow,
	}
	if _, err := tx.Exec(ctx, `INSERT INTO fleet_runner_attempts (`+fleetRunnerAttemptColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, item.ID, item.PlanID, item.Attempt, item.RunnerAttemptID, item.CommitSHA, item.PlanSHA256, item.WorkflowURL, item.Status, item.CurrentPhase, item.RootAttemptID, item.RetryOf, item.HeartbeatSequence, item.HeartbeatTimeoutSeconds, item.Revision, item.HeartbeatAt, item.HeartbeatExpiresAt, item.LastError, item.StartedAt, item.UpdatedAt, item.FinishedAt); err != nil {
		return nil, err
	}
	return item, nil
}

func validateFleetRunnerAttemptLineage(descending []fleet.RunnerAttempt) error {
	if len(descending) == 0 {
		return nil
	}
	root := descending[len(descending)-1]
	if root.Attempt != 1 || root.RootAttemptID != root.ID || root.RetryOf != "" {
		return fmt.Errorf("fleet runner-attempt root lineage is corrupt")
	}
	previousID := root.ID
	for index := len(descending) - 2; index >= 0; index-- {
		item := descending[index]
		wantAttempt := len(descending) - index
		if item.Attempt != wantAttempt || item.RootAttemptID != root.ID || item.RetryOf != previousID {
			return fmt.Errorf("fleet runner-attempt retry lineage is corrupt")
		}
		previousID = item.ID
	}
	return nil
}

func fleetRunnerAttemptInitialPhaseFromPlan(payload map[string]interface{}) string {
	if fleetAcceptancePlanRequiresDrain(&model.Operation{Payload: payload}) {
		return "prechange_verified"
	}
	return "provider_applying"
}

func verifyImmutableFleetRunnerAttempt(expected *FleetRunnerAttemptAdmission, output *fleetRunnerAttemptEnvelope, actual *fleet.RunnerAttempt, lineageValid bool) error {
	if expected == nil {
		if output != nil || actual != nil {
			return fmt.Errorf("unexpected fleet runner-attempt link")
		}
		return nil
	}
	if output == nil || actual == nil {
		return fmt.Errorf("accepted fleet runner attempt is missing")
	}
	if output.ID != actual.ID || output.PlanID != actual.PlanID || output.Attempt != actual.Attempt || output.RunnerAttemptID != actual.RunnerAttemptID || output.CommitSHA != actual.CommitSHA || output.PlanSHA256 != actual.PlanSHA256 || output.WorkflowURL != actual.WorkflowURL || output.RootAttemptID != actual.RootAttemptID || output.RetryOf != actual.RetryOf || output.HeartbeatTimeoutSeconds != actual.HeartbeatTimeoutSeconds {
		return fmt.Errorf("signed fleet runner-attempt output differs from durable lineage")
	}
	if actual.PlanID != expected.PlanID || actual.RunnerAttemptID != expected.RunnerAttemptID || actual.CommitSHA != expected.CommitSHA || actual.PlanSHA256 != expected.PlanSHA256 || actual.WorkflowURL != expected.WorkflowURL || actual.HeartbeatTimeoutSeconds != expected.HeartbeatTimeoutSeconds {
		return fmt.Errorf("immutable fleet runner-attempt fields differ from accepted request")
	}
	if actual.Attempt < 1 || actual.RootAttemptID == "" || (actual.Attempt == 1 && (actual.RootAttemptID != actual.ID || actual.RetryOf != "")) || (actual.Attempt > 1 && actual.RetryOf == "") {
		return fmt.Errorf("fleet runner-attempt lineage is invalid")
	}
	if !lineageValid {
		return fmt.Errorf("fleet runner-attempt lineage references are missing or inconsistent")
	}
	return nil
}

// VerifyFleetRunnerAttemptEvidence verifies that the signed acceptance
// envelope still names exactly the immutable fields persisted for its runner
// attempt.  It is used by non-PostgreSQL backends while replaying a signed
// acceptance, after they have independently validated the full lineage.
func VerifyFleetRunnerAttemptEvidence(expected *FleetRunnerAttemptAdmission, canonical []byte, actual *fleet.RunnerAttempt, lineageValid bool) error {
	if expected == nil {
		return verifyImmutableFleetRunnerAttempt(nil, nil, actual, lineageValid)
	}
	var envelope acceptanceEnvelope
	if err := json.Unmarshal(canonical, &envelope); err != nil {
		return fmt.Errorf("decode signed fleet runner-attempt envelope: %w", err)
	}
	if envelope.FleetRunnerAttempt == nil || actual == nil {
		return fmt.Errorf("fleet runner-attempt replay link missing: signed=%t durable=%t", envelope.FleetRunnerAttempt != nil, actual != nil)
	}
	return verifyImmutableFleetRunnerAttempt(expected, envelope.FleetRunnerAttempt, actual, lineageValid)
}

func fleetRunnerAttemptAdmissionError(code, reason string) error {
	return &FleetRunnerAttemptAdmissionError{Code: code, Reason: reason}
}
