package store

// Executor-lane attempt admission (plan-5-7 E2, ADR 0010). On the executor
// lane the PG legacy path has no dispatch row, nonce or claim: attempt start
// acquires the fence, binds it and inserts the attempt in ONE transaction.
//
// Lock order (M7, store/fleet_targets.go), every step inside that one tx:
//
//  1. plan advisory locks: "norn:fleet-attempt:<plan>" (shared with dispatch
//     acquire, plan-keyed abandon and checkpoint admission), then
//     "norn.fleet-runner:<plan>" (shared with the legacy attempt create and
//     recovery). No other path takes both, so this nesting cannot invert;
//  2. fleet_target_registry FOR SHARE (ResolveFleetTargetForPlan);
//  3. fleet_authority_epoch FOR SHARE;
//  4. fleet_target_fences FOR UPDATE (DecideAcquire, then DecideBind);
//  5. fleet_github_dispatches: never touched on this lane;
//  6. fleet_runner_attempts: read, optionally cancel the recovery source,
//     insert.
//
// T6: a replay of an accepted runnerAttemptId returns right after the plan
// locks and before any fence, approval or evidence check. Approve, stop
// attestation and provider evidence are existing-ledger rows (operations),
// read inside this transaction in the pinned E0 shapes; nothing is written to
// them here. A resume approve is consumed by the recovery attempt that
// follows it: that attempt is the durable consumption record.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

// Executor-lane refusals that have no code in executor.Code*. They are
// reported as *executor.Error with these codes and map to 409.
const (
	CodeFleetExecutorRetryRequired   = "fleet_runner_retry_required"
	CodeFleetExecutorBindingConflict = "fleet_runner_attempt_binding_conflict"
	CodeFleetExecutorResumeConflict  = "fleet_runner_resume_conflict"
)

// Ledger operation kinds the admission reads (E0 pinned shapes).
const (
	// FleetExecutorAttesterMetadataKey is the operation-metadata key holding the
	// attesting device's executor.PrincipalSubject. The server writes it
	// (never the body), like every other server-gathered fact.
	FleetExecutorAttesterMetadataKey = "principalSubject"

	// KindFleetExecutorProviderEvidence is the ONLY ledger kind whose
	// providerEvidence a recovery accepts (E2 review, hand-off to E5). It is a
	// non-releasing, admin-signed (admin scope, non-CI, Idempotency-Key)
	// assertion: fleet.target.fence-release and fleet.target.abandon-plan free
	// the fence (and abandon the plan), so a recovery could never bind after
	// them. Payload FleetExecutorProviderEvidencePayload, Ref = plan ID; the
	// server stores executor.ProviderEvidence.Digest under
	// executor.ProviderEvidenceDigestMetadataKey after checking the admin
	// scope, that SourceAttemptID is the holder plan's latest attempt, that
	// DropletID equals that attempt's metadata, that its device is revoked,
	// the H5 age gate and ProviderEvidence.Validate. Any other row carrying a
	// providerEvidence key or the digest metadata key is ignored.
	KindFleetExecutorProviderEvidence = "fleet.executor.provider-evidence"
)

// FleetExecutorProviderEvidencePayload is the ledger payload of
// KindFleetExecutorProviderEvidence. The evidence vouches for exactly one
// source attempt, so it can never authorize a recovery of any other attempt.
type FleetExecutorProviderEvidencePayload struct {
	PlanID           string                    `json:"planId"`
	SourceAttemptID  string                    `json:"sourceAttemptId"`
	ProviderEvidence executor.ProviderEvidence `json:"providerEvidence"`
}

// FleetExecutorStart is one executor-lane start or recovery. Request must have
// passed executor.AttemptStartRequest.Validate and carry a normalized
// HeartbeatTimeoutSeconds (> 0). PrincipalSubject is
// executor.PrincipalSubject(deviceID) of the calling executor principal.
type FleetExecutorStart struct {
	PlanID           string
	Request          executor.AttemptStartRequest
	PrincipalSubject string
	Now              time.Time
}

// FleetExecutorStartResult is the admitted attempt. Replayed is true when the
// same runnerAttemptId was already admitted.
type FleetExecutorStartResult struct {
	Attempt  *model.FleetRunnerAttempt
	Replayed bool
}

func executorRefusal(code, format string, args ...interface{}) error {
	return &executor.Error{Code: code, Reason: fmt.Sprintf(format, args...)}
}

const fleetExecutorAttemptColumns = `id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
	commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
	heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
	heartbeat_at, updated_at, finished_at, last_error, metadata`

type fleetLedgerRow struct {
	ID        string
	Payload   map[string]interface{}
	Metadata  map[string]interface{}
	StartedAt time.Time
}

// fleetLedgerRowsTx reads planID's succeeded ledger operations of kind, oldest
// first.
func fleetLedgerRowsTx(ctx context.Context, tx pgx.Tx, kind, planID string) ([]fleetLedgerRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, payload, metadata, started_at FROM operations
		WHERE kind=$1 AND ref=$2 AND status='succeeded' ORDER BY started_at ASC, id ASC
	`, kind, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fleetLedgerRow
	for rows.Next() {
		var row fleetLedgerRow
		var payload, metadata []byte
		if err := rows.Scan(&row.ID, &payload, &metadata, &row.StartedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(payload, &row.Payload)
		_ = json.Unmarshal(metadata, &row.Metadata)
		out = append(out, row)
	}
	return out, rows.Err()
}

func decodeLedgerValue(value interface{}, target interface{}) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return json.Unmarshal(encoded, target) == nil
}

func fleetExecutorAttemptsTx(ctx context.Context, tx pgx.Tx, planID string) ([]model.FleetRunnerAttempt, error) {
	rows, err := tx.Query(ctx, `SELECT `+fleetExecutorAttemptColumns+` FROM fleet_runner_attempts WHERE plan_id=$1 ORDER BY attempt ASC`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.FleetRunnerAttempt
	for rows.Next() {
		attempt, err := scanFleetRunnerAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *attempt)
	}
	return out, rows.Err()
}

// fleetExecutorMetadata is the stored attempt metadata: the pinned executor
// identity flat at the top level, plus the timing classification the GitHub
// lane also records (same shape, so the shared timing readers work).
func fleetExecutorMetadata(request executor.AttemptStartRequest, consumedApprove string) map[string]interface{} {
	metadata := map[string]interface{}{}
	encoded, _ := json.Marshal(request.Executor)
	_ = json.Unmarshal(encoded, &metadata)
	metadata["timingClassification"] = map[string]interface{}{"operationClass": request.OperationClass, "createdNodeCount": request.CreatedNodeCount}
	if consumedApprove != "" {
		metadata["consumedApproveOperationId"] = consumedApprove
		metadata["recoveryReason"] = "executor recovery after stop proof and resume approval"
	}
	return metadata
}

// FleetExecutorAttemptMetadata decodes the executor identity stored on attempt.
func FleetExecutorAttemptMetadata(attempt *model.FleetRunnerAttempt) (executor.AttemptMetadata, bool) {
	var metadata executor.AttemptMetadata
	if attempt == nil || attempt.Metadata == nil || !decodeLedgerValue(attempt.Metadata, &metadata) {
		return executor.AttemptMetadata{}, false
	}
	return metadata, metadata.Lane == executor.LaneMetadataValue
}

func fleetExecutorReplayMatches(attempt *model.FleetRunnerAttempt, in FleetExecutorStart) bool {
	request := in.Request
	metadata, ok := FleetExecutorAttemptMetadata(attempt)
	timing, _ := attempt.Metadata["timingClassification"].(map[string]interface{})
	createdNodes, _ := timing["createdNodeCount"].(float64)
	operationClass, _ := timing["operationClass"].(string)
	return ok && metadata == request.Executor &&
		attempt.RunnerAttemptID == request.RunnerAttemptID && attempt.CommitSHA == request.CommitSHA &&
		attempt.PlanSHA256 == request.PlanSHA256 && attempt.WorkflowURL == request.WorkflowURL &&
		attempt.Recovery == request.Resume && attempt.PrincipalSubject == in.PrincipalSubject &&
		attempt.HeartbeatTimeoutSeconds == request.HeartbeatTimeoutSeconds &&
		operationClass == request.OperationClass && int(createdNodes) == request.CreatedNodeCount
}

// fleetExecutorPlanTx reads the plan's cluster, started-at and converge
// payload. converge is nil unless the plan is a valid converge plan.
func fleetExecutorPlanTx(ctx context.Context, tx pgx.Tx, planID string) (cluster string, startedAt time.Time, converge *executor.ConvergePlan, digestHex string, err error) {
	var payload []byte
	err = tx.QueryRow(ctx, `SELECT payload, started_at FROM operations WHERE id=$1 AND kind='fleet.capacity-plan'`, planID).Scan(&payload, &startedAt)
	if err != nil {
		return "", time.Time{}, nil, "", err
	}
	converge, digest, cluster := parseConvergePlanPayload(payload)
	if len(digest) == len("sha256:")+64 && digest[:7] == "sha256:" {
		digestHex = digest[7:]
	}
	return cluster, startedAt, converge, digestHex, nil
}

// parseConvergePlanPayload returns the converge object, the plan digest and
// the cluster of a fleet.capacity-plan payload. converge is nil for any plan
// that is not a valid converge plan.
func parseConvergePlanPayload(payload []byte) (*executor.ConvergePlan, string, string) {
	var plan struct {
		Cluster  string                 `json:"cluster"`
		Action   string                 `json:"action"`
		Digest   string                 `json:"digest"`
		Converge *executor.ConvergePlan `json:"converge"`
	}
	if json.Unmarshal(payload, &plan) != nil {
		return nil, "", ""
	}
	if plan.Action != executor.ActionConverge || plan.Converge == nil || plan.Converge.Validate() != nil {
		return nil, plan.Digest, plan.Cluster
	}
	return plan.Converge, plan.Digest, plan.Cluster
}

// fleetConvergePlanTx returns planID's converge payload, or nil when the plan
// does not exist or is not a converge plan. It is a plain read.
func fleetConvergePlanTx(ctx context.Context, tx pgx.Tx, planID string) (*executor.ConvergePlan, error) {
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT payload FROM operations WHERE id=$1 AND kind='fleet.capacity-plan'`, planID).Scan(&payload)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	converge, _, _ := parseConvergePlanPayload(payload)
	return converge, nil
}

// fleetNewestConvergePlanIDTx returns the ID of resource's newest recorded
// converge plan ("" when none). The PG recorder (handler/fleet_converge.go)
// records each converge plan with Ref = resource; the order is the
// recorder's own NewestConvergePlan order. It reads the ledger only, never
// fleet_resources (T7).
func fleetNewestConvergePlanIDTx(ctx context.Context, tx pgx.Tx, resource string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id FROM operations
		WHERE kind='fleet.capacity-plan' AND ref=$1 AND status='succeeded'
		  AND payload->>'action'=$2 AND payload->'converge'->>'resource'=$1
		ORDER BY started_at DESC, id DESC LIMIT 1
	`, resource, executor.ActionConverge).Scan(&id)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return id, err
}

// CreateFleetExecutorAttempt admits the first attempt of an executor-lane
// converge plan, or one recovery attempt when in.Request.Resume is set. See
// the file comment for the lock order. Refusals are *lifecycle.FenceError
// (fence, epoch, registry, abandon) or *executor.Error (everything else).
func (db *DB) CreateFleetExecutorAttempt(ctx context.Context, in FleetExecutorStart) (*FleetExecutorStartResult, error) {
	if in.PlanID == "" || in.PrincipalSubject == "" || in.Request.HeartbeatTimeoutSeconds <= 0 {
		return nil, fmt.Errorf("plan, principal subject and a normalized heartbeat timeout are required")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. plan advisory locks.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+in.PlanID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('norn.fleet-runner:' || $1))`, in.PlanID); err != nil {
		return nil, err
	}
	attempts, err := fleetExecutorAttemptsTx(ctx, tx, in.PlanID)
	if err != nil {
		return nil, err
	}
	// T6: replay first.
	for index := range attempts {
		if attempts[index].RunnerAttemptID != in.Request.RunnerAttemptID {
			continue
		}
		if !fleetExecutorReplayMatches(&attempts[index], in) {
			return nil, executorRefusal(CodeFleetExecutorBindingConflict, "runnerAttemptId is already bound to different reviewed input")
		}
		attempts[index].SetDerivedFields()
		return &FleetExecutorStartResult{Attempt: &attempts[index], Replayed: true}, nil
	}

	cluster, planStartedAt, converge, digestHex, err := fleetExecutorPlanTx(ctx, tx, in.PlanID)
	if err != nil {
		return nil, err
	}
	if converge == nil {
		return nil, executorRefusal(executor.CodeFleetPlanNotConverge, "the plan is not a converge plan")
	}
	if in.Request.CommitSHA != converge.CommitSHA || in.Request.PlanSHA256 != digestHex {
		return nil, executorRefusal(executor.CodeFleetConvergeCommitMismatch, "commitSha and planSha256 must equal the converge plan's")
	}
	abandoned, err := IsFleetPlanAbandoned(ctx, tx, in.PlanID)
	if err != nil {
		return nil, err
	}
	if abandoned {
		return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetHolderAbandoned}
	}

	// 2-4. registry, epoch, fence FOR UPDATE. The executor lane requires a
	// registered target, so an empty registry is as refused as an unknown cluster.
	targetID, registryEmpty, err := ResolveFleetTargetForPlan(ctx, tx, cluster, "")
	if err != nil {
		return nil, err
	}
	if registryEmpty || targetID == "" {
		return nil, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetUnregistered}
	}
	epoch, err := FleetAuthorityEpoch(ctx, tx)
	if err != nil {
		return nil, err
	}
	fence, err := GetFleetTargetFence(ctx, tx, targetID, true)
	if err != nil {
		return nil, err
	}
	nonce := executor.HolderNonceSHA256(in.PlanID)

	attempt := &model.FleetRunnerAttempt{
		ID: uuid.NewString(), PlanID: in.PlanID, RunnerAttemptID: in.Request.RunnerAttemptID,
		Status:    model.FleetRunnerAttemptRunning,
		CommitSHA: in.Request.CommitSHA, PlanSHA256: in.Request.PlanSHA256, WorkflowURL: in.Request.WorkflowURL,
		SourceDispatchRunID: 0, PilotRunID: "", Recovery: in.Request.Resume,
		PrincipalSubject: in.PrincipalSubject, HeartbeatTimeoutSeconds: in.Request.HeartbeatTimeoutSeconds,
		Revision: 1, StartedAt: now, PhaseStartedAt: now, HeartbeatAt: now, UpdatedAt: now,
	}
	var nextFence lifecycle.FenceFacts
	if !in.Request.Resume {
		if len(attempts) > 0 {
			return nil, executorRefusal(CodeFleetExecutorRetryRequired, "this plan already has runner history; recover the latest attempt")
		}
		// D5 "never older": a first start must be the resource's newest
		// recorded converge plan. Recovery is exempt: its plan already holds
		// the fence, and a newer plan waits for it.
		newest, err := fleetNewestConvergePlanIDTx(ctx, tx, converge.Resource)
		if err != nil {
			return nil, err
		}
		if newest != in.PlanID {
			return nil, executorRefusal(executor.CodeFleetExecutorPlanNotNewest, "a newer converge plan has been recorded for resource %s", converge.Resource)
		}
		if converge.RequiresApproval {
			if err := requireFleetConvergeApprovalTx(ctx, tx, in.PlanID, converge.CommitSHA); err != nil {
				return nil, err
			}
		}
		acquired, err := lifecycle.DecideAcquire(fence, in.PlanID, nonce, planStartedAt, epoch)
		if err != nil {
			return nil, err
		}
		nextFence, err = lifecycle.DecideBind(acquired, in.PlanID, nonce, epoch)
		if err != nil {
			return nil, err
		}
		attempt.CurrentPhase = lifecycle.Phases(lifecycle.PostgresLegacy, false)[0]
		attempt.Metadata = fleetExecutorMetadata(in.Request, "")
	} else {
		if len(attempts) == 0 {
			return nil, executorRefusal(CodeFleetExecutorRetryRequired, "a durable source runner attempt is required before recovery")
		}
		previous := &attempts[len(attempts)-1]
		consumed, err := authorizeFleetExecutorRecoveryTx(ctx, tx, in, converge, previous, attempts, now)
		if err != nil {
			return nil, err
		}
		nextFence, err = lifecycle.DecideBind(fence, in.PlanID, nonce, epoch)
		if err != nil {
			return nil, err
		}
		attempt.CurrentPhase = previous.CurrentPhase
		attempt.RetryOf = previous.ID
		attempt.Metadata = fleetExecutorMetadata(in.Request, consumed)
		if previous.Status == model.FleetRunnerAttemptQueued || previous.Status == model.FleetRunnerAttemptRunning {
			result, err := tx.Exec(ctx, `UPDATE fleet_runner_attempts
				SET status='canceled', last_error='superseded by verified executor recovery', finished_at=now(), updated_at=now(), revision=revision+1
				WHERE id=$1 AND revision=$2 AND status IN ('queued','running')`, previous.ID, previous.Revision)
			if err != nil {
				return nil, err
			}
			if result.RowsAffected() != 1 {
				return nil, executorRefusal(CodeFleetExecutorResumeConflict, "the durable source runner attempt changed before recovery")
			}
		}
	}
	if err := PutFleetTargetFence(ctx, tx, nextFence); err != nil {
		return nil, err
	}

	// 6. attempt insert.
	attempt.Attempt = len(attempts) + 1
	if len(attempts) == 0 {
		attempt.RootAttemptID = attempt.ID
	} else {
		attempt.RootAttemptID = attempts[0].RootAttemptID
	}
	metadata, _ := json.Marshal(attempt.Metadata)
	if _, err := tx.Exec(ctx, `
		INSERT INTO fleet_runner_attempts (
			id, plan_id, attempt, root_attempt_id, source_dispatch_run_id, pilot_run_id, recovery, runner_attempt_id, status, current_phase,
			commit_sha, plan_sha256, workflow_url, principal_subject, retry_of,
			heartbeat_sequence, heartbeat_timeout_seconds, revision, started_at, phase_started_at,
			heartbeat_at, updated_at, finished_at, last_error, metadata, heartbeat_expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$21::timestamptz + ($17::int * interval '1 second'))
	`, attempt.ID, attempt.PlanID, attempt.Attempt, attempt.RootAttemptID, attempt.SourceDispatchRunID, attempt.PilotRunID, attempt.Recovery, attempt.RunnerAttemptID, attempt.Status, attempt.CurrentPhase,
		attempt.CommitSHA, attempt.PlanSHA256, attempt.WorkflowURL, attempt.PrincipalSubject, attempt.RetryOf,
		attempt.HeartbeatSequence, attempt.HeartbeatTimeoutSeconds, attempt.Revision, attempt.StartedAt, attempt.PhaseStartedAt,
		attempt.HeartbeatAt, attempt.UpdatedAt, attempt.FinishedAt, attempt.LastError, metadata); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	attempt.SetDerivedFields()
	return &FleetExecutorStartResult{Attempt: attempt}, nil
}

// requireFleetConvergeApprovalTx admits a requiresApproval plan only with a
// non-resume approve for exactly this plan and commit.
func requireFleetConvergeApprovalTx(ctx context.Context, tx pgx.Tx, planID, commitSHA string) error {
	rows, err := fleetLedgerRowsTx(ctx, tx, executor.KindConvergeApprove, planID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var approve executor.ApproveAdmission
		if decodeLedgerValue(row.Payload, &approve) && approve.PlanID == planID && approve.CommitSHA == commitSHA && !approve.Resume {
			return nil
		}
	}
	return executorRefusal(executor.CodeFleetConvergeApprovalRequired, "the plan requires a matching approve")
}

// authorizeFleetExecutorRecoveryTx decides one recovery: a source that may be
// recovered, stop proof (a valid attestation or admin provider evidence) and
// an unconsumed resume approve. It returns the consumed approve's ID.
func authorizeFleetExecutorRecoveryTx(ctx context.Context, tx pgx.Tx, in FleetExecutorStart, converge *executor.ConvergePlan, previous *model.FleetRunnerAttempt, attempts []model.FleetRunnerAttempt, now time.Time) (string, error) {
	sourceMetadata, ok := FleetExecutorAttemptMetadata(previous)
	if !ok {
		return "", executorRefusal(CodeFleetExecutorBindingConflict, "the source attempt is not an executor-lane attempt")
	}
	if previous.Status == model.FleetRunnerAttemptSucceeded {
		return "", executorRefusal(CodeFleetExecutorResumeConflict, "the source attempt already succeeded")
	}
	if previous.CommitSHA != in.Request.CommitSHA || previous.PlanSHA256 != in.Request.PlanSHA256 {
		return "", executorRefusal(CodeFleetExecutorBindingConflict, "recovery does not match the durable source runner attempt")
	}
	// The approve is checked first so a second recovery against one approve is
	// reported as consumed whatever proof it carries.
	approve, err := consumeFleetResumeApproveTx(ctx, tx, in.PlanID, converge.CommitSHA, previous, attempts)
	if err != nil {
		return "", err
	}
	if err := requireFleetExecutorStopProofTx(ctx, tx, in, previous, sourceMetadata, now); err != nil {
		return "", err
	}
	return approve, nil
}

func requireFleetExecutorStopProofTx(ctx context.Context, tx pgx.Tx, in FleetExecutorStart, previous *model.FleetRunnerAttempt, source executor.AttemptMetadata, now time.Time) error {
	attestations, err := fleetLedgerRowsTx(ctx, tx, executor.KindStopAttestation, in.PlanID)
	if err != nil {
		return err
	}
	var firstRefusal error
	for index := len(attestations) - 1; index >= 0; index-- {
		var payload executor.StopAttestationPayload
		if !decodeLedgerValue(attestations[index].Payload, &payload) || payload.PlanID != in.PlanID || payload.SourceAttemptID != previous.ID {
			continue
		}
		attester, _ := attestations[index].Metadata[FleetExecutorAttesterMetadataKey].(string)
		err := executor.ValidStopAttestation(payload.StopAttestationRequest, source, previous.PrincipalSubject, attester, now)
		if err == nil {
			// The attestation vouches for this host and this invocation: the
			// recovery must start from the attesting loop itself.
			request := in.Request.Executor
			if in.PrincipalSubject != previous.PrincipalSubject || request.DropletID != payload.DropletID ||
				request.MachineID != payload.MachineID || request.InvocationID != payload.InvocationID {
				err = executorRefusal(executor.CodeFleetExecutorStopAttestationInvalid, "the recovery must start from the attesting droplet, machine and invocation")
			}
		}
		if err == nil {
			return nil
		}
		if firstRefusal == nil {
			firstRefusal = err
		}
	}
	if evidence, err := fleetExecutorProviderEvidenceTx(ctx, tx, in.PlanID, previous.ID, source, now); err != nil {
		return err
	} else if evidence {
		return nil
	}
	if firstRefusal != nil {
		return executorRefusal(executor.CodeFleetExecutorStopAttestationInvalid, "%s", firstRefusal.Error())
	}
	return &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetRecoveryRequiresStoppedSource,
		Reason: "executor recovery needs a valid stop attestation or admin provider evidence"}
}

// fleetExecutorProviderEvidenceTx reports whether the ledger holds an admin
// KindFleetExecutorProviderEvidence assertion for planID that names exactly
// the source attempt and its droplet, is still fresh
// (executor.ProviderEvidence.Validate) and whose server-stored digest equals
// the digest of the signed evidence. Only that kind counts: a providerEvidence
// key or digest metadata on any other operation is never evidence. The
// evidence is an assertion, not proof (ADR 0010); E5 owns recording it.
func fleetExecutorProviderEvidenceTx(ctx context.Context, tx pgx.Tx, planID, sourceAttemptID string, source executor.AttemptMetadata, now time.Time) (bool, error) {
	rows, err := fleetLedgerRowsTx(ctx, tx, KindFleetExecutorProviderEvidence, planID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		var payload FleetExecutorProviderEvidencePayload
		digest, _ := row.Metadata[executor.ProviderEvidenceDigestMetadataKey].(string)
		if !decodeLedgerValue(row.Payload, &payload) || payload.PlanID != planID || payload.SourceAttemptID != sourceAttemptID {
			continue
		}
		evidence := payload.ProviderEvidence
		if evidence.Validate(now) == nil && evidence.DropletID == source.DropletID && digest != "" && evidence.Digest() == digest {
			return true, nil
		}
	}
	return false, nil
}

// consumeFleetResumeApproveTx finds the resume approve naming previous and
// not yet consumed. An approve is consumed when its source attempt already has
// a recovery child, so a second recovery against one approve is refused with
// fleet_converge_approval_consumed.
func consumeFleetResumeApproveTx(ctx context.Context, tx pgx.Tx, planID, commitSHA string, previous *model.FleetRunnerAttempt, attempts []model.FleetRunnerAttempt) (string, error) {
	rows, err := fleetLedgerRowsTx(ctx, tx, executor.KindConvergeApprove, planID)
	if err != nil {
		return "", err
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
		if !decodeLedgerValue(row.Payload, &approve) || approve.PlanID != planID || approve.CommitSHA != commitSHA || !approve.Resume {
			continue
		}
		if hasChild[approve.SourceAttemptID] {
			consumed = true
			continue
		}
		if approve.SourceAttemptID == previous.ID {
			return row.ID, nil
		}
	}
	if consumed {
		return "", executorRefusal(executor.CodeFleetConvergeApprovalConsumed, "the resume approve was already consumed by a recovery attempt")
	}
	return "", executorRefusal(executor.CodeFleetConvergeApprovalRequired, "recovery requires a resume approve for the latest attempt")
}
