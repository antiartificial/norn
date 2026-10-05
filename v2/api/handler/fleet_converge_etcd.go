package handler

// The etcd side of the converge lane (plan-5-7 work package E6): the converge
// plan recorder (controller.ConvergePlanRecorder), the plan reader and approval
// recorder behind FleetConvergeRoutes, the execution-mode recorder, the
// converge facts reader and the watcher's desired-revision setter.
//
// Plans are signed with controller.CanonicalConvergePlan (BuildConvergePlan
// does that); the etcd capacity-plan signer refreshEtcdCapacityPlanSignature
// would drop the converge object, so nothing here re-signs a plan. The
// recorder commits through V3OperationStore.AcceptFleetConvergePlan, which
// pins the resource's execution mode in the same Txn.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// EtcdConvergePlanRecorder is the etcd controller.ConvergePlanRecorder, the
// ConvergePlanReader of the converge routes and the plan source of the facts
// reader.
type EtcdConvergePlanRecorder struct {
	cfg        *config.Config
	operations *etcdstore.V3OperationStore
}

// NewEtcdConvergePlanRecorder returns the etcd recorder.
func NewEtcdConvergePlanRecorder(cfg *config.Config, operations *etcdstore.V3OperationStore) *EtcdConvergePlanRecorder {
	return &EtcdConvergePlanRecorder{cfg: cfg, operations: operations}
}

// recorded wraps a stored plan. Admissible is false for an abandoned plan
// (fleet_plan_revalidation_required) so a rescan after a mode round-trip
// records a fresh plan, and stays true for a refused plan so a rescan never
// records the same commit again.
func (p *EtcdConvergePlanRecorder) recorded(ctx context.Context, plan *controller.ConvergeCapacityPlan) (*controller.RecordedConverge, error) {
	abandoned, err := p.operations.CheckFleetTargetPlanAbandoned(ctx, plan.ID)
	if err != nil {
		return nil, err
	}
	return &controller.RecordedConverge{PlanID: plan.ID, Digest: plan.Digest, Converge: *plan.Converge, Admissible: !abandoned}, nil
}

// NewestConvergePlan implements controller.ConvergePlanRecorder. Newest is
// (StartedAt, ID) descending, as on PG and in executor admission.
func (p *EtcdConvergePlanRecorder) NewestConvergePlan(ctx context.Context, resource string) (*controller.RecordedConverge, error) {
	op, err := p.operations.NewestFleetConvergePlan(ctx, resource)
	if err != nil || op == nil {
		return nil, err
	}
	plan, ok := decodeConvergePlan(op)
	if !ok || plan.Converge.Resource != resource {
		return nil, nil
	}
	return p.recorded(ctx, plan)
}

// RecordConvergePlan implements controller.ConvergePlanRecorder.
func (p *EtcdConvergePlanRecorder) RecordConvergePlan(ctx context.Context, plan *controller.ConvergeCapacityPlan, key string) (*controller.RecordedConverge, error) {
	authority, err := p.operations.Authority(ctx)
	if err != nil {
		return nil, err
	}
	const subject = "fleet-converge-watcher"
	resource := plan.Converge.Resource
	identity := store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/system", Subject: subject}, Kind: "fleet.capacity-plan", Resource: resource, Key: key}
	// A replayed key returns the first plan: the accepted intent covers the
	// operation's identity, so a second plan object under the same key would
	// conflict rather than replay.
	if replayed, found, err := p.resolve(ctx, identity, plan); err != nil {
		return nil, err
	} else if found {
		return replayed, nil
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	finished := now
	acceptance := store.OperationAcceptance{
		Identity: identity, Audit: store.AcceptanceAuditContext{Source: subject},
		Operation: model.Operation{ID: plan.ID, Kind: "fleet.capacity-plan", Ref: resource, Status: model.OperationSucceeded,
			Risk: "records a converge plan for the controller-run executor; no provider mutation performed", Source: "control-api", Message: "converge plan recorded; no provider mutation performed",
			Payload: payload, Metadata: map[string]interface{}{"planId": plan.ID, "planDigest": plan.Digest, "signature": plan.Signature},
			StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1},
		Semantics: map[string]interface{}{"action": "fleet.capacity-plan", "resource": resource, "commitSha": plan.Converge.CommitSHA, "baseCommitSha": plan.Converge.BaseCommitSHA},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		return nil, err
	}
	accepted, err := p.operations.AcceptFleetConvergePlan(ctx, acceptance)
	if err != nil {
		if replayed, found, _ := p.resolve(ctx, identity, plan); found {
			return replayed, nil
		}
		return nil, err
	}
	stored, ok := decodeConvergePlan(&accepted.Operation)
	if !ok {
		return nil, errors.New("recorded operation is not a converge plan")
	}
	return p.recorded(ctx, stored)
}

func (p *EtcdConvergePlanRecorder) resolve(ctx context.Context, identity store.OperationRequestIdentity, plan *controller.ConvergeCapacityPlan) (*controller.RecordedConverge, bool, error) {
	accepted, err := p.operations.ResolveIdentity(ctx, identity)
	if errors.Is(err, store.ErrAcceptanceNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	stored, ok := decodeConvergePlan(&accepted.Operation)
	if !ok || accepted.Operation.Ref != identity.Resource || stored.Converge.CommitSHA != plan.Converge.CommitSHA || stored.Converge.BaseCommitSHA != plan.Converge.BaseCommitSHA {
		return nil, false, &store.AcceptanceConflictError{Identity: identity}
	}
	rec, err := p.recorded(ctx, stored)
	return rec, err == nil, err
}

// ConvergePlan implements ConvergePlanReader. The plan is verified like
// admission verifies it (current and previous audit keys).
func (p *EtcdConvergePlanRecorder) ConvergePlan(ctx context.Context, planID string) (*controller.RecordedConverge, error) {
	op, err := p.operations.GetOperation(ctx, planID)
	if errors.Is(err, etcdstore.ErrNotFound) || (err == nil && op.Kind != "fleet.capacity-plan") {
		return nil, ErrConvergePlanNotFound
	}
	if err != nil {
		return nil, err
	}
	plan, ok := decodeConvergePlan(op)
	if !ok {
		return nil, ErrNotConvergePlan
	}
	var keys []string
	if p.cfg != nil {
		keys = append([]string{p.cfg.AuditSigningKey}, p.cfg.AuditPreviousSigningKeys...)
	}
	if !controller.VerifyConvergePlan(plan, keys) {
		return nil, ErrConvergePlanInvalid
	}
	return p.recorded(ctx, plan)
}

// NewEtcdFleetConvergeRoutes returns the etcd converge routes (rescan and
// approve) over the same recorder and approval ledger.
func NewEtcdFleetConvergeRoutes(cfg *config.Config, operations *etcdstore.V3OperationStore, rescanner ConvergeRescanner) *FleetConvergeRoutes {
	return NewFleetConvergeRoutes(rescanner, NewEtcdConvergePlanRecorder(cfg, operations), &etcdConvergeApprovals{cfg: cfg, operations: operations})
}

// etcdConvergeApprovals records fleet.converge.approve through the signed
// etcd acceptance. approvedBy is the operation actor's subject, as on PG.
type etcdConvergeApprovals struct {
	cfg        *config.Config
	operations *etcdstore.V3OperationStore
}

func (a *etcdConvergeApprovals) RecordApproval(w http.ResponseWriter, r *http.Request, key string, admission executor.ApproveAdmission) (model.Operation, bool, bool) {
	principal, present := AccessPrincipalFromRequest(r)
	if !present {
		WriteControlProblem(w, r, http.StatusUnauthorized, "authenticated_principal_required", "an explicitly authenticated control principal is required")
		return model.Operation{}, false, false
	}
	actor, audit, ok := (&etcdFleetTargetBackend{cfg: a.cfg, operations: a.operations}).Actor(w, r, principal)
	if !ok {
		return model.Operation{}, false, false
	}
	audit.Scopes = append([]string(nil), principal.Scopes...)
	authority, err := a.operations.Authority(r.Context())
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "control authority is unavailable")
		return model.Operation{}, false, false
	}
	encoded, _ := json.Marshal(admission)
	var payload map[string]interface{}
	_ = json.Unmarshal(encoded, &payload)
	now := time.Now().UTC()
	finished := now
	acceptance := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: authority, Actor: actor, Kind: executor.KindConvergeApprove, Resource: admission.PlanID, Key: key}, Audit: audit,
		Operation: model.Operation{ID: uuid.NewString(), Kind: executor.KindConvergeApprove, Ref: admission.PlanID, Status: model.OperationSucceeded,
			Risk: "records an operator approval; no provider mutation performed", Source: "control-api", Message: "converge approval recorded",
			Payload: payload, Metadata: map[string]interface{}{"planId": admission.PlanID, "approvedBy": actor.Subject}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1},
		Semantics: map[string]interface{}{"action": executor.KindConvergeApprove, "planId": admission.PlanID, "commitSha": admission.CommitSHA, "resume": admission.Resume, "sourceAttemptId": admission.SourceAttemptID},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_acceptance_failed", "failed to fingerprint operation")
		return model.Operation{}, false, false
	}
	accepted, err := a.operations.Accept(r.Context(), acceptance)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return model.Operation{}, false, false
	}
	return accepted.Operation, accepted.Replayed, true
}

// NewEtcdExecutionModeRecorder returns the etcd FleetExecutionModeRecorder:
// one V3OperationStore.SetFleetResourceExecutionMode Txn applies and records.
func NewEtcdExecutionModeRecorder(cfg *config.Config, operations *etcdstore.V3OperationStore) FleetExecutionModeRecorder {
	return &etcdExecutionModeRecorder{cfg: cfg, operations: operations}
}

type etcdExecutionModeRecorder struct {
	cfg        *config.Config
	operations *etcdstore.V3OperationStore
}

func (a *etcdExecutionModeRecorder) RecordExecutionMode(w http.ResponseWriter, r *http.Request, key, resource string, request executor.ExecutionModeRequest) (model.Operation, bool, bool) {
	principal, present := AccessPrincipalFromRequest(r)
	if !present {
		WriteControlProblem(w, r, http.StatusUnauthorized, "authenticated_principal_required", "an explicitly authenticated control principal is required")
		return model.Operation{}, false, false
	}
	actor, audit, ok := (&etcdFleetTargetBackend{cfg: a.cfg, operations: a.operations}).Actor(w, r, principal)
	if !ok {
		return model.Operation{}, false, false
	}
	audit.Scopes = append([]string(nil), principal.Scopes...)
	authority, err := a.operations.Authority(r.Context())
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "control authority is unavailable")
		return model.Operation{}, false, false
	}
	identity := store.OperationRequestIdentity{Authority: authority, Actor: actor, Kind: executor.KindResourceExecutionMode, Resource: resource, Key: key}
	accepted, err := a.operations.SetFleetResourceExecutionMode(r.Context(), identity, audit, request)
	if err != nil {
		var refusal *executor.Error
		mapped := mapFleetResourceEtcdError(err)
		switch {
		case errors.As(err, &refusal):
			writeExecutorError(w, r, err)
		case errors.Is(mapped, errFleetResourceNotFound), errors.Is(mapped, errFleetResourceRevisionConflict):
			writeFleetResourceError(w, r, mapped)
		default:
			writeOperationAcceptanceError(w, r, err)
		}
		return model.Operation{}, false, false
	}
	return accepted.Operation, accepted.Replayed, true
}

// --- facts reader ---

type etcdConvergeFacts struct {
	operations *etcdstore.V3OperationStore
	plans      *EtcdConvergePlanRecorder
	decisions  ConvergeDecisionSource
	now        func() time.Time
}

// NewEtcdConvergeFactsReader returns the etcd FleetConvergeFactsReader, the
// counterpart of Handler.ConvergeFactsReader: the same facts from the same
// ledger kinds (attestations without an age check, provider evidence with
// Validate, ResumeNeededAttemptID, StateLock and HolderRefused). decisions is
// the watcher, nil when this process runs none.
func NewEtcdConvergeFactsReader(cfg *config.Config, operations *etcdstore.V3OperationStore, decisions ConvergeDecisionSource) FleetConvergeFactsReader {
	return &etcdConvergeFacts{operations: operations, plans: NewEtcdConvergePlanRecorder(cfg, operations), decisions: decisions, now: time.Now}
}

func (f *etcdConvergeFacts) ConvergeFacts(ctx context.Context, resource *controller.Resource, status controller.Status) (*controller.ConvergeFacts, error) {
	facts := &controller.ConvergeFacts{}
	if f.decisions != nil {
		if decision, ok := f.decisions.Decision(resource.Name); ok {
			switch decision.Action {
			case controller.WatchPending:
				facts.Trigger = controller.TriggerPending
			case controller.WatchUnverified:
				facts.Trigger = controller.TriggerUnverified
			case controller.WatchHistoryRewritten:
				facts.Trigger = controller.TriggerHistoryRewritten
			}
			facts.TriggerReason = decision.Reason
		}
	}
	if resource.Desired.PlanID != "" {
		recorded, err := f.plans.ConvergePlan(ctx, resource.Desired.PlanID)
		switch {
		case errors.Is(err, ErrConvergePlanNotFound), errors.Is(err, ErrNotConvergePlan):
		case err != nil:
			return nil, err
		default:
			plan := &controller.ConvergePlanFacts{PlanID: recorded.PlanID, CommitSHA: recorded.Converge.CommitSHA, BaseCommitSHA: recorded.Converge.BaseCommitSHA,
				PlanSHA256: strings.TrimPrefix(recorded.Digest, "sha256:"), RequiresApproval: recorded.Converge.RequiresApproval}
			approves, err := f.operations.FleetExecutorLedgerRows(ctx, executor.KindConvergeApprove, plan.PlanID)
			if err != nil {
				return nil, err
			}
			for i := len(approves) - 1; i >= 0; i-- { // newest first, as PG lists them
				var approve executor.ApproveAdmission
				if decodeFleetLedgerValue(approves[i].Payload, &approve) && approve.PlanID == plan.PlanID && approve.CommitSHA == plan.CommitSHA && !approve.Resume {
					plan.Approved = true
					if by, _ := approves[i].Metadata["approvedBy"].(string); by != "" {
						plan.ApprovedBy = by
					}
					break
				}
			}
			refusals, err := f.refusals(ctx, plan.PlanID)
			if err != nil {
				return nil, err
			}
			for _, refusal := range refusals {
				if refusal.Code == executor.RefusalDestructive && refusal.CommitSHA == plan.CommitSHA {
					plan.Refused = true
					break
				}
			}
			facts.Plan = plan
		}
	}
	if status.Active != nil {
		state, err := f.holderState(ctx, status.Active.PlanID)
		if err != nil {
			return nil, err
		}
		facts.ResumeApproved, facts.ResumeNeededAttemptID, facts.StateLock = state.resumeApproved, state.resumeNeededAttemptID, state.stateLock
		facts.HolderRefused = state.refused
		facts.HolderCommitSHA = state.commitSHA
	}
	return facts, nil
}

func (f *etcdConvergeFacts) refusals(ctx context.Context, planID string) ([]executor.ConvergeRefusalPayload, error) {
	rows, err := f.operations.FleetExecutorLedgerRows(ctx, executor.KindConvergeRefusal, planID)
	if err != nil {
		return nil, err
	}
	var out []executor.ConvergeRefusalPayload
	for i := len(rows) - 1; i >= 0; i-- {
		var payload executor.ConvergeRefusalPayload
		if decodeFleetLedgerValue(rows[i].Payload, &payload) && payload.PlanID == planID {
			out = append(out, payload)
		}
	}
	return out, nil
}

// holderState mirrors pgConvergeFacts.holderState over the etcd readers.
func (f *etcdConvergeFacts) holderState(ctx context.Context, planID string) (pgHolderState, error) {
	var state pgHolderState
	converge, err := f.plans.ConvergePlan(ctx, planID)
	if errors.Is(err, ErrConvergePlanNotFound) || errors.Is(err, ErrNotConvergePlan) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.commitSHA = converge.Converge.CommitSHA
	attempts, err := f.operations.FleetExecutorAttempts(ctx, planID)
	if err != nil || len(attempts) == 0 {
		return state, err
	}
	latest := attempts[0]
	hasChild := map[string]bool{}
	for _, a := range attempts {
		if a.Attempt > latest.Attempt {
			latest = a
		}
		if a.RetryOf != "" {
			hasChild[a.RetryOf] = true
		}
	}
	if !latest.Executor {
		return state, nil
	}
	refusals, err := f.refusals(ctx, planID)
	if err != nil {
		return state, err
	}
	for _, refusal := range refusals {
		if refusal.Code == executor.RefusalDestructive && refusal.CommitSHA == converge.Converge.CommitSHA {
			state.refused = true
		}
	}
	for _, refusal := range refusals {
		if refusal.Code == executor.RefusalStateLockForeign && refusal.SourceAttemptID == latest.ID {
			lock := controller.StateLockFacts{SourceAttemptID: latest.ID}
			if refusal.TofuLock != nil {
				lock.LockID, lock.Who, lock.Operation = refusal.TofuLock.ID, refusal.TofuLock.Who, refusal.TofuLock.Operation
			}
			state.stateLock = &lock
			break
		}
	}
	if latest.Status == string(model.FleetRunnerAttemptSucceeded) || hasChild[latest.ID] {
		return state, nil
	}
	stopped, err := f.stopProof(ctx, planID, latest)
	if err != nil || !stopped {
		return state, err
	}
	approves, err := f.operations.FleetExecutorLedgerRows(ctx, executor.KindConvergeApprove, planID)
	if err != nil {
		return state, err
	}
	for _, row := range approves {
		var approve executor.ApproveAdmission
		if decodeFleetLedgerValue(row.Payload, &approve) && approve.Resume && approve.PlanID == planID &&
			approve.CommitSHA == converge.Converge.CommitSHA && approve.SourceAttemptID == latest.ID {
			state.resumeApproved = true
			return state, nil
		}
	}
	state.resumeNeededAttemptID = latest.ID
	return state, nil
}

// stopProof: a stop attestation (age ignored, see pgConvergeFacts.holderState)
// or admin provider evidence exists for exactly attempt latest.
func (f *etcdConvergeFacts) stopProof(ctx context.Context, planID string, latest store.FleetExecutorAttemptFact) (bool, error) {
	now := f.now().UTC()
	attestations, err := f.operations.FleetExecutorLedgerRows(ctx, executor.KindStopAttestation, planID)
	if err != nil {
		return false, err
	}
	for _, row := range attestations {
		var payload executor.StopAttestationPayload
		if !decodeFleetLedgerValue(row.Payload, &payload) || payload.PlanID != planID || payload.SourceAttemptID != latest.ID {
			continue
		}
		attester, _ := row.Metadata[store.FleetExecutorAttesterMetadataKey].(string)
		// now = observedAt: the age bound is vacuous, every other rule applies.
		if executor.ValidStopAttestation(payload.StopAttestationRequest, latest.Metadata, latest.PrincipalSubject, attester, payload.ObservedAt) == nil {
			return true, nil
		}
	}
	evidenceRows, err := f.operations.FleetExecutorLedgerRows(ctx, store.KindFleetExecutorProviderEvidence, planID)
	if err != nil {
		return false, err
	}
	for _, row := range evidenceRows {
		var payload store.FleetExecutorProviderEvidencePayload
		digest, _ := row.Metadata[executor.ProviderEvidenceDigestMetadataKey].(string)
		if !decodeFleetLedgerValue(row.Payload, &payload) || payload.PlanID != planID || payload.SourceAttemptID != latest.ID {
			continue
		}
		evidence := payload.ProviderEvidence
		if evidence.Validate(now) == nil && evidence.DropletID == latest.Metadata.DropletID && digest != "" && evidence.Digest() == digest {
			return true, nil
		}
	}
	return false, nil
}
