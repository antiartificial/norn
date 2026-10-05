package handler

// Fleet resource HTTP routes (plan.md WP13), shared by both backends and
// served only where the reconciler runs: the PG Fleet authority-only router
// and the etcd Fleet runtime (Q6).
//
// A resource is the one record that explains what was requested (desired),
// what exists (conditions and the watermarks of the observations behind
// them), what is running (status.active), what blocks progress (blocker) and
// which supported action comes next (status.nextAction), together with the
// approval policy Norn actually enforces. The stored status is a derived
// cache: every read re-applies the freshness rule (controller.DowngradeStale)
// against the current time and reports controller liveness separately,
// because an unchanged status is not rewritten (WP12).
//
// Authority rules:
//   - Creating a resource binds it to a registered target and needs
//     platform:operate, like target registration (Q7). The desired revision
//     is set with api:write, never by a CI identity. A client-supplied commit is never trusted while the GitHub
//     App can verify: the server resolves the merged plan's approved commit
//     (Q8). Only without the App is an explicit commitSha accepted, stored
//     and reported as operator-declared.
//   - Observations come only from a CI workload identity holding
//     fleet:operate with the fail-closed `observe` intent on a protected ref,
//     from the configured Fleet repository, whose GitHub environment resolves
//     through an `environment:` alias to the resource's target (Q3).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/githubapp"
	"norn/v2/api/store"
)

// FleetObserveIntent is the CI intent a workload must carry to append
// observations. Operators must add it to GitHubActionsFleetAllowedIntents;
// until they do the OIDC exchange never issues such a token and the route
// refuses everything (fail closed).
const FleetObserveIntent = "observe"

// fleetObservationBodyLimit bounds one observation request (facts are
// limited further to controller.MaxObservationFactsBytes).
const fleetObservationBodyLimit = 32 << 10

// Page sizes for GET resources and GET resources/{name}/observations.
const (
	fleetResourceDefaultLimit = 25
	fleetResourceMaxLimit     = 100
)

var fleetResourceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// FleetObservationPath reports whether path is exactly
// /api/v1/fleet/resources/{name}/observations: the only resource path a
// fleet:operate CI token reaches (the generic router check lets it through
// and the handler enforces the rest). Empty, "." or ".." segments, a trailing
// slash or extra segments never match.
func FleetObservationPath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/v1/fleet/resources/")
	if !ok {
		return false
	}
	name, tail, ok := strings.Cut(rest, "/")
	return ok && tail == "observations" && name != "" && name != "." && name != ".."
}

// Handler-level sentinels the backend adapters translate their store errors
// into, so the shared routes never import a backend's error values.
var (
	errFleetResourceNotFound         = errors.New("fleet resource not found")
	errFleetResourceRevisionConflict = errors.New("fleet resource revision conflict")
	errFleetResourceTargetMismatch   = errors.New("fleet resource bound to a different target")
	errFleetResourceInvalid          = errors.New("fleet resource request invalid")
	errFleetObservationBounds        = errors.New("fleet observation out of bounds")
)

// FleetApprovedPlanResolver is the GitHub App subset desired-revision
// resolution uses. *githubapp.Client implements it.
type FleetApprovedPlanResolver interface {
	ResolveApprovedPlan(ctx context.Context, planID, fleetEnvironment string) (*githubapp.Dispatch, error)
}

// FleetControllerLiveness reports the in-process reconciler's last completed
// rescan. *controller.Reconciler implements it.
type FleetControllerLiveness interface {
	LastRescan() (at time.Time, interval time.Duration)
}

// FleetResourceBackend is the per-backend adapter behind the shared routes.
// Implementations return the handler sentinels above for the cases the routes
// map to a stable problem code, and any other error as a store failure.
type FleetResourceBackend interface {
	GetTarget(ctx context.Context, targetID string) (*FleetTargetRecord, error)
	EnsureResource(ctx context.Context, name, targetID string) (*controller.Resource, error)
	GetResource(ctx context.Context, name string) (*controller.Resource, error)
	ListResourceNames(ctx context.Context, after string, limit int) ([]string, error)
	SetDesired(ctx context.Context, name string, expectedRevision int64, next controller.DesiredRevision) (*controller.Resource, error)
	AppendObservation(ctx context.Context, name, source string, observedAt time.Time, facts controller.ObservationFacts, evidenceRefs []string, reporter string) (*controller.Resource, controller.Observation, error)
	ListObservations(ctx context.Context, name string, limit int) ([]controller.Observation, error)
	// PlanTarget resolves a capacity plan's cluster, with the dispatch
	// lane's environment alias, to its registered target through the same
	// alias resolution admission uses (ResolveFleetTargetForPlan). found is
	// false when no such plan exists; targetID is "" when the registry is
	// empty. Unregistered or conflicting aliases return *lifecycle.FenceError.
	PlanTarget(ctx context.Context, planID, environment string) (targetID string, found bool, err error)
	// FleetEnvironment is the protected workflow environment plans resolve
	// under (the same lane check dispatch uses).
	FleetEnvironment() (string, *routeProblem)
}

// FleetResourceRoutes serves the resource routes for one backend.
type FleetResourceRoutes struct {
	backend  FleetResourceBackend
	cfg      *config.Config
	resolver FleetApprovedPlanResolver
	// resolverErr is set when the GitHub App is configured but unusable:
	// setting a desired revision then fails closed instead of falling back to
	// operator-declared.
	resolverErr error
	liveness    FleetControllerLiveness
	now         func() time.Time
}

// NewFleetResourceRoutes builds the shared routes. resolver is nil only when
// no GitHub App is configured (operator-declared commits are then accepted);
// liveness is nil when this process runs no reconciler.
func NewFleetResourceRoutes(backend FleetResourceBackend, cfg *config.Config, resolver FleetApprovedPlanResolver, resolverErr error, liveness FleetControllerLiveness) *FleetResourceRoutes {
	return &FleetResourceRoutes{backend: backend, cfg: cfg, resolver: resolver, resolverErr: resolverErr, liveness: liveness, now: time.Now}
}

// SetFleetControllerLiveness records the in-process reconciler so resource
// reads can report its liveness. Call it before FleetResourceRoutes.
func (h *Handler) SetFleetControllerLiveness(liveness FleetControllerLiveness) {
	h.fleetControllerLiveness = liveness
}

// FleetResourceRoutes returns the PostgreSQL routes for the authority-only
// router (Q6).
func (h *Handler) FleetResourceRoutes() *FleetResourceRoutes {
	var resolver FleetApprovedPlanResolver
	if h.fleetGitHub != nil {
		resolver = h.fleetGitHub
	}
	return NewFleetResourceRoutes(&pgFleetResourceBackend{h: h}, h.cfg, resolver, h.fleetGitHubConfigError, h.fleetControllerLiveness)
}

// NewEtcdFleetResourceRoutes returns the etcd routes. resolver may be nil
// (no GitHub App); environment names the protected workflow environment.
func NewEtcdFleetResourceRoutes(cfg *config.Config, operations *etcdstore.V3OperationStore, resolver FleetApprovedPlanResolver, environment func() (string, error), liveness FleetControllerLiveness) *FleetResourceRoutes {
	return NewFleetResourceRoutes(&etcdFleetResourceBackend{operations: operations, environment: environment}, cfg, resolver, nil, liveness)
}

// --- wire shapes ---

// FleetDesiredView is a desired revision on the wire.
type FleetDesiredView struct {
	Generation   int64      `json:"generation"`
	PlanID       string     `json:"planId,omitempty"`
	CommitSHA    string     `json:"commitSha,omitempty"`
	Repository   string     `json:"repository,omitempty"`
	Verification string     `json:"verification,omitempty"`
	AcceptedAt   *time.Time `json:"acceptedAt,omitempty"`
	AcceptedBy   string     `json:"acceptedBy,omitempty"`
}

// FleetConditionView is one derived readiness fact.
type FleetConditionView struct {
	Type                string     `json:"type"`
	Status              string     `json:"status"`
	Reason              string     `json:"reason,omitempty"`
	Message             string     `json:"message,omitempty"`
	ObservedAt          *time.Time `json:"observedAt,omitempty"`
	EvidenceRefs        []string   `json:"evidenceRefs"`
	ObservationSequence int64      `json:"observationSequence,omitempty"`
}

// FleetAppliedView is the commit the last succeeded release ran.
type FleetAppliedView struct {
	Generation int64  `json:"generation"`
	CommitSHA  string `json:"commitSha"`
	PlanID     string `json:"planId"`
	Reason     string `json:"reason,omitempty"`
}

// FleetActiveView names the plan, dispatch and attempts currently holding the
// target's fence (IDs only; the fence and attempt stores stay the truth).
type FleetActiveView struct {
	PlanID          string   `json:"planId"`
	DispatchRunID   string   `json:"dispatchRunId,omitempty"`
	AttemptIDs      []string `json:"attemptIds"`
	FenceGeneration int64    `json:"fenceGeneration"`
	Occupancy       string   `json:"occupancy"`
	OccupancyReason string   `json:"occupancyReason,omitempty"`
}

// FleetStatusView is the stored status with the freshness rule re-applied at
// read time.
type FleetStatusView struct {
	ObservedGeneration    int64                `json:"observedGeneration"`
	LastAppliedGeneration int64                `json:"lastAppliedGeneration"`
	LastApplied           *FleetAppliedView    `json:"lastApplied,omitempty"`
	Active                *FleetActiveView     `json:"active,omitempty"`
	Conditions            []FleetConditionView `json:"conditions"`
	NextAction            string               `json:"nextAction,omitempty"`
	EvaluatedAt           *time.Time           `json:"evaluatedAt,omitempty"`
}

// FleetBlockerView is the single thing most directly blocking progress, or
// absent when nothing is. Kind is execution, condition, reconciliation or
// observation.
type FleetBlockerView struct {
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	Message   string `json:"message,omitempty"`
	Condition string `json:"condition,omitempty"`
	PlanID    string `json:"planId,omitempty"`
}

// FleetWatermarkView is a source's latest applied observation.
type FleetWatermarkView struct {
	ObservedAt time.Time `json:"observedAt"`
	Sequence   int64     `json:"sequence"`
}

// FleetApprovalPolicyView is the approval policy Norn actually enforces,
// reported as data. Basis names the desired revision's verification the
// policy was derived for.
type FleetApprovalPolicyView struct {
	Basis                 string `json:"basis"`
	ProtectedBranch       bool   `json:"protectedBranch"`
	MergedPullRequest     bool   `json:"mergedPullRequest"`
	PlanWorkflowSucceeded bool   `json:"planWorkflowSucceeded"`
	BoundPlanDigest       bool   `json:"boundPlanDigest"`
	AuthorizedDispatch    bool   `json:"authorizedDispatch"`
	OwnerApprovalEnvelope bool   `json:"ownerApprovalEnvelope"`
	EnvironmentReviewers  string `json:"environmentReviewers"`
	IndependentReview     string `json:"independentReview"`
}

// FleetControllerView reports whether a controller is keeping the stored
// status current. State is running, stale (no completed rescan within three
// intervals) or unknown (this process runs no reconciler, or it has not
// completed a rescan yet).
type FleetControllerView struct {
	State                 string     `json:"state"`
	LastRescanAt          *time.Time `json:"lastRescanAt,omitempty"`
	RescanIntervalSeconds int64      `json:"rescanIntervalSeconds,omitempty"`
}

// FleetResourceView is GET resources/{name}.
type FleetResourceView struct {
	Name                string                        `json:"name"`
	TargetID            string                        `json:"targetId"`
	Revision            int64                         `json:"revision"`
	AuthorityEpoch      int64                         `json:"authorityEpoch"`
	ObservationSequence int64                         `json:"observationSequence"`
	CreatedAt           time.Time                     `json:"createdAt"`
	UpdatedAt           time.Time                     `json:"updatedAt"`
	Desired             FleetDesiredView              `json:"desired"`
	DesiredHistory      []FleetDesiredView            `json:"desiredHistory"`
	Status              FleetStatusView               `json:"status"`
	Blocker             *FleetBlockerView             `json:"blocker,omitempty"`
	Observed            map[string]FleetWatermarkView `json:"observed"`
	ApprovalPolicy      FleetApprovalPolicyView       `json:"approvalPolicy"`
	Controller          FleetControllerView           `json:"controller"`
	ReadAt              time.Time                     `json:"readAt"`
}

// FleetResourceList is GET resources.
type FleetResourceList struct {
	Items      []FleetResourceView `json:"items"`
	Next       string              `json:"next,omitempty"`
	Controller FleetControllerView `json:"controller"`
}

// FleetObservationView is one stored observation.
type FleetObservationView struct {
	Sequence     int64                       `json:"sequence"`
	Source       string                      `json:"source"`
	ObservedAt   time.Time                   `json:"observedAt"`
	ReceivedAt   time.Time                   `json:"receivedAt"`
	Facts        controller.ObservationFacts `json:"facts"`
	EvidenceRefs []string                    `json:"evidenceRefs"`
	Reporter     string                      `json:"reporter"`
	Applied      bool                        `json:"applied"`
}

// FleetObservationAppended is the POST observations response. Result is
// applied when the observation advanced its source's watermark and
// superseded when it was stored but an equal-or-newer one already applied.
type FleetObservationAppended struct {
	FleetObservationView
	Result string `json:"result"`
}

// FleetObservationList is GET resources/{name}/observations, newest first.
type FleetObservationList struct {
	Items []FleetObservationView `json:"items"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func nonNilRefs(refs []string) []string {
	if refs == nil {
		return []string{}
	}
	return refs
}

func desiredView(d controller.DesiredRevision) FleetDesiredView {
	return FleetDesiredView{Generation: d.Generation, PlanID: d.PlanID, CommitSHA: d.CommitSHA, Repository: d.Repository,
		Verification: d.Verification, AcceptedAt: timePtr(d.AcceptedAt), AcceptedBy: d.AcceptedBy}
}

func observationView(o controller.Observation) FleetObservationView {
	facts := o.Facts
	if facts == nil {
		facts = controller.ObservationFacts{}
	}
	return FleetObservationView{Sequence: o.Sequence, Source: o.Source, ObservedAt: o.ObservedAt.UTC(), ReceivedAt: o.ReceivedAt.UTC(),
		Facts: facts, EvidenceRefs: nonNilRefs(o.EvidenceRefs), Reporter: o.Reporter, Applied: o.Applied}
}

// FleetApprovalPolicy is the approval policy Norn actually enforces for a
// resource whose desired revision has the given verification (plan.md §1.5,
// Q8). The values are facts about the code paths, not configuration:
//
//   - github-merged-plan: the desired commit was resolved by
//     ResolveApprovedPlan, which requires a merged pull request, a succeeded
//     plan workflow on the protected default branch at the merge SHA and the
//     bound plan artifact digest.
//   - operator-declared (or no desired revision yet): no provenance control
//     ran on the declared commit, so none is claimed.
//
// authorizedDispatch is true only when this server has a usable GitHub App
// dispatch path; without one no authorized-dispatch control exists.
//
// The owner-signed approval envelope is enforced only on the external-Mac
// lane and so is not claimed for a resource. GitHub Environment reviewers
// and independent review are not enforced anywhere.
func FleetApprovalPolicy(verification string, authorizedDispatch bool) controller.ApprovalPolicy {
	policy := controller.ApprovalPolicy{AuthorizedDispatch: authorizedDispatch, EnvironmentReviewers: "not_enforced", IndependentReview: "not_enforced"}
	if verification == controller.VerificationGitHubMergedPlan {
		policy.ProtectedBranch, policy.MergedPullRequest, policy.PlanWorkflowSucceeded, policy.BoundPlanDigest = true, true, true, true
	}
	return policy
}

func approvalPolicyView(verification string, authorizedDispatch bool) FleetApprovalPolicyView {
	p := FleetApprovalPolicy(verification, authorizedDispatch)
	basis := verification
	if basis == "" {
		basis = "none"
	}
	return FleetApprovalPolicyView{Basis: basis, ProtectedBranch: p.ProtectedBranch, MergedPullRequest: p.MergedPullRequest,
		PlanWorkflowSucceeded: p.PlanWorkflowSucceeded, BoundPlanDigest: p.BoundPlanDigest, AuthorizedDispatch: p.AuthorizedDispatch,
		OwnerApprovalEnvelope: p.OwnerApprovalEnvelope, EnvironmentReviewers: p.EnvironmentReviewers, IndependentReview: p.IndependentReview}
}

func statusView(s controller.Status) FleetStatusView {
	view := FleetStatusView{ObservedGeneration: s.ObservedGeneration, LastAppliedGeneration: s.LastAppliedGeneration,
		Conditions: make([]FleetConditionView, 0, len(s.Conditions)), NextAction: s.NextAction, EvaluatedAt: timePtr(s.EvaluatedAt)}
	if s.LastApplied != nil {
		view.LastApplied = &FleetAppliedView{Generation: s.LastApplied.Generation, CommitSHA: s.LastApplied.CommitSHA, PlanID: s.LastApplied.PlanID, Reason: s.LastApplied.Reason}
	}
	if s.Active != nil {
		ids := s.Active.AttemptIDs
		if ids == nil {
			ids = []string{}
		}
		view.Active = &FleetActiveView{PlanID: s.Active.PlanID, DispatchRunID: s.Active.DispatchRunID, AttemptIDs: ids,
			FenceGeneration: s.Active.FenceGeneration, Occupancy: s.Active.Occupancy, OccupancyReason: s.Active.OccupancyReason}
	}
	for _, c := range s.Conditions {
		view.Conditions = append(view.Conditions, FleetConditionView{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message,
			ObservedAt: timePtr(c.ObservedAt), EvidenceRefs: nonNilRefs(c.EvidenceRefs), ObservationSequence: c.ObservationSequence})
	}
	return view
}

// fleetBlocker names what most directly blocks progress, from the already
// downgraded status. Priority: a held fence, then a condition that is False,
// then a ReconciliationRequired that is True, then an observation-backed
// condition that is Unknown (readiness cannot be claimed without it).
func fleetBlocker(s controller.Status) *FleetBlockerView {
	if s.Active != nil {
		reason := s.Active.OccupancyReason
		if reason == "" {
			reason = s.Active.Occupancy
		}
		return &FleetBlockerView{Kind: "execution", Reason: reason, PlanID: s.Active.PlanID,
			Message: "the target mutation fence is held by plan " + s.Active.PlanID + " (" + s.Active.Occupancy + ")"}
	}
	for _, c := range s.Conditions {
		if c.Type != controller.ConditionReconciliationRequired && c.Status == controller.StatusFalse {
			return &FleetBlockerView{Kind: "condition", Condition: c.Type, Reason: c.Reason, Message: c.Message}
		}
	}
	for _, c := range s.Conditions {
		if c.Type == controller.ConditionReconciliationRequired && c.Status == controller.StatusTrue {
			return &FleetBlockerView{Kind: "reconciliation", Condition: c.Type, Reason: c.Reason, Message: c.Message}
		}
	}
	for _, c := range s.Conditions {
		if c.Type != controller.ConditionReconciliationRequired && c.Status == controller.StatusUnknown {
			return &FleetBlockerView{Kind: "observation", Condition: c.Type, Reason: c.Reason, Message: c.Message}
		}
	}
	return nil
}

func (s *FleetResourceRoutes) controllerView(now time.Time) FleetControllerView {
	if s.liveness == nil {
		return FleetControllerView{State: "unknown"}
	}
	at, interval := s.liveness.LastRescan()
	if at.IsZero() {
		return FleetControllerView{State: "unknown", RescanIntervalSeconds: int64(interval / time.Second)}
	}
	state := "running"
	if now.Sub(at) > 3*interval {
		state = "stale"
	}
	return FleetControllerView{State: state, LastRescanAt: timePtr(at), RescanIntervalSeconds: int64(interval / time.Second)}
}

// resourceView builds the read response: the stored status with the
// freshness rule re-applied against now, the real approval policy and the
// controller's liveness.
func (s *FleetResourceRoutes) resourceView(resource *controller.Resource, now time.Time) FleetResourceView {
	status := controller.DowngradeStale(resource.Status, now)
	history := make([]FleetDesiredView, 0, len(resource.DesiredHistory))
	for _, entry := range resource.DesiredHistory {
		history = append(history, desiredView(entry))
	}
	observed := make(map[string]FleetWatermarkView, len(resource.Watermarks))
	for source, mark := range resource.Watermarks {
		observed[source] = FleetWatermarkView{ObservedAt: mark.ObservedAt.UTC(), Sequence: mark.Sequence}
	}
	return FleetResourceView{
		Name: resource.Name, TargetID: resource.TargetID, Revision: resource.Revision, AuthorityEpoch: resource.AuthorityEpoch,
		ObservationSequence: resource.ObservationSequence, CreatedAt: resource.CreatedAt.UTC(), UpdatedAt: resource.UpdatedAt.UTC(),
		Desired: desiredView(resource.Desired), DesiredHistory: history, Status: statusView(status), Blocker: fleetBlocker(status),
		Observed: observed, ApprovalPolicy: approvalPolicyView(resource.Desired.Verification, s.resolver != nil && s.resolverErr == nil),
		Controller: s.controllerView(now), ReadAt: now.UTC(),
	}
}

// --- handlers ---

func resourceName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := chi.URLParam(r, "name")
	if !fleetResourceNameRe.MatchString(name) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_resource_name", "resource name must be 1-128 lowercase letters, digits, '.', '_' or '-', starting with a letter or digit")
		return "", false
	}
	return name, true
}

func writeFleetResourceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errFleetResourceNotFound):
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_resource_not_found", "fleet resource not found")
	case errors.Is(err, errFleetResourceRevisionConflict):
		WriteControlProblem(w, r, http.StatusConflict, "fleet_resource_revision_conflict", "the resource changed since expectedRevision was read; read it again and retry")
	case errors.Is(err, errFleetResourceTargetMismatch):
		WriteControlProblem(w, r, http.StatusConflict, "fleet_resource_target_mismatch", "the resource is already bound to a different target")
	case errors.Is(err, errFleetObservationBounds):
		WriteControlProblem(w, r, http.StatusBadRequest, "fleet_observation_out_of_bounds", "the observation is outside the accepted time, size or evidence bounds")
	case errors.Is(err, errFleetResourceInvalid):
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_resource_request", "the request is not valid for a Fleet resource")
	default:
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_resource_store_failed", "the Fleet resource store could not complete the request")
	}
}

type fleetResourceCreateRequest struct {
	TargetID string `json:"targetId"`
}

// Create is POST resources/{name} (platform:operate, as target registration;
// Q7): idempotently bind name to a registered target. 201 when it created the
// resource, 200 when it existed.
func (s *FleetResourceRoutes) Create(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireTargetScope(w, r, ScopePlatformOperate, true); !ok {
		return
	}
	name, ok := resourceName(w, r)
	if !ok {
		return
	}
	var request fleetResourceCreateRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_resource_request", err.Error())
		return
	}
	if !fleetTargetIDRe.MatchString(request.TargetID) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_target_id", "targetId must be tgt_ followed by 64 lowercase hex digits")
		return
	}
	target, err := s.backend.GetTarget(r.Context(), request.TargetID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_target_read_failed", "fleet target could not be read")
		return
	}
	if target == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_target_not_found", "fleet target is not registered")
		return
	}
	created := false
	if _, err := s.backend.GetResource(r.Context(), name); errors.Is(err, errFleetResourceNotFound) {
		created = true
	} else if err != nil {
		writeFleetResourceError(w, r, err)
		return
	}
	resource, err := s.backend.EnsureResource(r.Context(), name, request.TargetID)
	if err != nil {
		writeFleetResourceError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", "/api/v1/fleet/resources/"+name)
	}
	writeJSONStatus(w, status, s.resourceView(resource, s.now().UTC()))
}

// Get is GET resources/{name} (api:read).
func (s *FleetResourceRoutes) Get(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireTargetScope(w, r, ScopeAPIRead, false); !ok {
		return
	}
	name, ok := resourceName(w, r)
	if !ok {
		return
	}
	resource, err := s.backend.GetResource(r.Context(), name)
	if err != nil {
		writeFleetResourceError(w, r, err)
		return
	}
	writeJSON(w, s.resourceView(resource, s.now().UTC()))
}

func pageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fleetResourceDefaultLimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > fleetResourceMaxLimit {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_resource_request", "limit must be an integer from 1 to "+strconv.Itoa(fleetResourceMaxLimit))
		return 0, false
	}
	return limit, true
}

// List is GET resources (api:read), paged by name: limit (default 25, at
// most 100) and after (the previous page's next).
func (s *FleetResourceRoutes) List(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireTargetScope(w, r, ScopeAPIRead, false); !ok {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	after := r.URL.Query().Get("after")
	if after != "" && !fleetResourceNameRe.MatchString(after) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_resource_name", "after must be a resource name")
		return
	}
	names, err := s.backend.ListResourceNames(r.Context(), after, limit)
	if err != nil {
		writeFleetResourceError(w, r, err)
		return
	}
	now := s.now().UTC()
	list := FleetResourceList{Items: make([]FleetResourceView, 0, len(names)), Controller: s.controllerView(now)}
	for _, name := range names {
		resource, err := s.backend.GetResource(r.Context(), name)
		if errors.Is(err, errFleetResourceNotFound) {
			continue
		}
		if err != nil {
			writeFleetResourceError(w, r, err)
			return
		}
		list.Items = append(list.Items, s.resourceView(resource, now))
	}
	if len(names) == limit {
		list.Next = names[len(names)-1]
	}
	writeJSON(w, list)
}

type fleetDesiredRequest struct {
	PlanID           string `json:"planId"`
	CommitSHA        string `json:"commitSha"`
	ExpectedRevision *int64 `json:"expectedRevision"`
}

// SetDesired is POST resources/{name}/desired (api:write, never a CI
// identity). The body is intent: a plan ID and the revision the operator
// read. The commit is resolved server-side from the merged plan (Q8); a
// client-supplied commitSha is refused whenever the GitHub App can verify.
func (s *FleetResourceRoutes) SetDesired(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireTargetScope(w, r, ScopeAPIWrite, true)
	if !ok {
		return
	}
	name, ok := resourceName(w, r)
	if !ok {
		return
	}
	var request fleetDesiredRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_resource_request", err.Error())
		return
	}
	if request.ExpectedRevision == nil || *request.ExpectedRevision < 0 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_resource_request", "expectedRevision is required and must not be negative")
		return
	}
	acceptedBy := strings.TrimSpace(principal.Subject)
	if acceptedBy == "" {
		acceptedBy = strings.TrimSpace(principal.TokenID)
	}
	if acceptedBy == "" {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "the principal does not establish a stable identity to record as acceptedBy")
		return
	}
	resource, err := s.backend.GetResource(r.Context(), name)
	if err != nil {
		writeFleetResourceError(w, r, err)
		return
	}
	if resource.Revision != *request.ExpectedRevision {
		writeFleetResourceError(w, r, errFleetResourceRevisionConflict)
		return
	}
	next := controller.DesiredRevision{AcceptedAt: s.now().UTC(), AcceptedBy: acceptedBy}
	if s.resolverErr != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_github_not_configured", "the Fleet GitHub App is configured but unusable; desired revisions cannot be verified")
		return
	}
	if s.resolver != nil {
		if problem := s.resolveMergedPlan(r.Context(), resource.TargetID, request, &next); problem != nil {
			WriteControlProblem(w, r, problem.status, problem.code, problem.detail)
			return
		}
	} else {
		// Without the GitHub App only an explicit commit is possible, and it
		// is labeled operator-declared (never protected-main provenance).
		if request.PlanID != "" {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_resource_request", "planId requires the Fleet GitHub App to verify it; declare commitSha instead")
			return
		}
		if !fleetCommitSHARe.MatchString(request.CommitSHA) {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_resource_request", "commitSha must be a 40-character lowercase hex SHA")
			return
		}
		next.CommitSHA, next.Verification = request.CommitSHA, controller.VerificationOperatorDeclared
	}
	updated, err := s.backend.SetDesired(r.Context(), name, *request.ExpectedRevision, next)
	if err != nil {
		writeFleetResourceError(w, r, err)
		return
	}
	writeJSON(w, s.resourceView(updated, s.now().UTC()))
}

// resolveMergedPlan fills next from the merged plan's approved commit. The
// plan's cluster must resolve, through the target alias resolution admission
// uses, to this resource's target; a plan for another cluster is refused
// before GitHub is consulted. A pending or unmerged proposal is refused;
// nothing the client sends reaches next.CommitSHA.
func (s *FleetResourceRoutes) resolveMergedPlan(ctx context.Context, resourceTargetID string, request fleetDesiredRequest, next *controller.DesiredRevision) *routeProblem {
	if request.CommitSHA != "" {
		return &routeProblem{http.StatusBadRequest, "invalid_fleet_resource_request", "commitSha is not accepted: the server resolves the merged plan's approved commit"}
	}
	planID := strings.TrimSpace(request.PlanID)
	if _, err := uuid.Parse(planID); err != nil {
		return &routeProblem{http.StatusBadRequest, "invalid_fleet_plan_id", "planId must be a UUID"}
	}
	repository := ""
	if s.cfg != nil {
		repository = strings.TrimSpace(s.cfg.FleetGitHubRepository)
	}
	if repository == "" {
		return &routeProblem{http.StatusServiceUnavailable, "fleet_github_not_configured", "the Fleet GitHub repository is not configured"}
	}
	environment, problem := s.backend.FleetEnvironment()
	if problem != nil {
		return problem
	}
	planTarget, found, err := s.backend.PlanTarget(ctx, planID, environment)
	var fenceErr *lifecycle.FenceError
	switch {
	case errors.As(err, &fenceErr):
		return &routeProblem{FleetTargetFenceStatus(fenceErr.Code), fenceErr.Code, "the plan's cluster does not resolve to a registered Fleet target"}
	case err != nil:
		return &routeProblem{http.StatusServiceUnavailable, "fleet_plan_read_failed", "fleet capacity plan could not be read"}
	case !found:
		return &routeProblem{http.StatusNotFound, "fleet_plan_not_found", "fleet capacity plan not found"}
	case planTarget != resourceTargetID:
		return &routeProblem{http.StatusConflict, "fleet_resource_plan_target_mismatch", "the plan's cluster resolves to a different Fleet target than this resource"}
	}
	approved, err := s.resolver.ResolveApprovedPlan(ctx, planID, environment)
	if errors.Is(err, githubapp.ErrNotReady) {
		return &routeProblem{http.StatusConflict, "fleet_github_plan_not_ready", "merge the fleet pull request and wait for its protected main-branch plan workflow to succeed"}
	}
	if err != nil {
		return &routeProblem{http.StatusBadGateway, "fleet_github_dispatch_failed", "GitHub could not resolve the protected fleet plan"}
	}
	if approved == nil || !fleetCommitSHARe.MatchString(approved.ApprovedHeadSHA) {
		return &routeProblem{http.StatusBadGateway, "fleet_github_plan_unproven", "GitHub could not prove the approved immutable fleet plan commit"}
	}
	next.PlanID, next.CommitSHA, next.Repository, next.Verification = planID, approved.ApprovedHeadSHA, repository, controller.VerificationGitHubMergedPlan
	return nil
}

// ListObservations is GET resources/{name}/observations (api:read), newest
// first, limit at most 100.
func (s *FleetResourceRoutes) ListObservations(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireTargetScope(w, r, ScopeAPIRead, false); !ok {
		return
	}
	name, ok := resourceName(w, r)
	if !ok {
		return
	}
	limit := fleetResourceMaxLimit
	if r.URL.Query().Get("limit") != "" {
		var ok bool
		if limit, ok = pageLimit(w, r); !ok {
			return
		}
	}
	if _, err := s.backend.GetResource(r.Context(), name); err != nil {
		writeFleetResourceError(w, r, err)
		return
	}
	observations, err := s.backend.ListObservations(r.Context(), name, limit)
	if err != nil {
		writeFleetResourceError(w, r, err)
		return
	}
	list := FleetObservationList{Items: make([]FleetObservationView, 0, len(observations))}
	for _, o := range observations {
		list.Items = append(list.Items, observationView(o))
	}
	writeJSON(w, list)
}

type fleetObservationRequest struct {
	Source       string                      `json:"source"`
	ObservedAt   *time.Time                  `json:"observedAt"`
	Facts        controller.ObservationFacts `json:"facts"`
	EvidenceRefs []string                    `json:"evidenceRefs"`
}

// observationIdentity checks, before any resource is read, that the
// principal is a GitHub Actions workload holding exactly fleet:operate with
// the observe intent on a protected ref, from the configured Fleet
// repository tuple. It returns the server-derived reporter.
func (s *FleetResourceRoutes) observationIdentity(principal AccessPrincipal) (string, bool) {
	ci := principal.CI
	if ci == nil || ci.Provider != "github-actions" || ci.Intent != FleetObserveIntent || !ci.RefProtected || ci.RunID == "" || ci.RunAttempt == "" || ci.Environment == "" {
		return "", false
	}
	if s.cfg == nil {
		return "", false
	}
	parts := strings.Split(strings.TrimSpace(s.cfg.GitHubActionsFleetAllowedRepository), "@")
	if len(parts) != 3 || parts[0] == "" || parts[0] != ci.Repository || parts[1] != ci.RepositoryID || parts[2] != ci.RepositoryOwnerID {
		return "", false
	}
	return "github-actions:" + ci.Repository + ":run/" + ci.RunID + "/" + ci.RunAttempt, true
}

// AppendObservation is POST resources/{name}/observations. Only a CI
// workload identity with fleet:operate and the observe intent may append, and
// its environment must resolve to the resource's target through an
// `environment:` alias (Q3). The reporter is derived from the verified
// identity; ObservedAt is bounded relative to server receive time.
func (s *FleetResourceRoutes) AppendObservation(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireFleetOperateScope(w, r)
	if !ok {
		return
	}
	reporter, ok := s.observationIdentity(principal)
	if !ok {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_observation_identity_denied", "observations require a GitHub Actions workload identity with the observe intent on a protected ref from the Fleet repository")
		return
	}
	name, ok := resourceName(w, r)
	if !ok {
		return
	}
	var request fleetObservationRequest
	if err := decodeControlJSONLimit(w, r, &request, fleetObservationBodyLimit); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_observation", err.Error())
		return
	}
	if !controller.ValidSource(request.Source) || request.ObservedAt == nil || request.ObservedAt.IsZero() {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_observation", "source must be provider, state or runtime and observedAt is required")
		return
	}
	if !controller.ValidateObservationStrings(request.EvidenceRefs, reporter) {
		WriteControlProblem(w, r, http.StatusBadRequest, "fleet_observation_out_of_bounds", "at most 20 evidence refs of at most 512 bytes are accepted")
		return
	}
	if encoded, err := json.Marshal(request.Facts); err != nil || len(encoded) > controller.MaxObservationFactsBytes {
		WriteControlProblem(w, r, http.StatusBadRequest, "fleet_observation_out_of_bounds", "facts must be at most 16 KiB of JSON")
		return
	}
	if !controller.ObservationWithinBounds(request.ObservedAt.UTC(), s.now().UTC()) {
		WriteControlProblem(w, r, http.StatusBadRequest, "fleet_observation_out_of_bounds", "observedAt must be within 24h before and 1m after the server receive time")
		return
	}
	resource, err := s.backend.GetResource(r.Context(), name)
	if err != nil {
		writeFleetResourceError(w, r, err)
		return
	}
	// Aliases have one meaning, environment:<lane> (plan §2.2). The token's
	// GitHub environment must be the one this server dispatches its lane
	// under (FleetGitHubEnvironment), and that lane's alias must name this
	// resource's target.
	lane, problem := s.backend.FleetEnvironment()
	if problem != nil {
		WriteControlProblem(w, r, problem.status, problem.code, problem.detail)
		return
	}
	if s.cfg == nil || s.cfg.FleetGitHubEnvironment == "" || principal.CI.Environment != s.cfg.FleetGitHubEnvironment {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_observation_identity_denied", "the workload environment is not the GitHub environment of this server's Fleet lane")
		return
	}
	target, err := s.backend.GetTarget(r.Context(), resource.TargetID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_target_read_failed", "fleet target could not be read")
		return
	}
	if target == nil || !hasAlias(target.Aliases, "environment:"+lane) {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_observation_identity_denied", "the workload's Fleet lane does not resolve to this resource's target")
		return
	}
	_, observation, err := s.backend.AppendObservation(r.Context(), name, request.Source, request.ObservedAt.UTC(), request.Facts, request.EvidenceRefs, reporter)
	if err != nil {
		writeFleetResourceError(w, r, err)
		return
	}
	result := "superseded"
	if observation.Applied {
		result = "applied"
	}
	writeJSONStatus(w, http.StatusCreated, FleetObservationAppended{FleetObservationView: observationView(observation), Result: result})
}

func hasAlias(aliases []string, want string) bool {
	for _, alias := range aliases {
		if alias == want {
			return true
		}
	}
	return false
}

// --- PostgreSQL adapter ---

type pgFleetResourceBackend struct{ h *Handler }

func mapFleetResourcePGError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrFleetResourceNotFound):
		return errFleetResourceNotFound
	case errors.Is(err, store.ErrFleetResourceRevisionConflict):
		return errFleetResourceRevisionConflict
	case errors.Is(err, store.ErrFleetResourceTargetMismatch):
		return errFleetResourceTargetMismatch
	case errors.Is(err, store.ErrFleetObservationOutOfBounds):
		return errFleetObservationBounds
	case errors.Is(err, store.ErrFleetResourceDesiredInvalid):
		return errFleetResourceInvalid
	}
	return err
}

func (b *pgFleetResourceBackend) GetTarget(ctx context.Context, targetID string) (*FleetTargetRecord, error) {
	return (&pgFleetTargetBackend{h: b.h}).GetTarget(ctx, targetID)
}
func (b *pgFleetResourceBackend) EnsureResource(ctx context.Context, name, targetID string) (*controller.Resource, error) {
	resource, err := b.h.db.EnsureFleetResource(ctx, name, targetID)
	return resource, mapFleetResourcePGError(err)
}
func (b *pgFleetResourceBackend) GetResource(ctx context.Context, name string) (*controller.Resource, error) {
	resource, err := b.h.db.GetFleetResource(ctx, name)
	return resource, mapFleetResourcePGError(err)
}
func (b *pgFleetResourceBackend) ListResourceNames(ctx context.Context, after string, limit int) ([]string, error) {
	return b.h.db.ListFleetResourceNames(ctx, after, limit)
}
func (b *pgFleetResourceBackend) SetDesired(ctx context.Context, name string, expectedRevision int64, next controller.DesiredRevision) (*controller.Resource, error) {
	resource, err := b.h.db.SetDesiredFleetResourceIf(ctx, name, expectedRevision, next)
	return resource, mapFleetResourcePGError(err)
}
func (b *pgFleetResourceBackend) AppendObservation(ctx context.Context, name, source string, observedAt time.Time, facts controller.ObservationFacts, evidenceRefs []string, reporter string) (*controller.Resource, controller.Observation, error) {
	resource, observation, err := b.h.db.AppendFleetObservation(ctx, name, source, observedAt, facts, evidenceRefs, reporter)
	return resource, observation, mapFleetResourcePGError(err)
}
func (b *pgFleetResourceBackend) ListObservations(ctx context.Context, name string, limit int) ([]controller.Observation, error) {
	return b.h.db.ListFleetObservations(ctx, name, limit)
}
func (b *pgFleetResourceBackend) PlanTarget(ctx context.Context, planID, environment string) (string, bool, error) {
	plan, err := b.h.db.GetOperation(ctx, planID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if plan.Kind != "fleet.capacity-plan" {
		return "", false, nil
	}
	cluster, _ := plan.Payload["cluster"].(string)
	targetID, _, err := b.h.db.ResolveFleetTargetForCluster(ctx, cluster, environment)
	return targetID, true, err
}
func (b *pgFleetResourceBackend) FleetEnvironment() (string, *routeProblem) {
	return b.h.matchingFleetEnvironment()
}

// --- etcd adapter ---

type etcdFleetResourceBackend struct {
	operations  *etcdstore.V3OperationStore
	environment func() (string, error)
}

func mapFleetResourceEtcdError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, etcdstore.ErrFleetResourceNotFound):
		return errFleetResourceNotFound
	case errors.Is(err, etcdstore.ErrFleetResourceRevisionConflict):
		return errFleetResourceRevisionConflict
	case errors.Is(err, etcdstore.ErrFleetResourceTargetMismatch):
		return errFleetResourceTargetMismatch
	case errors.Is(err, etcdstore.ErrFleetObservationOutOfBounds):
		return errFleetObservationBounds
	case errors.Is(err, etcdstore.ErrFleetResourceDesiredInvalid):
		return errFleetResourceInvalid
	}
	return err
}

func (b *etcdFleetResourceBackend) GetTarget(ctx context.Context, targetID string) (*FleetTargetRecord, error) {
	return (&etcdFleetTargetBackend{operations: b.operations}).GetTarget(ctx, targetID)
}
func (b *etcdFleetResourceBackend) EnsureResource(ctx context.Context, name, targetID string) (*controller.Resource, error) {
	resource, err := b.operations.EnsureFleetResource(ctx, name, targetID)
	return resource, mapFleetResourceEtcdError(err)
}
func (b *etcdFleetResourceBackend) GetResource(ctx context.Context, name string) (*controller.Resource, error) {
	resource, err := b.operations.GetFleetResource(ctx, name)
	return resource, mapFleetResourceEtcdError(err)
}
func (b *etcdFleetResourceBackend) ListResourceNames(ctx context.Context, after string, limit int) ([]string, error) {
	return b.operations.ListFleetResourceNames(ctx, after, limit)
}
func (b *etcdFleetResourceBackend) SetDesired(ctx context.Context, name string, expectedRevision int64, next controller.DesiredRevision) (*controller.Resource, error) {
	resource, err := b.operations.SetDesiredFleetResourceIf(ctx, name, expectedRevision, next)
	return resource, mapFleetResourceEtcdError(err)
}
func (b *etcdFleetResourceBackend) AppendObservation(ctx context.Context, name, source string, observedAt time.Time, facts controller.ObservationFacts, evidenceRefs []string, reporter string) (*controller.Resource, controller.Observation, error) {
	resource, observation, err := b.operations.AppendFleetObservation(ctx, name, source, observedAt, facts, evidenceRefs, reporter)
	return resource, observation, mapFleetResourceEtcdError(err)
}
func (b *etcdFleetResourceBackend) ListObservations(ctx context.Context, name string, limit int) ([]controller.Observation, error) {
	return b.operations.ListFleetObservations(ctx, name, limit)
}
func (b *etcdFleetResourceBackend) PlanTarget(ctx context.Context, planID, environment string) (string, bool, error) {
	plan, err := b.operations.GetOperation(ctx, planID)
	if errors.Is(err, etcdstore.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if plan.Kind != "fleet.capacity-plan" {
		return "", false, nil
	}
	cluster, _ := plan.Payload["cluster"].(string)
	targetID, _, _, err := b.operations.ResolveFleetTargetForPlan(ctx, cluster, environment)
	return targetID, true, err
}
func (b *etcdFleetResourceBackend) FleetEnvironment() (string, *routeProblem) {
	if b.environment == nil {
		return "", &routeProblem{http.StatusServiceUnavailable, "fleet_github_not_configured", "configured Fleet GitHub root is invalid"}
	}
	environment, err := b.environment()
	if err != nil {
		return "", &routeProblem{http.StatusServiceUnavailable, "fleet_github_not_configured", "configured Fleet GitHub root is invalid"}
	}
	return environment, nil
}
