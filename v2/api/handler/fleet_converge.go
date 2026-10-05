package handler

// Converge routes and the PG converge-plan recorder (plan-5-7 sections 2.4 and
// 2.5, work package E4a). Route wiring belongs to E6; this file only defines
// the handlers and the PG adapter:
//
//	POST /api/v1/fleet/resources/{name}/converge             (Rescan)
//	POST /api/v1/fleet/plans/{planID}/converge/approve       (Approve)
//
// The approve route records the signed fleet.converge.approve ledger operation
// in the pinned shape (executor.ApproveAdmission, Ref = plan ID). Consuming a
// resume approve and checking that the source attempt exists are admission
// concerns (E2/E4b), not this route's.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// Sentinels a ConvergePlanReader returns.
var (
	ErrConvergePlanNotFound = errors.New("fleet plan not found")
	ErrNotConvergePlan      = errors.New("fleet plan is not a converge plan")
	// ErrConvergePlanInvalid means the stored plan fails digest or signature
	// verification (VerifyConvergePlan with the current and previous keys).
	ErrConvergePlanInvalid = errors.New("converge plan failed integrity verification")
)

// ConvergeRescanner is the watcher subset the rescan route uses.
// *controller.Watcher implements it.
type ConvergeRescanner interface {
	Rescan(ctx context.Context, resource string) (controller.WatchDecision, error)
}

// ConvergePlanReader reads one converge plan by ID.
type ConvergePlanReader interface {
	ConvergePlan(ctx context.Context, planID string) (*controller.RecordedConverge, error)
}

// ConvergeApprovalRecorder records the approve ledger operation. It writes the
// problem response itself and returns ok=false when it did (the PG adapter
// needs the request's signed acceptance context).
type ConvergeApprovalRecorder interface {
	RecordApproval(w http.ResponseWriter, r *http.Request, key string, admission executor.ApproveAdmission) (op model.Operation, replayed, ok bool)
}

// FleetConvergeRoutes holds the converge handlers.
type FleetConvergeRoutes struct {
	rescanner ConvergeRescanner
	plans     ConvergePlanReader
	approvals ConvergeApprovalRecorder
}

// NewFleetConvergeRoutes builds the routes from backend-neutral parts (etcd
// supplies its own plan reader and approval recorder, E6).
func NewFleetConvergeRoutes(rescanner ConvergeRescanner, plans ConvergePlanReader, approvals ConvergeApprovalRecorder) *FleetConvergeRoutes {
	return &FleetConvergeRoutes{rescanner: rescanner, plans: plans, approvals: approvals}
}

// FleetConvergeRoutes returns the PG routes.
func (h *Handler) FleetConvergeRoutes(rescanner ConvergeRescanner) *FleetConvergeRoutes {
	return NewFleetConvergeRoutes(rescanner, &PGConvergePlanRecorder{h: h}, &pgConvergeApprovals{h: h})
}

func writeExecutorError(w http.ResponseWriter, r *http.Request, err error) {
	var refusal *executor.Error
	if errors.As(err, &refusal) {
		status, ok := executor.CodeStatus[refusal.Code]
		if !ok {
			status = http.StatusConflict
		}
		WriteControlProblem(w, r, status, refusal.Code, refusal.Error())
		return
	}
	WriteControlProblem(w, r, http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest, err.Error())
}

func requireIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 200 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "a non-empty Idempotency-Key of at most 200 characters is required")
		return "", false
	}
	return key, true
}

// Rescan is POST resources/{name}/converge: no body, Idempotency-Key required,
// a non-CI platform:operate caller or the executor principal. It re-reads the
// tip and records a converge plan only when the verified tip has no admissible
// plan, so repeating it is harmless.
func (s *FleetConvergeRoutes) Rescan(w http.ResponseWriter, r *http.Request) {
	if _, ok := FleetExecutorPrincipalFromRequest(r); !ok {
		if _, ok := requireTargetScope(w, r, ScopePlatformOperate, true); !ok {
			return
		}
	}
	name, ok := resourceName(w, r)
	if !ok {
		return
	}
	// The key is required by the route contract, but replay safety does not
	// rest on it: the watcher derives its record key from (resource, tip,
	// superseded plan) only, so every caller, replay and concurrent watcher
	// converges on one stored plan. No SHA is accepted from the client.
	if _, ok := requireIdempotencyKey(w, r); !ok {
		return
	}
	decision, err := s.rescanner.Rescan(r.Context(), name)
	switch {
	case errors.Is(err, controller.ErrNotWatched):
		WriteControlProblem(w, r, executor.CodeStatus[executor.CodeFleetExecutionModeNotController], executor.CodeFleetExecutionModeNotController, "resource is not in controller execution mode")
	case err != nil:
		WriteControlProblem(w, r, http.StatusBadGateway, "fleet_converge_rescan_failed", "the branch tip could not be verified or the plan could not be recorded")
	default:
		writeJSON(w, decision)
	}
}

// Approve is POST plans/{planID}/converge/approve (platform:operate, non-CI,
// Idempotency-Key). Body {commitSha, resume?, sourceAttemptId?}; commitSha must
// equal the plan's commit.
func (s *FleetConvergeRoutes) Approve(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireTargetScope(w, r, ScopePlatformOperate, true); !ok {
		return
	}
	planID := chi.URLParam(r, "planID")
	if _, err := uuid.Parse(planID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest, "planID must be a UUID")
		return
	}
	key, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var request executor.ApproveRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest, err.Error())
		return
	}
	if err := request.Validate(); err != nil {
		writeExecutorError(w, r, err)
		return
	}
	plan, err := s.plans.ConvergePlan(r.Context(), planID)
	switch {
	case errors.Is(err, ErrConvergePlanNotFound):
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_plan_not_found", "fleet plan not found")
		return
	case errors.Is(err, ErrNotConvergePlan):
		WriteControlProblem(w, r, executor.CodeStatus[executor.CodeFleetPlanNotConverge], executor.CodeFleetPlanNotConverge, "the plan is not a converge plan")
		return
	case errors.Is(err, ErrConvergePlanInvalid):
		WriteControlProblem(w, r, http.StatusConflict, "fleet_plan_invalid", "converge plan failed integrity verification")
		return
	case err != nil:
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_plan_read_failed", "failed to read the plan")
		return
	}
	if plan.Converge.CommitSHA != request.CommitSHA {
		WriteControlProblem(w, r, executor.CodeStatus[executor.CodeFleetConvergeCommitMismatch], executor.CodeFleetConvergeCommitMismatch, "commitSha does not match the plan's commit")
		return
	}
	op, replayed, ok := s.approvals.RecordApproval(w, r, key, executor.ApproveAdmission{
		PlanID: planID, CommitSHA: request.CommitSHA, Resume: request.Resume, SourceAttemptID: request.SourceAttemptID,
	})
	if !ok {
		return
	}
	op.AttachReceipt()
	if replayed {
		writeJSON(w, op)
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusCreated, op)
}

// pgConvergeApprovals records fleet.converge.approve through the pipeline.
// Metadata approvedBy is the authenticated operation actor's subject (outside
// the request fingerprint); the resource view reports it as
// approvalPolicy.approvedBy.
type pgConvergeApprovals struct{ h *Handler }

func (a *pgConvergeApprovals) RecordApproval(w http.ResponseWriter, r *http.Request, key string, admission executor.ApproveAdmission) (model.Operation, bool, bool) {
	h := a.h
	enqueue, ok := h.pipelineEnqueueRequest(w, r, key, map[string]interface{}{
		"action": executor.KindConvergeApprove, "planId": admission.PlanID, "commitSha": admission.CommitSHA,
		"resume": admission.Resume, "sourceAttemptId": admission.SourceAttemptID,
	})
	if !ok {
		return model.Operation{}, false, false
	}
	encoded, _ := json.Marshal(admission)
	var payload map[string]interface{}
	_ = json.Unmarshal(encoded, &payload)
	now := time.Now().UTC()
	finished := now
	op := model.Operation{ID: uuid.NewString(), Kind: executor.KindConvergeApprove, Ref: admission.PlanID, Status: model.OperationSucceeded,
		Risk: "records an operator approval; no provider mutation performed", Source: "control-api", Message: "converge approval recorded",
		Payload: payload, Metadata: map[string]interface{}{"planId": admission.PlanID, "approvedBy": enqueue.Actor.Subject}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1}
	accepted, err := h.pipeline.QueueOperation(r.Context(), op, enqueue)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return model.Operation{}, false, false
	}
	return accepted.Operation, accepted.Replayed, true
}

// PGConvergePlanRecorder is the PG controller.ConvergePlanRecorder: it records
// through the existing signed fleet.capacity-plan operation path (system actor,
// since the watcher has no request) and reads plans back from the operations
// table. The etcd recorder is E6's.
type PGConvergePlanRecorder struct {
	h *Handler
	// Admissible, when set, reports whether the plan may still be admitted
	// (false after fleet_plan_revalidation_required). Nil means admissible.
	Admissible func(ctx context.Context, planID string) (bool, error)
}

// ConvergePlanRecorder returns the PG recorder.
func (h *Handler) ConvergePlanRecorder() *PGConvergePlanRecorder {
	return &PGConvergePlanRecorder{h: h}
}

func decodeConvergePlan(op *model.Operation) (*controller.ConvergeCapacityPlan, bool) {
	if op == nil || op.Kind != "fleet.capacity-plan" {
		return nil, false
	}
	encoded, err := json.Marshal(op.Payload)
	if err != nil {
		return nil, false
	}
	var plan controller.ConvergeCapacityPlan
	if json.Unmarshal(encoded, &plan) != nil || plan.Action != executor.ActionConverge || plan.Converge == nil || plan.ID != op.ID || plan.Converge.Validate() != nil {
		return nil, false
	}
	return &plan, true
}

func (p *PGConvergePlanRecorder) recorded(ctx context.Context, plan *controller.ConvergeCapacityPlan) (*controller.RecordedConverge, error) {
	admissible := true
	if p.Admissible != nil {
		var err error
		if admissible, err = p.Admissible(ctx, plan.ID); err != nil {
			return nil, err
		}
	}
	return &controller.RecordedConverge{PlanID: plan.ID, Digest: plan.Digest, Converge: *plan.Converge, Admissible: admissible}, nil
}

// NewestConvergePlan implements controller.ConvergePlanRecorder.
func (p *PGConvergePlanRecorder) NewestConvergePlan(ctx context.Context, resource string) (*controller.RecordedConverge, error) {
	if p.h == nil || p.h.db == nil {
		return nil, errors.New("durable operation storage is unavailable")
	}
	ops, err := p.h.db.ListOperations(ctx, store.OperationFilter{Kind: "fleet.capacity-plan", Ref: resource, Limit: 20})
	if err != nil {
		return nil, err
	}
	for i := range ops {
		if plan, ok := decodeConvergePlan(&ops[i]); ok && plan.Converge.Resource == resource {
			return p.recorded(ctx, plan)
		}
	}
	return nil, nil
}

// RecordConvergePlan implements controller.ConvergePlanRecorder.
func (p *PGConvergePlanRecorder) RecordConvergePlan(ctx context.Context, plan *controller.ConvergeCapacityPlan, key string) (*controller.RecordedConverge, error) {
	h := p.h
	if h == nil || h.pipeline == nil || h.operationStore == nil {
		return nil, errors.New("signed operation acceptance is unavailable")
	}
	authority, err := h.operationStore.Authority(ctx)
	if err != nil {
		return nil, err
	}
	const subject = "fleet-converge-watcher"
	enqueue := pipeline.EnqueueRequest{
		Authority: authority, Actor: store.OperationActor{Issuer: authority + "/system", Subject: subject}, Key: key,
		Audit: store.AcceptanceAuditContext{Source: subject},
		Semantics: map[string]interface{}{
			"action": "fleet.capacity-plan", "resource": plan.Converge.Resource, "commitSha": plan.Converge.CommitSHA, "baseCommitSha": plan.Converge.BaseCommitSHA,
		},
		// The watcher read executionMode before getting here; a switch to
		// github may have landed since. The acceptance transaction re-checks
		// the mode under the resource row lock, so the plan is either recorded
		// before the switch (which then abandons it) or not at all.
		RequireFleetResourceMode: executor.ModeController,
	}
	// A replayed key returns the first plan: the accepted intent covers the
	// operation's identity, so a second plan object under the same key would
	// conflict rather than replay.
	if replayed, found, err := p.resolve(ctx, enqueue, plan.Converge.Resource); err != nil {
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
	op := model.Operation{ID: plan.ID, Kind: "fleet.capacity-plan", Ref: plan.Converge.Resource, Status: model.OperationSucceeded,
		Risk: "records a converge plan for the controller-run executor; no provider mutation performed", Source: "control-api", Message: "converge plan recorded; no provider mutation performed",
		Payload: payload, Metadata: map[string]interface{}{"planId": plan.ID, "planDigest": plan.Digest, "signature": plan.Signature},
		StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1}
	accepted, err := h.pipeline.QueueOperation(ctx, op, enqueue)
	if err != nil {
		if replayed, found, _ := p.resolve(ctx, enqueue, plan.Converge.Resource); found {
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

func (p *PGConvergePlanRecorder) resolve(ctx context.Context, enqueue pipeline.EnqueueRequest, resource string) (*controller.RecordedConverge, bool, error) {
	accepted, err := p.h.pipeline.ResolveEnqueue(ctx, enqueue, "fleet.capacity-plan", resource)
	if errors.Is(err, store.ErrAcceptanceNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	stored, ok := decodeConvergePlan(&accepted.Operation)
	if !ok || !acceptedRequestMatches(accepted, enqueue) || accepted.Operation.Ref != resource {
		return nil, false, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "fleet.capacity-plan", Resource: resource}}
	}
	rec, err := p.recorded(ctx, stored)
	return rec, err == nil, err
}

// ConvergePlan implements ConvergePlanReader.
func (p *PGConvergePlanRecorder) ConvergePlan(ctx context.Context, planID string) (*controller.RecordedConverge, error) {
	if p.h == nil || p.h.db == nil {
		return nil, errors.New("durable operation storage is unavailable")
	}
	op, err := p.h.db.GetOperation(ctx, planID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConvergePlanNotFound
	}
	if err != nil {
		return nil, err
	}
	if op.Kind != "fleet.capacity-plan" {
		return nil, ErrConvergePlanNotFound
	}
	plan, ok := decodeConvergePlan(op)
	if !ok {
		return nil, ErrNotConvergePlan
	}
	// An approve binds to this plan, so it must verify like admission does.
	var keys []string
	if p.h.cfg != nil {
		keys = append([]string{p.h.cfg.AuditSigningKey}, p.h.cfg.AuditPreviousSigningKeys...)
	}
	if !controller.VerifyConvergePlan(plan, keys) {
		return nil, ErrConvergePlanInvalid
	}
	return p.recorded(ctx, plan)
}
