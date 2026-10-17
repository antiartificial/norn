// Package executor pins the wire and ledger shapes of the controller-run
// executor lane (ADR 0010, docs/v3/fleet-controller/plan-5-7.md, work package
// E0). It holds data definitions and pure validators only: no storage, no
// handlers, no I/O. Later work packages (E1-E6, P1-P3, C1) import or mirror
// these definitions so that parallel work agrees on one shape.
//
// Import rule: this package imports only the standard library and uuid. It
// must never import norn/v2/api/fleet or fleet/lifecycle, so that E4a may hang
// ConvergePlan off fleet.CapacityPlan without an import cycle.
//
// Lane ownership (ADR 0010, "Validators and lanes"): the lane is chosen by the
// principal, never by the request shape. The GitHub-lane validators
// (validateFleetRunnerStart, validOptionalFleetWorkflowURL,
// validateFleetRunnerAttemptAcceptance, fleetRunnerPrincipalOwnsAttempt's CI
// branch, lifecycle.ValidStopEvidence, DecideRelease's GitHub proof) stay
// byte-identical and are never called for an executor principal. The
// validators in this package are never called for a CI principal. The two
// identifier spaces are disjoint, so each lane's validator rejects the
// other's identifiers.
//
// Every shape is additive: new fields on existing structures are omitempty and
// existing request fingerprints stay byte-identical. The signed-operation
// pattern is the one fleet.target.* already uses (store/fleet_target_mutation.go):
// the request body is intent only, the server fills identity, and any evidence
// it gathers itself is recorded in operation metadata, outside the fingerprint.
package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Identity constants (D1, D8).
const (
	// Scope is the only scope an executor principal carries. It is granted
	// alone and only to platform Platform.
	Scope    = "fleet:execute"
	Platform = "fleet-executor"
	// TokenLifetime is the executor device token lifetime (D8).
	TokenLifetime = 7 * 24 * time.Hour
	// RotateWhenRemaining is when the loop self-rotates, idle only.
	RotateWhenRemaining = 84 * time.Hour
	// GrantTTL is a credential grant's lifetime.
	GrantTTL = 2 * time.Hour
	// LaneMetadataValue is attempt metadata "lane" for this lane.
	LaneMetadataValue = "executor"
	// ActionConverge is CapacityPlan.Action for a converge plan.
	ActionConverge = "converge"
	// AttemptSchemaVersion equals model.FleetRunnerAttemptSchemaVersion
	// (checked by the external test).
	AttemptSchemaVersion = "norn.fleet-runner-attempt/v1"
	// AttestationMaxAge bounds observedAt, as lifecycle.ValidStopEvidence does.
	AttestationMaxAge = 5 * time.Minute
	// ProviderEvidenceMaxAge bounds providerEvidence.observedAt.
	ProviderEvidenceMaxAge = time.Hour
)

// Ledger operation kinds added by this lane. Each is a signed operation in
// the existing ledger (no second ledger, no migration). Executor attempts
// themselves keep kind fleet.runner-attempt with payload lane "executor".
const (
	KindConvergeApprove       = "fleet.converge.approve"
	KindCredentialGrant       = "fleet.credential-grant"
	KindPreflightComplete     = "fleet.preflight-complete"
	KindStopAttestation       = "fleet.executor.stop-attestation"
	KindResourceExecutionMode = "fleet.resource.execution-mode"
	// KindConvergeRefusal records an executor refusal of a plan
	// (ConvergeRefusalRequest), Ref = plan ID.
	KindConvergeRefusal = "fleet.converge.refusal"
)

// Execution modes (stored only in fleet_resources.document, never read inside
// execution transactions, T7).
const (
	ModeGitHub     = "github"
	ModeController = "controller"
)

// Path classes of a converge plan (default-deny, plan-5-7 section 2.5).
// PathClassInert is a classifier result only (a docs-only range): no converge
// plan is recorded for it, so ConvergePlan.Validate rejects it.
const (
	PathClassConfig     = "config"
	PathClassExecutable = "executable"
	PathClassInert      = "inert"
)

// Error codes added by this lane, named as in fleet/lifecycle/fence.go: the
// constant is the CamelCase of the exact string. Existing codes
// (fleet_target_execution_occupied, fleet_plan_revalidation_required,
// fleet_target_unregistered, fleet_target_recovery_requires_stopped_source,
// fleet_target_holder_abandoned, insufficient_scope, ...) are reused unchanged
// and not redeclared here.
const (
	CodeInvalidFleetExecutorRequest         = "invalid_fleet_executor_request"
	CodeFleetConvergeApprovalRequired       = "fleet_converge_approval_required"
	CodeFleetConvergeCommitMismatch         = "fleet_converge_commit_mismatch"
	CodeFleetConvergeApprovalConsumed       = "fleet_converge_approval_consumed"
	CodeFleetPlanNotConverge                = "fleet_plan_not_converge"
	CodeFleetExecutionModeNotController     = "fleet_execution_mode_not_controller"
	CodeFleetExecutionModeFenceHeld         = "fleet_execution_mode_fence_held"
	CodeFleetExecutorTriggerUnverified      = "fleet_executor_trigger_unverified"
	CodeFleetExecutorPlanNotNewest          = "fleet_executor_plan_not_newest"
	CodeFleetExecutorStopAttestationInvalid = "fleet_executor_stop_attestation_invalid"
	CodeFleetCredentialGrantAttemptRequired = "fleet_credential_grant_attempt_required"
	CodeFleetCredentialGrantPhaseMismatch   = "fleet_credential_grant_phase_mismatch"
	CodeFleetCredentialGrantReplayClosed    = "fleet_credential_grant_replay_closed"
	CodeFleetPreflightRequired              = "fleet_preflight_required"
	CodeFleetCredentialTypeNotAllowed       = "fleet_credential_type_not_allowed"
	// repo-read (ADR 0010): 503 when the GitHub App cannot mint the token;
	// 409 when a replayed grant's token has expired or is no longer held.
	CodeFleetRepoTokenUnavailable     = "fleet_repo_token_unavailable"
	CodeFleetRepoTokenReplayExpired   = "fleet_repo_token_replay_expired"
	CodeFleetProviderEvidenceInvalid  = "fleet_provider_evidence_invalid"
	CodeFleetProviderEvidenceRequired = "fleet_provider_evidence_required"
	CodeFleetExecutorDeviceActive     = "fleet_executor_device_active"
	// The two 503s of the resource routes (E4b): the store cannot set the
	// mode atomically with its ledger operation (the route fails closed), and
	// the executor-lane facts of the resource view could not be read.
	CodeFleetExecutionModeUnavailable = "fleet_execution_mode_unavailable"
	CodeFleetConvergeFactsFailed      = "fleet_converge_facts_failed"
	// CodeFleetConvergeRefusalRejected: the refusal does not describe the plan
	// (planSha256 differs) or, for state_lock_foreign, its source attempt is
	// not the plan's latest, not succeeded, executor-lane attempt.
	CodeFleetConvergeRefusalRejected = "fleet_converge_refusal_rejected"
)

// CodeStatus is the HTTP status of each code above. The contract test checks
// every key against the OpenAPI ErrorCode enum.
var CodeStatus = map[string]int{
	CodeInvalidFleetExecutorRequest:         400,
	CodeFleetConvergeApprovalRequired:       409,
	CodeFleetConvergeCommitMismatch:         409,
	CodeFleetConvergeApprovalConsumed:       409,
	CodeFleetPlanNotConverge:                409,
	CodeFleetExecutionModeNotController:     409,
	CodeFleetExecutionModeFenceHeld:         409,
	CodeFleetExecutorTriggerUnverified:      409,
	CodeFleetExecutorPlanNotNewest:          409,
	CodeFleetExecutorStopAttestationInvalid: 409,
	CodeFleetCredentialGrantAttemptRequired: 409,
	CodeFleetCredentialGrantPhaseMismatch:   409,
	CodeFleetCredentialGrantReplayClosed:    409,
	CodeFleetPreflightRequired:              409,
	CodeFleetCredentialTypeNotAllowed:       403,
	CodeFleetRepoTokenUnavailable:           503,
	CodeFleetRepoTokenReplayExpired:         409,
	CodeFleetProviderEvidenceInvalid:        400,
	CodeFleetProviderEvidenceRequired:       409,
	CodeFleetExecutorDeviceActive:           409,
	CodeFleetExecutionModeUnavailable:       503,
	CodeFleetConvergeFactsFailed:            503,
	CodeFleetConvergeRefusalRejected:        409,
}

// Resource-view values this lane adds (E4b): status.nextAction and the
// blocker reasons of FleetResourceBlocker, derived at read time
// (fleet/controller.DeriveConverge), never stored. The contract test pins
// NextActionApproveConverge in the OpenAPI nextAction enum and every reason
// in BlockerReasons in the blocker reason description.
const (
	NextActionApproveConverge = "approve_converge"

	BlockerReasonExecutionModeMismatch = "execution_mode_mismatch"
	BlockerReasonDestructive           = "destructive_requires_github_lane"
	BlockerReasonHistoryRewritten      = "history_rewritten"
	BlockerReasonTriggerUnverified     = "trigger_unverified"
	BlockerReasonTriggerPending        = "trigger_pending"
	BlockerReasonApprovalRequired      = "approval_required"
	BlockerReasonStateLockForeign      = "state_lock_foreign"
)

// BlockerReasons lists the executor-lane blocker reasons in derivation
// priority order (a held fence outranks all of them).
var BlockerReasons = []string{
	BlockerReasonExecutionModeMismatch, BlockerReasonDestructive, BlockerReasonStateLockForeign, BlockerReasonHistoryRewritten,
	BlockerReasonTriggerUnverified, BlockerReasonTriggerPending, BlockerReasonApprovalRequired,
}

// Error reports a refusal by its stable code, like lifecycle.FenceError.
type Error struct {
	Code   string
	Reason string
}

func (e *Error) Error() string {
	if e.Reason == "" {
		return e.Code
	}
	return e.Reason
}

func refuse(code, format string, args ...interface{}) error {
	return &Error{Code: code, Reason: fmt.Sprintf(format, args...)}
}

func invalid(format string, args ...interface{}) error {
	return refuse(CodeInvalidFleetExecutorRequest, format, args...)
}

// ConvergePlan is the converge payload nested in the existing
// fleet.capacity-plan operation payload under the key "converge", with
// CapacityPlan.Action == "converge". It is one additive omitempty pointer, so
// the canonical bytes, digest and signature of every existing plan are
// unchanged (pinned by TestConvergeExtensionKeepsCapacityPlanDigest). The
// server signs the plan; the recorder is the shared E4a helper.
type ConvergePlan struct {
	Resource         string `json:"resource"`
	Repository       string `json:"repository"`
	CommitSHA        string `json:"commitSha"`
	BaseCommitSHA    string `json:"baseCommitSha,omitempty"` // last applied; empty on first converge
	PathClass        string `json:"pathClass"`
	RequiresApproval bool   `json:"requiresApproval"`
	MergeActor       string `json:"mergeActor,omitempty"`
	PullRequest      int    `json:"pullRequest,omitempty"`
	Generation       int64  `json:"generation"` // desired generation this plan records
}

// ApproveRequest is the body of POST /api/v1/fleet/plans/{planID}/converge/approve.
// The plan ID comes from the path. Ledger kind KindConvergeApprove, Ref = plan
// ID, requires platform:operate, a non-CI caller and an Idempotency-Key. With
// Resume it authorizes exactly one recovery attempt of SourceAttemptID.
type ApproveRequest struct {
	CommitSHA       string `json:"commitSha"`
	Resume          bool   `json:"resume,omitempty"`
	SourceAttemptID string `json:"sourceAttemptId,omitempty"`
}

// ApproveAdmission is the signed ledger payload of an approve: the request plus
// the path plan ID. Seeded directly as a ledger operation by E2 fixtures.
type ApproveAdmission struct {
	PlanID          string `json:"planId"`
	CommitSHA       string `json:"commitSha"`
	Resume          bool   `json:"resume,omitempty"`
	SourceAttemptID string `json:"sourceAttemptId,omitempty"`
}

// ExecutionModeRequest is the body of
// POST /api/v1/fleet/resources/{name}/execution-mode (admin, non-CI,
// Idempotency-Key; the fence must be Free). Ledger kind
// KindResourceExecutionMode, Ref = resource name.
type ExecutionModeRequest struct {
	Mode             string `json:"mode"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

// AttemptStartRequest is the executor-lane body of the existing
// POST /api/v1/fleet/plans/{planID}/attempts route. The handler decodes it
// (DisallowUnknownFields) only after it has identified an executor principal;
// a CI principal's body is still decoded into fleet.RunnerAttemptStartRequest
// by the unchanged GitHub branch. GitHub-lane fields (dispatchNonce,
// sourceDispatchRunId, pilotRunId, approvalEnvelopeSHA256,
// consumptionReceiptSHA256) are unknown here and therefore refused. The
// server stores sourceDispatchRunId = 0, pilotRunId = "", PrincipalSubject
// from the principal, and Executor as attempt metadata.
type AttemptStartRequest struct {
	SchemaVersion           string          `json:"schemaVersion"`
	RunnerAttemptID         string          `json:"runnerAttemptId"`
	CommitSHA               string          `json:"commitSha"`
	PlanSHA256              string          `json:"planSha256"`
	WorkflowURL             string          `json:"workflowUrl"`
	Resume                  bool            `json:"resume,omitempty"`
	HeartbeatTimeoutSeconds int             `json:"heartbeatTimeoutSeconds,omitempty"`
	OperationClass          string          `json:"operationClass,omitempty"`
	CreatedNodeCount        int             `json:"createdNodeCount,omitempty"`
	Executor                AttemptMetadata `json:"executor"`
}

// AttemptMetadata is stored in fleet_runner_attempts.metadata (PG) and the
// equivalent etcd attempt field. DropletID, MachineID, BootID and
// InvocationID are the host identity of the loop that started the attempt.
type AttemptMetadata struct {
	Lane             string `json:"lane"`
	DropletID        string `json:"dropletId"`
	MachineID        string `json:"machineId"`
	BootID           string `json:"bootId"`
	InvocationID     string `json:"invocationId"`
	LoopRelease      string `json:"loopRelease"`
	CommitSHA        string `json:"commitSha"`
	WheelhouseSHA256 string `json:"wheelhouseSha256"`
	TofuVersion      string `json:"tofuVersion"`
}

// StopAttestationRequest is the body of
// POST /api/v1/fleet/executor/stop-attestations (executor principal,
// Idempotency-Key). Ledger kind KindStopAttestation. DropletID, MachineID,
// BootID and InvocationID are the attesting loop's own current identity,
// read the same way as AttemptMetadata; the source attempt's identity comes
// from its stored metadata, never from this body. Every boolean is a positive
// fact, so a missing field decodes to false and fails closed. The server
// resolves the plan from SourceAttemptID and records PlanID in the ledger
// payload; Ref = plan ID. ValidStopAttestation is the decision.
//
// SourceCgroupEmpty: systemd reuses one cgroup for every invocation of the
// executor unit, so the source invocation's cgroup is the attester's own and
// can never read "populated 0". The fact is therefore: the unit's cgroup
// subtree (cgroup.procs of the unit cgroup and of every descendant) holds no
// process other than the attesting loop, read before that loop has started
// any child. It is sound because the unit uses KillMode=control-group and no
// Delegate=, and phases have no capabilities (R1): a phase can neither leave
// the unit cgroup nor create a sub-cgroup. Phases never run in a transient
// unit or scope (systemd-run), which would move them out of the unit and out
// of KillMode. A process outside the cgroup (cron, at) still has the
// norn-phase uid and is caught by PhaseUIDEmpty.
type StopAttestationRequest struct {
	SourceAttemptID         string    `json:"sourceAttemptId"`
	DropletID               string    `json:"dropletId"`
	MachineID               string    `json:"machineId"`
	BootID                  string    `json:"bootId"`
	InvocationID            string    `json:"invocationId"`
	PhaseUIDEmpty           bool      `json:"phaseUidEmpty"`           // no process owned by norn-phase
	PhaseProcessGroupReaped bool      `json:"phaseProcessGroupReaped"` // the source phase process group is gone
	SourceCgroupEmpty       bool      `json:"sourceCgroupEmpty"`       // see below
	ObservedAt              time.Time `json:"observedAt"`
	TofuLock                *TofuLock `json:"tofuLock,omitempty"`
}

// TofuLock is the OpenTofu state lock the loop saw, recorded before any
// force-unlock (plan-5-7 section 2.7, M5).
type TofuLock struct {
	ID        string    `json:"id"`
	Operation string    `json:"operation"`
	Who       string    `json:"who"`
	Created   time.Time `json:"created"`
}

// ConvergeView is the executor's work view of a converge plan, served as the
// additive omitempty object "converge" on FleetResourceDesired (E4b, both
// backends). It is present only when the desired revision's plan is a
// converge plan, so an absent object means "nothing to converge" and fails
// closed. The executor reads it with GET /api/v1/fleet/resources/{name} and
// needs no plan read.
type ConvergeView struct {
	// PlanSHA256 is the hex part of the plan's digest ("sha256:<hex>"), the
	// value AttemptStartRequest.PlanSHA256 must carry.
	PlanSHA256 string `json:"planSha256"`
	// RequiresApproval is ConvergePlan.RequiresApproval.
	RequiresApproval bool `json:"requiresApproval"`
	// Approved is true when a non-resume fleet.converge.approve for this plan
	// and its commitSha exists. The executor starts a requiresApproval plan
	// only when Approved is true; admission still enforces it
	// (CodeFleetConvergeApprovalRequired).
	Approved bool `json:"approved"`
	// Refused is true when the executor recorded a destructive refusal
	// (fleet.converge.refusal) for this plan: blocker
	// BlockerReasonDestructive, and the loop waits instead of replanning.
	Refused bool `json:"refused,omitempty"`
}

// JSON names of the structured resume fields in status.active (omitted when
// empty): the stopped attempt a resume approve must name, and the holder
// converge plan's commit.
const (
	ActiveResumeNeededAttemptIDField = "resumeNeededAttemptId"
	ActiveCommitSHAField             = "commitSha"
)

// ExecutorRoutes are the only routes the executor principal (exact
// fleet:execute) may call; every other route refuses it with
// insufficient_scope (E6 wires the scope matcher). The resource read is the
// single-name GET only, never the list, and returns the ordinary resource
// view (ConvergeView and status.active.resumeApproved included). No other
// read is granted: the executor needs no plan read.
var ExecutorRoutes = []string{
	"GET /api/v1/fleet/resources/{name}",
	"POST /api/v1/fleet/resources/{name}/converge",
	"POST /api/v1/fleet/plans/{planID}/attempts",
	"GET /api/v1/fleet/plans/{planID}/attempts/{attemptID}",
	"POST /api/v1/fleet/plans/{planID}/attempts/{attemptID}/heartbeat",
	"POST /api/v1/fleet/plans/{planID}/attempts/{attemptID}/advance",
	"POST /api/v1/fleet/plans/{planID}/attempts/{attemptID}/cancel",
	"GET /api/v1/fleet/plans/{planID}/reconciliations",
	"POST /api/v1/fleet/plans/{planID}/reconciliations",
	"POST /api/v1/fleet/plans/{planID}/credential-grants",
	"POST /api/v1/fleet/plans/{planID}/preflight-completions",
	"POST /api/v1/fleet/plans/{planID}/converge-refusals",
	"POST /api/v1/fleet/executor/stop-attestations",
	"POST /api/v1/auth/rotate",
}

// Enrollment exchange while the admin has not approved yet. E1 keeps the
// existing response unchanged: POST /api/v1/enrollments/{id}/exchange answers
// 409 with problem code EnrollmentPendingCode, for a pending and an expired
// enrollment alike. The loop polls every EnrollmentPollInterval until the
// start response's expiresAt and then stops. Every other refusal is terminal;
// above all 401 invalid_enrollment_verifier, which counts toward the
// enrollment lockout and must never be retried.
const (
	EnrollmentPendingCode   = "enrollment_not_approved"
	EnrollmentPendingStatus = 409
	EnrollmentPollInterval  = 5 * time.Second
)

// Refusal codes of ConvergeRefusalRequest. They are also the blocker reasons
// they derive (BlockerReasonDestructive, BlockerReasonStateLockForeign).
const (
	RefusalDestructive      = BlockerReasonDestructive
	RefusalStateLockForeign = BlockerReasonStateLockForeign
)

// MaxRefusalChangesBytes bounds ConvergeRefusalRequest.Changes (encoded).
const MaxRefusalChangesBytes = 16 << 10

// ConvergeRefusalRequest is the body of
// POST /api/v1/fleet/plans/{planID}/converge-refusals (executor principal,
// Idempotency-Key). Ledger kind KindConvergeRefusal, Ref = plan ID, payload
// ConvergeRefusalPayload. Two codes:
//
//   - destructive_requires_github_lane: planTextSha256 and changes describe
//     the tofu plan that was refused; sourceAttemptId and tofuLock are absent.
//     Idempotency-Key executor-refusal:<planID>. It sets
//     ConvergeView.Refused, so the loop stops fetching and the resource
//     blocker is destructive_requires_github_lane. It is admitted while a
//     recovery attempt is live too.
//   - state_lock_foreign: sourceAttemptId (the plan's latest, not succeeded,
//     executor-lane attempt) and tofuLock; every tofuLock field may be empty
//     when tofu's lock text was unreadable. It does NOT set Refused: it
//     derives the blocker state_lock_foreign while that attempt stays the
//     plan's latest.
type ConvergeRefusalRequest struct {
	Code            string           `json:"code"`
	CommitSHA       string           `json:"commitSha"`
	PlanSHA256      string           `json:"planSha256"`
	PlanTextSHA256  string           `json:"planTextSha256,omitempty"`
	Changes         json.RawMessage  `json:"changes,omitempty"`
	SourceAttemptID string           `json:"sourceAttemptId,omitempty"`
	TofuLock        *RefusalTofuLock `json:"tofuLock,omitempty"`
}

// RefusalTofuLock is the foreign OpenTofu state lock a recovery found. Unlike
// TofuLock every field is optional and opaque (created is tofu's own text).
type RefusalTofuLock struct {
	ID        string `json:"id,omitempty"`
	Operation string `json:"operation,omitempty"`
	Who       string `json:"who,omitempty"`
	Created   string `json:"created,omitempty"`
}

// ConvergeRefusalPayload is the ledger payload: the request plus PlanID.
type ConvergeRefusalPayload struct {
	PlanID string `json:"planId"`
	ConvergeRefusalRequest
}

// Validate checks a refusal body's shape. Matching it to the plan and the
// attempt is the route's decision.
func (r ConvergeRefusalRequest) Validate() error {
	if !commitSHARe.MatchString(r.CommitSHA) || !sha256HexRe.MatchString(r.PlanSHA256) {
		return invalid("commitSha and planSha256 must be lowercase Git/SHA-256 values")
	}
	switch r.Code {
	case RefusalDestructive:
		if r.SourceAttemptID != "" || r.TofuLock != nil {
			return invalid("%s carries no sourceAttemptId or tofuLock", RefusalDestructive)
		}
		if r.PlanTextSHA256 != "" && !sha256HexRe.MatchString(r.PlanTextSHA256) {
			return invalid("planTextSha256 must be 64 lowercase hex characters")
		}
		if len(r.Changes) > MaxRefusalChangesBytes {
			return invalid("changes is too large")
		}
		if len(r.Changes) != 0 {
			var object map[string]json.RawMessage
			if json.Unmarshal(r.Changes, &object) != nil || object == nil {
				return invalid("changes must be a JSON object")
			}
		}
	case RefusalStateLockForeign:
		if _, err := uuid.Parse(r.SourceAttemptID); err != nil {
			return invalid("sourceAttemptId must be a UUID")
		}
		if r.TofuLock == nil || r.PlanTextSHA256 != "" || len(r.Changes) != 0 {
			return invalid("%s requires tofuLock and carries no planTextSha256 or changes", RefusalStateLockForeign)
		}
		for _, field := range []string{r.TofuLock.ID, r.TofuLock.Operation, r.TofuLock.Who, r.TofuLock.Created} {
			if len(field) > 512 {
				return invalid("tofuLock fields are limited to 512 bytes")
			}
		}
	default:
		return invalid("code must be %s or %s", RefusalDestructive, RefusalStateLockForeign)
	}
	return nil
}

// StopAttestationPayload is the ledger payload: the request plus PlanID.
type StopAttestationPayload struct {
	PlanID string `json:"planId"`
	StopAttestationRequest
}

// Credential types (credential-provider interface, D3).
const (
	CredDORead        = "do-read"
	CredDOWrite       = "do-write"
	CredSpacesRW      = "spaces-rw"
	CredSpacesRead    = "spaces-read"
	CredSSHNode       = "ssh-node"
	CredBootstrapDir  = "bootstrap-dir"
	CredReleaseGitHub = "release-github"
	CredNornAttempt   = "norn-attempt"
	// CredRepoRead is a short-lived GitHub App installation token restricted
	// to the configured repository and contents:read. It lets the loop fetch
	// the private repository without any GitHub credential at rest on the
	// host. Only the plan and recover grants may carry it.
	CredRepoRead = "repo-read"
)

// Grant pseudo-phases. Every other grant phase is a real lifecycle phase and
// must equal the live attempt's currentPhase exactly; there is no alias.
// GrantPhasePlan precedes any attempt. GrantPhaseRecover needs a live,
// owned recovery attempt (retryOf set) and grants the union of the table.
// GrantPhasePreflight lets an executor verify private bootstrap inputs after
// it owns a live V3 attempt at provider_applying, including a recovery attempt,
// but before it receives provider mutation credentials for that attempt.
const (
	GrantPhasePlan      = "plan"
	GrantPhasePreflight = "preflight"
	GrantPhaseRecover   = "recover"
)

// GrantProfile names the phase list of the backend that owns the attempt.
// Executor plans never drain (contraction and replace are GitHub lane only),
// so PG executor attempts use the PG legacy no-drain list and etcd executor
// attempts use the canonical nine-phase list (lifecycle.Phases, D3/D4).
type GrantProfile string

const (
	GrantProfilePostgresLegacyNoDrain GrantProfile = "pg-legacy-no-drain"
	GrantProfileV3                    GrantProfile = "v3"
)

var (
	planTypes          = []string{CredDORead, CredSpacesRW, CredRepoRead}
	preflightTypes     = []string{CredBootstrapDir, CredSSHNode}
	providerApplyTypes = []string{CredDOWrite, CredSpacesRW, CredNornAttempt}
	// do-read in the post-apply phases is deliberately absent until P2 measures
	// that fleet_hooks.py needs it (plan-5-7 section 2.3).
	postApplyTypes  = []string{CredSpacesRead, CredSSHNode, CredBootstrapDir, CredReleaseGitHub, CredNornAttempt}
	checkpointTypes = []string{CredNornAttempt}
	recoverTypes    = []string{CredDORead, CredDOWrite, CredSpacesRW, CredSpacesRead, CredSSHNode,
		CredBootstrapDir, CredReleaseGitHub, CredNornAttempt, CredRepoRead}
)

// GrantTypes is the admission table: the types allowed per grant phase, per
// profile. The provider apply runs while currentPhase is provider_applying on
// the canonical list, and while it is infrastructure_applied on the PG legacy
// no-drain list (which has no provider_applying phase and whose
// infrastructure_applied checkpoint is written after tofu apply). complete
// has no work and no grant.
var GrantTypes = map[GrantProfile]map[string][]string{
	GrantProfilePostgresLegacyNoDrain: {
		GrantPhasePlan:           planTypes,
		"infrastructure_applied": providerApplyTypes,
		"inventory_generated":    postApplyTypes,
		"nodes_configured":       postApplyTypes,
		"nodes_enrolled":         postApplyTypes,
		"readiness_verified":     postApplyTypes,
		GrantPhaseRecover:        recoverTypes,
	},
	GrantProfileV3: {
		GrantPhasePlan:           planTypes,
		GrantPhasePreflight:      preflightTypes,
		"prechange_verified":     checkpointTypes,
		"provider_applying":      providerApplyTypes,
		"infrastructure_applied": postApplyTypes,
		"inventory_generated":    postApplyTypes,
		"nodes_configured":       postApplyTypes,
		"nodes_enrolled":         postApplyTypes,
		"readiness_verified":     postApplyTypes,
		"old_nodes_drained":      checkpointTypes, // evidence only: executor plans never drain
		GrantPhaseRecover:        recoverTypes,
	},
}

// CredentialGrantRequest is the body of
// POST /api/v1/fleet/plans/{planID}/credential-grants (executor principal,
// Idempotency-Key). AttemptID is required for every phase except GrantPhasePlan.
type CredentialGrantRequest struct {
	AttemptID string   `json:"attemptId,omitempty"`
	Phase     string   `json:"phase"`
	Types     []string `json:"types"`
}

// PreflightCompleteRequest is an executor attestation that its local private
// input checks completed. The digest identifies a non-secret validation
// receipt; Norn verifies binding and order, not the private inputs themselves.
type PreflightCompleteRequest struct {
	AttemptID      string `json:"attemptId"`
	GrantID        string `json:"grantId"`
	EvidenceDigest string `json:"evidenceDigest"`
}

func (r PreflightCompleteRequest) Validate() error {
	if _, err := uuid.Parse(r.AttemptID); err != nil {
		return invalid("attemptId must be a UUID")
	}
	if _, err := uuid.Parse(r.GrantID); err != nil {
		return invalid("grantId must be a UUID")
	}
	if !strings.HasPrefix(r.EvidenceDigest, "sha256:") || !sha256HexRe.MatchString(strings.TrimPrefix(r.EvidenceDigest, "sha256:")) {
		return invalid("evidenceDigest must be sha256 followed by 64 lowercase hex characters")
	}
	return nil
}

type PreflightCompletePayload struct {
	PlanID   string `json:"planId"`
	DeviceID string `json:"deviceId"`
	PreflightCompleteRequest
}

// CredentialGrantResponse is the 201/200 body. AttemptToken is present only
// for a grant that includes CredNornAttempt: the attempt-bound credential
// (scope AttemptScope), a bearer secret that is never stored in the ledger
// (only its token ID is). A replay of the same Idempotency-Key re-signs the
// same token ID; a newer grant for the plan revokes it.
//
// RepoToken is present only for a grant that includes CredRepoRead: a GitHub
// App installation token for the configured repository, contents:read only,
// expiring at GitHub's expiry (about 1 h), never after the grant. It is never
// stored in the ledger or logged (the ledger records only that it was issued
// and its expiry). A replay of the same Idempotency-Key returns the same
// token from the route's memory while it is unexpired; otherwise the replay
// is refused with CodeFleetRepoTokenReplayExpired and the caller must request
// a new grant under a new key.
type CredentialGrantResponse struct {
	GrantID      string    `json:"grantId"`
	ExpiresAt    time.Time `json:"expiresAt"`
	AttemptToken string    `json:"attemptToken,omitempty"`
	RepoToken    string    `json:"repoToken,omitempty"`
}

// AttemptScope is the only scope of the attempt-bound credential the
// norn-attempt grant returns (E1/P1 hand-off). It is internal: it is not an
// access-token scope any enrollment, approval or token request can ask for,
// and it is never listed in the capabilities scope list. The credential is a
// managed token with subject "fleet-attempt:<attemptId>", no device claim,
// recorded against the executor device, expiring with its grant.
const AttemptScope = "fleet:attempt"

// AttemptCredentialRoutes are the only routes the attempt-bound credential
// may reach (E6 wires the scope matcher), and only for its own attempt; the
// reconciliation handlers bind the attemptId of the body or query. Every one
// is also in ExecutorRoutes. Attempt start and cancel, the grant route and
// /auth/rotate are not here.
var AttemptCredentialRoutes = []string{
	"GET /api/v1/fleet/plans/{planID}/attempts/{attemptID}",
	"POST /api/v1/fleet/plans/{planID}/attempts/{attemptID}/heartbeat",
	"POST /api/v1/fleet/plans/{planID}/attempts/{attemptID}/advance",
	"GET /api/v1/fleet/plans/{planID}/reconciliations",
	"POST /api/v1/fleet/plans/{planID}/reconciliations",
}

// CredentialGrantPayload is the ledger payload of fleet.credential-grant
// (Ref = plan ID). Types are sorted and deduplicated. DeviceID and TokenID
// are the server-filled executor principal.
type CredentialGrantPayload struct {
	GrantID   string `json:"grantId"`
	PlanID    string `json:"planId"`
	AttemptID string `json:"attemptId,omitempty"`
	// AttemptPhase binds recover and preflight grants to the attempt's phase
	// when issued, even though their request phase is a pseudo-phase.
	AttemptPhase string    `json:"attemptPhase,omitempty"`
	Phase        string    `json:"phase"`
	Types        []string  `json:"types"`
	DeviceID     string    `json:"deviceId"`
	TokenID      string    `json:"tokenId"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// ProviderEvidence is the optional field added (omitempty) to the existing
// admin-only release and abandon-plan admissions (fleet.target.fence-release
// and fleet.target.abandon-plan). It is NOT proof: Norn holds no provider
// credential (D1) and cannot check it. It is the signed assertion of the
// admin principal who submits it, carried inside the signed, fingerprinted
// admission, never in FleetTargetReleaseEvidence (the server-gathered GitHub
// proof, which it can never satisfy). The server adds its own checked facts:
// the admin scope, DropletID equal to the holder's latest executor attempt
// metadata, the holder's executor device revoked (CodeFleetExecutorDeviceActive
// otherwise) and the H5 age gate; it stores Digest in operation metadata under
// ProviderEvidenceDigestMetadataKey.
type ProviderEvidence struct {
	Provider   string    `json:"provider"` // "digitalocean"
	DropletID  string    `json:"dropletId"`
	Status     string    `json:"status"` // off | archive | not_found
	ObservedAt time.Time `json:"observedAt"`
	Reference  string    `json:"reference,omitempty"` // console or audit-log pointer, <= 512 bytes
}

// ProviderEvidenceRequest is the body of
// POST /api/v1/fleet/plans/{planID}/provider-evidence (admin, non-CI,
// Idempotency-Key; non-releasing). Ledger kind
// fleet.executor.provider-evidence, Ref = plan ID, payload {planId,
// sourceAttemptId, providerEvidence}. It is the recovery use of the evidence:
// proof for a resume on a rebuilt host, read by recovery admission.
type ProviderEvidenceRequest struct {
	SourceAttemptID  string           `json:"sourceAttemptId"`
	ProviderEvidence ProviderEvidence `json:"providerEvidence"`
}

// ProviderEvidenceDigestMetadataKey is the operation-metadata key holding the
// stored digest. A caller-supplied value under it is discarded.
const ProviderEvidenceDigestMetadataKey = "fleetProviderEvidenceDigest"

// Provider evidence statuses.
const (
	ProviderStatusOff      = "off"
	ProviderStatusArchive  = "archive"
	ProviderStatusNotFound = "not_found"
)

var (
	dropletIDRe    = regexp.MustCompile(`^[0-9]{1,20}$`)
	invocationIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	commitSHARe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256HexRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	attemptIDRe    = regexp.MustCompile(`^executor:([0-9]{1,20}):([0-9a-f]{32}):([0-9a-f-]{36})$`)
	workflowURLRe  = regexp.MustCompile(`^norn-executor://([0-9]{1,20})/([0-9a-f]{32})$`)
)

// HolderNonceSHA256 is the fence holder nonce hash of an executor-lane plan.
// DecideAcquire and DecideBind are unchanged.
func HolderNonceSHA256(planID string) string {
	sum := sha256.Sum256([]byte("executor-lane:" + planID))
	return hex.EncodeToString(sum[:])
}

// PrincipalSubject is the attempt's principal_subject for an executor device.
// It names the device, not the token, so the idle self-rotation of D8 (which
// keeps the device and issues a new token ID) never orphans a live attempt,
// and DELETE /api/v1/devices/{id} cuts every attempt the device owns.
func PrincipalSubject(deviceID string) string {
	return "managed-device:" + deviceID
}

// RunnerAttemptID is "executor:<dropletId>:<invocationId>:<planId>".
func RunnerAttemptID(dropletID, invocationID, planID string) string {
	return "executor:" + dropletID + ":" + invocationID + ":" + planID
}

// WorkflowURL is "norn-executor://<dropletId>/<invocationId>". It is not an
// https URL, so every GitHub-lane URL check rejects it.
func WorkflowURL(dropletID, invocationID string) string {
	return "norn-executor://" + dropletID + "/" + invocationID
}

// ParseRunnerAttemptID reports whether id is an executor-lane runner attempt ID.
func ParseRunnerAttemptID(id string) (dropletID, invocationID, planID string, ok bool) {
	m := attemptIDRe.FindStringSubmatch(id)
	if m == nil {
		return "", "", "", false
	}
	if _, err := uuid.Parse(m[3]); err != nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// Validate checks an executor start body against the path plan ID.
func (r AttemptStartRequest) Validate(planID string) error {
	if r.SchemaVersion != AttemptSchemaVersion {
		return invalid("schemaVersion must be %q", AttemptSchemaVersion)
	}
	if !commitSHARe.MatchString(r.CommitSHA) || !sha256HexRe.MatchString(r.PlanSHA256) {
		return invalid("commitSha and planSha256 must be lowercase Git/SHA-256 values")
	}
	if _, _, attemptPlan, ok := ParseRunnerAttemptID(r.RunnerAttemptID); !ok || attemptPlan != planID {
		return invalid("runnerAttemptId must be executor:<dropletId>:<invocationId>:<planId> for this plan")
	}
	if r.HeartbeatTimeoutSeconds != 0 && (r.HeartbeatTimeoutSeconds < 30 || r.HeartbeatTimeoutSeconds > 900) {
		return invalid("heartbeatTimeoutSeconds must be between 30 and 900")
	}
	return r.Executor.Validate(r.RunnerAttemptID, r.WorkflowURL, r.CommitSHA)
}

// Validate checks the metadata's shape and its consistency with the attempt.
func (m AttemptMetadata) Validate(runnerAttemptID, workflowURL, commitSHA string) error {
	droplet, invocation, _, ok := ParseRunnerAttemptID(runnerAttemptID)
	if !ok {
		return invalid("runnerAttemptId is not an executor attempt ID")
	}
	w := workflowURLRe.FindStringSubmatch(workflowURL)
	switch {
	case m.Lane != LaneMetadataValue:
		return invalid("metadata lane must be %q", LaneMetadataValue)
	case w == nil || w[1] != droplet || w[2] != invocation:
		return invalid("workflowUrl must be norn-executor://<dropletId>/<invocationId> matching runnerAttemptId")
	case m.DropletID != droplet || m.InvocationID != invocation:
		return invalid("metadata dropletId and invocationId must match runnerAttemptId")
	case !boundedIdentity(m.MachineID) || !boundedIdentity(m.BootID) || m.LoopRelease == "" || m.TofuVersion == "" || len(m.LoopRelease)+len(m.TofuVersion) > 256:
		return invalid("metadata machineId, bootId, loopRelease and tofuVersion are required and bounded")
	case !commitSHARe.MatchString(m.CommitSHA) || m.CommitSHA != commitSHA:
		return invalid("metadata commitSha must be the attempt commit")
	case !sha256HexRe.MatchString(m.WheelhouseSHA256):
		return invalid("metadata wheelhouseSha256 must be 64 lowercase hex characters")
	}
	return nil
}

func boundedIdentity(value string) bool {
	return value != "" && len(value) <= 128
}

// Validate checks a converge plan's shape.
func (p ConvergePlan) Validate() error {
	switch {
	case p.Resource == "" || p.Repository == "":
		return invalid("converge plan requires resource and repository")
	case !commitSHARe.MatchString(p.CommitSHA) || (p.BaseCommitSHA != "" && !commitSHARe.MatchString(p.BaseCommitSHA)):
		return invalid("converge plan commit SHAs must be 40 lowercase hex characters")
	case p.PathClass != PathClassConfig && p.PathClass != PathClassExecutable:
		return invalid("converge plan pathClass must be config or executable")
	case p.PathClass == PathClassExecutable && !p.RequiresApproval:
		return invalid("an executable converge plan always requires approval")
	case p.Generation < 1:
		return invalid("converge plan generation must be positive")
	}
	return nil
}

// Validate checks an approve request.
func (r ApproveRequest) Validate() error {
	switch {
	case !commitSHARe.MatchString(r.CommitSHA):
		return invalid("commitSha must be a 40-character lowercase hex SHA")
	case r.Resume && r.SourceAttemptID == "":
		return invalid("resume requires sourceAttemptId")
	case !r.Resume && r.SourceAttemptID != "":
		return invalid("sourceAttemptId is only valid with resume")
	}
	if r.SourceAttemptID != "" {
		if _, err := uuid.Parse(r.SourceAttemptID); err != nil {
			return invalid("sourceAttemptId must be a UUID")
		}
	}
	return nil
}

// Validate checks an execution-mode request.
func (r ExecutionModeRequest) Validate() error {
	if r.Mode != ModeGitHub && r.Mode != ModeController {
		return invalid("mode must be github or controller")
	}
	if r.ExpectedRevision < 0 {
		return invalid("expectedRevision must not be negative")
	}
	return nil
}

// Validate checks an attestation request's shape. The decision needs the
// source attempt and is ValidStopAttestation.
func (r StopAttestationRequest) Validate() error {
	if _, err := uuid.Parse(r.SourceAttemptID); err != nil {
		return invalid("sourceAttemptId must be a UUID")
	}
	if !dropletIDRe.MatchString(r.DropletID) || !invocationIDRe.MatchString(r.InvocationID) || !boundedIdentity(r.MachineID) || !boundedIdentity(r.BootID) || r.ObservedAt.IsZero() {
		return invalid("dropletId, machineId, bootId, invocationId and observedAt are required")
	}
	if r.TofuLock != nil && (r.TofuLock.ID == "" || r.TofuLock.Operation == "" || r.TofuLock.Who == "" || r.TofuLock.Created.IsZero()) {
		return invalid("tofuLock requires id, operation, who and created")
	}
	return nil
}

// ValidStopAttestation decides an attestation (ADR 0010, B3). source is the
// source attempt's stored metadata and sourceSubject its principal_subject;
// attesterSubject is PrincipalSubject of the calling executor principal.
// Valid only when:
//   - the attester is the same device, droplet and machine as the source
//     (a snapshot clone keeps machine-id but not the droplet ID; a rebuilt
//     host is a new device);
//   - the attester is a different systemd invocation than the source;
//   - either the boot changed, or on the same boot no norn-phase process
//     exists, the source phase process group is reaped and the unit cgroup
//     subtree holds only the attester (SourceCgroupEmpty);
//   - observedAt is within AttestationMaxAge (and at most a minute ahead).
//
// A different host can never attest; it needs admin ProviderEvidence.
func ValidStopAttestation(r StopAttestationRequest, source AttemptMetadata, sourceSubject, attesterSubject string, now time.Time) error {
	if err := r.Validate(); err != nil {
		return err
	}
	switch {
	case source.Lane != LaneMetadataValue:
		return refuse(CodeFleetExecutorStopAttestationInvalid, "source attempt is not on the executor lane")
	case sourceSubject == "" || sourceSubject != attesterSubject:
		return refuse(CodeFleetExecutorStopAttestationInvalid, "attesting device is not the source attempt's device")
	case r.DropletID != source.DropletID || r.MachineID != source.MachineID:
		return refuse(CodeFleetExecutorStopAttestationInvalid, "attesting droplet or machine differs from the source attempt")
	case r.InvocationID == source.InvocationID:
		return refuse(CodeFleetExecutorStopAttestationInvalid, "the source invocation cannot attest its own stop")
	case now.Sub(r.ObservedAt) > AttestationMaxAge || r.ObservedAt.After(now.Add(time.Minute)):
		return refuse(CodeFleetExecutorStopAttestationInvalid, "observedAt is not recent")
	case r.BootID == source.BootID && !(r.PhaseUIDEmpty && r.PhaseProcessGroupReaped && r.SourceCgroupEmpty):
		return refuse(CodeFleetExecutorStopAttestationInvalid, "same-boot attestation requires no norn-phase process, a reaped process group and an empty source cgroup")
	}
	return nil
}

// Normalize returns the request with Types sorted and deduplicated.
func (r CredentialGrantRequest) Normalize() CredentialGrantRequest {
	set := map[string]bool{}
	types := []string{}
	for _, t := range r.Types {
		if !set[t] {
			set[t] = true
			types = append(types, t)
		}
	}
	sort.Strings(types)
	r.Types = types
	return r
}

// Validate applies the admission table for profile. Attempt liveness,
// ownership, recovery and the phase-equals-currentPhase rule need the attempt
// and belong to the server (CodeFleetCredentialGrantPhaseMismatch).
func (r CredentialGrantRequest) Validate(profile GrantProfile) error {
	table, ok := GrantTypes[profile]
	if !ok {
		return fmt.Errorf("unknown grant profile %q", profile)
	}
	allowed, ok := table[r.Phase]
	if !ok {
		return invalid("phase %q has no grant on this backend", r.Phase)
	}
	if len(r.Types) == 0 {
		return invalid("types must not be empty")
	}
	if r.Phase == GrantPhasePlan {
		if r.AttemptID != "" {
			return invalid("a plan grant precedes any attempt")
		}
	} else if _, err := uuid.Parse(r.AttemptID); err != nil {
		return refuse(CodeFleetCredentialGrantAttemptRequired, "attemptId is required for phase %q", r.Phase)
	}
	for _, t := range r.Normalize().Types {
		found := false
		for _, a := range allowed {
			found = found || a == t
		}
		if !found {
			return refuse(CodeFleetCredentialTypeNotAllowed, "credential type %q is not allowed for phase %q", t, r.Phase)
		}
	}
	return nil
}

// Validate checks provider evidence at time now.
func (e ProviderEvidence) Validate(now time.Time) error {
	bad := func(reason string) error { return refuse(CodeFleetProviderEvidenceInvalid, "%s", reason) }
	switch {
	case e.Provider != "digitalocean":
		return bad("provider must be digitalocean")
	case !dropletIDRe.MatchString(e.DropletID):
		return bad("dropletId must be decimal digits")
	case e.Status != ProviderStatusOff && e.Status != ProviderStatusArchive && e.Status != ProviderStatusNotFound:
		return bad("status must be off, archive or not_found")
	case e.ObservedAt.IsZero() || now.Sub(e.ObservedAt) > ProviderEvidenceMaxAge || e.ObservedAt.After(now.Add(time.Minute)):
		return bad("observedAt must be within the last hour")
	case len(e.Reference) > 512:
		return bad("reference exceeds 512 bytes")
	}
	return nil
}

// Digest is "sha256:" + hex of the canonical JSON of the evidence (field
// order as declared, observedAt in UTC).
func (e ProviderEvidence) Digest() string {
	e.ObservedAt = e.ObservedAt.UTC()
	encoded, _ := json.Marshal(e)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}
