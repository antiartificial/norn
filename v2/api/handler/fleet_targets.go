package handler

// Fleet target and fence HTTP routes (plan.md WP9b), shared by both backends.
//
// Request bodies carry intent only (B2). Evidence (GitHub run observations
// and the run-listing snapshot digest) is gathered server-side through the
// WP5 observers strictly after the scope check and the optional replay
// short-circuit, and reaches the store only as the evidence argument of
// store.NewFleetTargetMutationAcceptance. No request field can fill it.
//
// Authorization is enforced here, not in the store (WP9a): platform:operate
// for register, admin (no step-up, H3) for release and abandon-plan. Both
// backends run the same code; only the thin FleetTargetBackend adapters
// differ.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/githubapp"
	"norn/v2/api/store"
)

// FleetPlanRunnerScopedPath is the M17 matcher behind the router's deferral of
// the generic scope check: only plan-scoped runner attempt and reconciliation
// paths have handlers that own their own scope decision (fleet:operate plus a
// bound workload identity). A resource or target merely named attempts-* is
// not one and must face the generic check. The match is exact, segment by
// segment, on the registered route shapes ({planID}/attempts,
// {planID}/attempts/{attemptID}[/heartbeat|/advance|/cancel] and
// {planID}/reconciliations), so a suffix like attempts-foo, an empty, "." or
// ".." segment, a trailing slash or an extra (for example %2F-decoded)
// segment never defers.
func FleetPlanRunnerScopedPath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/v1/fleet/plans/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	switch len(parts) {
	case 2:
		return parts[1] == "attempts" || parts[1] == "reconciliations"
	case 3:
		return parts[1] == "attempts"
	case 4:
		return parts[1] == "attempts" && (parts[3] == "heartbeat" || parts[3] == "advance" || parts[3] == "cancel")
	}
	return false
}

var fleetTargetIDRe = regexp.MustCompile(`^tgt_[0-9a-f]{64}$`)

// fleetTargetObservationTimeout bounds all GitHub observation for one request.
const fleetTargetObservationTimeout = 60 * time.Second

// FleetTargetRunObserver is the WP5 observer subset the release routes use.
// *githubapp.Client implements it.
type FleetTargetRunObserver interface {
	ObserveApplyRunByNonceHash(ctx context.Context, planID, fleetEnvironment string, approved *githubapp.Dispatch, nonceHash string, runAttempt int64) (*githubapp.ApplyRunObservation, error)
	ObserveRecoverRun(ctx context.Context, boundApplyRunID int64, nonceHash string, runID, runAttempt int64, recordedHeadSHA string) (*githubapp.ApplyRunObservation, error)
	ListPlanRuns(ctx context.Context, planID string) ([]githubapp.RunSummary, error)
}

// FleetTargetRecord is a registered target's durable identity.
type FleetTargetRecord struct {
	TargetID                string    `json:"targetId"`
	Provider                string    `json:"provider"`
	ProviderAccount         string    `json:"providerAccount"`
	StateBackend            string    `json:"stateBackend"`
	CreatedAt               time.Time `json:"createdAt"`
	RegistrationOperationID string    `json:"registrationOperationId"`
	Aliases                 []string  `json:"aliases"`
}

// FleetTargetFenceView is the read-only fence and derived occupancy (M3). It
// never includes the holder's nonce hash.
type FleetTargetFenceView struct {
	TargetID              string                     `json:"targetId"`
	Generation            int64                      `json:"generation"`
	Held                  bool                       `json:"held"`
	HolderPlanID          string                     `json:"holderPlanId,omitempty"`
	AuthorityEpoch        int64                      `json:"authorityEpoch"`
	CurrentAuthorityEpoch int64                      `json:"currentAuthorityEpoch"`
	Revision              int64                      `json:"revision"`
	LastRelease           *FleetTargetLastReleaseDTO `json:"lastRelease,omitempty"`
	Occupancy             lifecycle.Occupancy        `json:"occupancy"`
	OccupancyReason       string                     `json:"occupancyReason,omitempty"`
}

// FleetTargetLastReleaseDTO is the last release recorded on a fence.
type FleetTargetLastReleaseDTO struct {
	PlanID string    `json:"planId"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// FleetTargetView is GET targets/{id}: identity, aliases, fence, occupancy.
type FleetTargetView struct {
	FleetTargetRecord
	Fence FleetTargetFenceView `json:"fence"`
}

// FleetTargetHolderBinding is everything the server needs about a holder plan
// to gather terminal evidence: the bound apply dispatch (RunID 0 when no run
// is bound), its nonce hash and lane, and the holder's attempts.
type FleetTargetHolderBinding struct {
	FleetEnvironment string
	NonceSHA256      string
	Dispatch         githubapp.Dispatch
	// BoundRunAttempt is the latest recorded attempt number of the bound
	// apply run (at least 1 once bound).
	BoundRunAttempt int64
	Attempts        []fleet.RunnerAttempt
}

// FleetTargetBackend is the per-backend adapter behind the shared routes.
type FleetTargetBackend interface {
	OperationStore() store.OperationStore
	// Actor resolves the stable operation actor and audit context for a
	// scope-checked principal, writing the problem response itself on refusal.
	Actor(w http.ResponseWriter, r *http.Request, principal AccessPrincipal) (store.OperationActor, store.AcceptanceAuditContext, bool)
	GetTarget(ctx context.Context, targetID string) (*FleetTargetRecord, error)
	FenceView(ctx context.Context, targetID string, now time.Time) (FleetTargetFenceView, error)
	PlanExists(ctx context.Context, planID string) (bool, error)
	HolderBinding(ctx context.Context, planID string) (FleetTargetHolderBinding, error)
}

// FleetTargetRoutes serves the target and fence routes for one backend.
type FleetTargetRoutes struct {
	backend  FleetTargetBackend
	observer FleetTargetRunObserver
}

// NewFleetTargetRoutes builds the shared routes. observer may be nil when no
// GitHub App is configured; release and abandon then answer 503 after the
// scope check.
func NewFleetTargetRoutes(backend FleetTargetBackend, observer FleetTargetRunObserver) *FleetTargetRoutes {
	return &FleetTargetRoutes{backend: backend, observer: observer}
}

// FleetTargetRoutes returns the PostgreSQL routes for the authority-only
// router (Q6).
func (h *Handler) FleetTargetRoutes() *FleetTargetRoutes {
	var observer FleetTargetRunObserver
	if h.fleetGitHub != nil {
		observer = h.fleetGitHub
	}
	return NewFleetTargetRoutes(&pgFleetTargetBackend{h: h}, observer)
}

// NewEtcdFleetTargetRoutes returns the etcd routes. observer may be nil.
func NewEtcdFleetTargetRoutes(cfg *config.Config, operations *etcdstore.V3OperationStore, observer FleetTargetRunObserver) *FleetTargetRoutes {
	return NewFleetTargetRoutes(&etcdFleetTargetBackend{cfg: cfg, operations: operations}, observer)
}

// fleetTargetFenceCodes lists every stable fence and release code the target
// routes can return. The mapping is deliberately explicit so a new code must
// be added here (and to the API contract) rather than fall through to a 5xx.
var fleetTargetFenceCodes = map[string]int{
	lifecycle.CodeFleetTargetExecutionOccupied:             http.StatusConflict,
	lifecycle.CodeFleetTargetUnregistered:                  http.StatusConflict,
	lifecycle.CodeFleetTargetAliasConflict:                 http.StatusConflict,
	lifecycle.CodeFleetTargetAuthoritySuperseded:           http.StatusConflict,
	lifecycle.CodeFleetPlanRevalidationRequired:            http.StatusConflict,
	lifecycle.CodeFleetTargetHolderAbandoned:               http.StatusConflict,
	lifecycle.CodeFleetTargetRecoveryRequiresStoppedSource: http.StatusConflict,
	lifecycle.CodeFleetTargetRegistrationInFlight:          http.StatusConflict,
	lifecycle.CodeFleetTargetFenceNotHeld:                  http.StatusConflict,
	lifecycle.CodeFleetTargetHasLiveAttempt:                http.StatusConflict,
	lifecycle.CodeFleetTargetTerminalProofIncomplete:       http.StatusConflict,
	lifecycle.CodeFleetTargetAbandonTooSoon:                http.StatusConflict,
	lifecycle.CodeFleetTargetAbandonSnapshotRequired:       http.StatusConflict,
	lifecycle.CodeFleetTargetReleaseEvidenceMismatch:       http.StatusConflict,
	store.CodeFleetTargetExpectedGenerationMismatch:        http.StatusConflict,
}

// FleetTargetFenceStatus maps a fence or release code to its stable HTTP
// status (409 for the whole family, including an unlisted future code).
func FleetTargetFenceStatus(code string) int {
	if status, ok := fleetTargetFenceCodes[code]; ok {
		return status
	}
	return http.StatusConflict
}

func writeFleetTargetError(w http.ResponseWriter, r *http.Request, err error) {
	var fenceErr *lifecycle.FenceError
	if errors.As(err, &fenceErr) {
		WriteControlProblem(w, r, FleetTargetFenceStatus(fenceErr.Code), fenceErr.Code, fenceErr.Error())
		return
	}
	var validation *store.AcceptanceValidationError
	if errors.As(err, &validation) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_target_request", validation.Reason)
		return
	}
	writeOperationAcceptanceError(w, r, err)
}

// requireTargetScope enforces an exact in-handler scope (M17). A CI workload
// identity never mutates targets, whatever scopes it carries.
func requireTargetScope(w http.ResponseWriter, r *http.Request, scope string, mutating bool) (AccessPrincipal, bool) {
	principal, ok := requireControlScope(w, r, scope)
	if !ok {
		return AccessPrincipal{}, false
	}
	if mutating && principal.CI != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "Fleet target mutations are not available to workload identities")
		return AccessPrincipal{}, false
	}
	return principal, true
}

type fleetTargetRegisterRequest struct {
	Provider        string   `json:"provider"`
	ProviderAccount string   `json:"providerAccount"`
	StateBackend    string   `json:"stateBackend"`
	Aliases         []string `json:"aliases"`
}

type fleetTargetReleaseRequest struct {
	Mode               string `json:"mode"`
	ExpectedGeneration *int64 `json:"expectedGeneration"`
	Reason             string `json:"reason"`
}

type fleetTargetAbandonPlanRequest struct {
	PlanID string `json:"planId"`
	Reason string `json:"reason"`
}

// Register is POST targets (platform:operate).
func (s *FleetTargetRoutes) Register(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireTargetScope(w, r, ScopePlatformOperate, true)
	if !ok {
		return
	}
	var request fleetTargetRegisterRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_target_request", err.Error())
		return
	}
	s.accept(w, r, principal, store.FleetTargetMutationAdmission{
		Kind: store.FleetTargetMutationRegister, Provider: request.Provider, ProviderAccount: request.ProviderAccount,
		StateBackend: request.StateBackend, Aliases: request.Aliases,
	}, nil)
}

// Release is POST targets/{id}/fence/release (admin; terminal or abandon).
func (s *FleetTargetRoutes) Release(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireTargetScope(w, r, ScopeAdmin, true)
	if !ok {
		return
	}
	targetID := chi.URLParam(r, "targetID")
	if !fleetTargetIDRe.MatchString(targetID) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_target_id", "target ID must be tgt_ followed by 64 lowercase hex digits")
		return
	}
	var request fleetTargetReleaseRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_target_request", err.Error())
		return
	}
	if request.ExpectedGeneration == nil || *request.ExpectedGeneration < 0 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_target_request", "expectedGeneration is required and must not be negative")
		return
	}
	admission := store.FleetTargetMutationAdmission{
		Kind: store.FleetTargetMutationRelease, TargetID: targetID,
		Release: &store.FleetTargetReleaseAdmission{Mode: request.Mode, ExpectedGeneration: *request.ExpectedGeneration, Reason: request.Reason},
	}
	s.accept(w, r, principal, admission, func(ctx context.Context) (*store.FleetTargetReleaseEvidence, *routeProblem) {
		target, err := s.backend.GetTarget(ctx, targetID)
		if err != nil {
			return nil, &routeProblem{http.StatusServiceUnavailable, "fleet_target_read_failed", "fleet target could not be read"}
		}
		if target == nil {
			return nil, &routeProblem{http.StatusNotFound, "fleet_target_not_found", "fleet target is not registered"}
		}
		fence, err := s.backend.FenceView(ctx, targetID, time.Now().UTC())
		if err != nil {
			return nil, &routeProblem{http.StatusServiceUnavailable, "fleet_target_read_failed", "fleet target fence could not be read"}
		}
		if fence.Generation != *request.ExpectedGeneration {
			return nil, &routeProblem{FleetTargetFenceStatus(store.CodeFleetTargetExpectedGenerationMismatch), store.CodeFleetTargetExpectedGenerationMismatch, "expected generation does not match the current fence generation"}
		}
		if !fence.Held {
			// The store refuses a free fence as not held before it reads any proof.
			return nil, nil
		}
		if request.Mode == string(lifecycle.ReleaseModeTerminal) {
			return s.terminalEvidence(ctx, fence.HolderPlanID)
		}
		return s.listingEvidence(ctx, fence.HolderPlanID)
	})
}

// AbandonPlan is POST targets/abandon-plan (admin; H7), keyed by plan so it
// also applies to plans on unregistered clusters.
func (s *FleetTargetRoutes) AbandonPlan(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireTargetScope(w, r, ScopeAdmin, true)
	if !ok {
		return
	}
	var request fleetTargetAbandonPlanRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_target_request", err.Error())
		return
	}
	planID := strings.TrimSpace(request.PlanID)
	if _, err := uuid.Parse(planID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "planId must be a UUID")
		return
	}
	admission := store.FleetTargetMutationAdmission{
		Kind: store.FleetTargetMutationAbandonPlan, PlanID: planID,
		Release: &store.FleetTargetReleaseAdmission{Mode: string(lifecycle.ReleaseModeAbandon), Reason: request.Reason},
	}
	s.accept(w, r, principal, admission, func(ctx context.Context) (*store.FleetTargetReleaseEvidence, *routeProblem) {
		exists, err := s.backend.PlanExists(ctx, planID)
		if err != nil {
			return nil, &routeProblem{http.StatusServiceUnavailable, "fleet_plan_read_failed", "fleet capacity plan could not be read"}
		}
		if !exists {
			return nil, &routeProblem{http.StatusNotFound, "fleet_plan_not_found", "fleet capacity plan not found"}
		}
		return s.listingEvidence(ctx, planID)
	})
}

// Get is GET targets/{id} (api:read): identity, aliases, fence, occupancy.
func (s *FleetTargetRoutes) Get(w http.ResponseWriter, r *http.Request) {
	targetID, ok := s.readTarget(w, r)
	if !ok {
		return
	}
	target, err := s.backend.GetTarget(r.Context(), targetID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_target_read_failed", "fleet target could not be read")
		return
	}
	if target == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_target_not_found", "fleet target is not registered")
		return
	}
	fence, err := s.backend.FenceView(r.Context(), targetID, time.Now().UTC())
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_target_read_failed", "fleet target fence could not be read")
		return
	}
	writeJSON(w, FleetTargetView{FleetTargetRecord: *target, Fence: fence})
}

// Fence is GET targets/{id}/fence (api:read): the fence and occupancy only.
func (s *FleetTargetRoutes) Fence(w http.ResponseWriter, r *http.Request) {
	targetID, ok := s.readTarget(w, r)
	if !ok {
		return
	}
	target, err := s.backend.GetTarget(r.Context(), targetID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_target_read_failed", "fleet target could not be read")
		return
	}
	if target == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "fleet_target_not_found", "fleet target is not registered")
		return
	}
	fence, err := s.backend.FenceView(r.Context(), targetID, time.Now().UTC())
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_target_read_failed", "fleet target fence could not be read")
		return
	}
	writeJSON(w, fence)
}

func (s *FleetTargetRoutes) readTarget(w http.ResponseWriter, r *http.Request) (string, bool) {
	if _, ok := requireTargetScope(w, r, ScopeAPIRead, false); !ok {
		return "", false
	}
	targetID := chi.URLParam(r, "targetID")
	if !fleetTargetIDRe.MatchString(targetID) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_target_id", "target ID must be tgt_ followed by 64 lowercase hex digits")
		return "", false
	}
	return targetID, true
}

type routeProblem struct {
	status int
	code   string
	detail string
}

// accept is the shared signed-operation flow after the scope check:
//  1. identity: actor = principal, key = Idempotency-Key;
//  2. optional replay (Resolve): a replay never touches GitHub;
//  3. gather server-side evidence (nil gather for register);
//  4. Accept with the evidence as its own argument.
func (s *FleetTargetRoutes) accept(w http.ResponseWriter, r *http.Request, principal AccessPrincipal, admission store.FleetTargetMutationAdmission, gather func(context.Context) (*store.FleetTargetReleaseEvidence, *routeProblem)) {
	w.Header().Set("Cache-Control", "no-store")
	if s == nil || s.backend == nil || s.backend.OperationStore() == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "durable signed operation acceptance is unavailable")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "a non-empty Idempotency-Key of at most 200 characters is required")
		return
	}
	actor, audit, ok := s.backend.Actor(w, r, principal)
	if !ok {
		return
	}
	audit.Scopes = append([]string(nil), principal.Scopes...)
	operations := s.backend.OperationStore()
	authority, err := operations.Authority(r.Context())
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "control authority is unavailable")
		return
	}
	acceptance, err := store.NewFleetTargetMutationAcceptance(authority, actor, key, audit, admission, nil)
	if err != nil {
		writeFleetTargetError(w, r, err)
		return
	}
	if prior, err := operations.Resolve(r.Context(), acceptance.Identity, acceptance.Fingerprint); err == nil {
		prior.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+prior.Operation.ID)
		writeJSONStatus(w, http.StatusOK, prior.Operation)
		return
	} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
		writeFleetTargetError(w, r, err)
		return
	}
	var evidence *store.FleetTargetReleaseEvidence
	if gather != nil {
		ctx, cancel := context.WithTimeout(r.Context(), fleetTargetObservationTimeout)
		var problem *routeProblem
		evidence, problem = gather(ctx)
		cancel()
		if problem != nil {
			WriteControlProblem(w, r, problem.status, problem.code, problem.detail)
			return
		}
		if evidence != nil {
			if acceptance, err = store.NewFleetTargetMutationAcceptance(authority, actor, key, audit, admission, evidence); err != nil {
				writeFleetTargetError(w, r, err)
				return
			}
		}
	}
	accepted, err := operations.Accept(r.Context(), acceptance)
	if err != nil {
		writeFleetTargetError(w, r, err)
		return
	}
	accepted.Operation.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	status := http.StatusCreated
	if accepted.Replayed {
		status = http.StatusOK
	}
	writeJSONStatus(w, status, accepted.Operation)
}

// terminalEvidence proves, by the WP5 observers, that the bound apply run and
// every distinct run that ever hosted a holder attempt are completed. A run
// that cannot be observed is simply absent from the proof: the store then
// refuses the release as incomplete.
func (s *FleetTargetRoutes) terminalEvidence(ctx context.Context, holderPlanID string) (*store.FleetTargetReleaseEvidence, *routeProblem) {
	if s.observer == nil {
		return nil, &routeProblem{http.StatusServiceUnavailable, "fleet_github_not_configured", "terminal release evidence requires the Fleet GitHub App"}
	}
	binding, err := s.backend.HolderBinding(ctx, holderPlanID)
	if err != nil {
		return nil, &routeProblem{http.StatusServiceUnavailable, "fleet_target_read_failed", "fleet holder dispatch could not be read"}
	}
	evidence := &store.FleetTargetReleaseEvidence{}
	if binding.Dispatch.RunID <= 0 {
		return evidence, nil
	}
	approved := binding.Dispatch
	boundAttempt := binding.BoundRunAttempt
	if boundAttempt < 1 {
		boundAttempt = 1
	}
	for _, attempt := range binding.Attempts {
		if _, runID, runAttempt, ok := parseFleetRunnerAttemptID(attempt.RunnerAttemptID); ok && runID == approved.RunID && runAttempt > boundAttempt {
			boundAttempt = runAttempt
		}
	}
	// The observer refuses unless boundAttempt is the run's latest attempt.
	if obs, err := s.observer.ObserveApplyRunByNonceHash(ctx, holderPlanID, binding.FleetEnvironment, &approved, binding.NonceSHA256, boundAttempt); err == nil && obs != nil && obs.Status == "completed" {
		evidence.BoundApplyRunCompleted = true
	}
	seen := map[string]bool{}
	for _, attempt := range binding.Attempts {
		id := attempt.RunnerAttemptID
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		_, runID, runAttempt, ok := parseFleetRunnerAttemptID(id)
		if !ok {
			continue
		}
		if runID == approved.RunID {
			// An attempt of the bound run has runAttempt <= boundAttempt. The
			// observer refuses a superseded attempt number, so an earlier
			// attempt is proven by the latest one: GitHub only re-runs a
			// completed run, so the latest attempt completed implies every
			// earlier attempt did.
			if evidence.BoundApplyRunCompleted {
				evidence.CompletedRunnerAttemptIDs = append(evidence.CompletedRunnerAttemptIDs, id)
			}
			continue
		}
		obs, obsErr := s.observer.ObserveRecoverRun(ctx, approved.RunID, binding.NonceSHA256, runID, runAttempt, attempt.CommitSHA)
		if obsErr == nil && obs != nil && obs.Status == "completed" {
			evidence.CompletedRunnerAttemptIDs = append(evidence.CompletedRunnerAttemptIDs, id)
		}
	}
	return evidence, nil
}

// listingEvidence records the run-listing snapshot digest (an empty listing
// is a recorded observation, never proof that no run exists) for the
// age-gated abandon modes.
func (s *FleetTargetRoutes) listingEvidence(ctx context.Context, planID string) (*store.FleetTargetReleaseEvidence, *routeProblem) {
	if s.observer == nil {
		return nil, &routeProblem{http.StatusServiceUnavailable, "fleet_github_not_configured", "abandon evidence requires the Fleet GitHub App"}
	}
	runs, err := s.observer.ListPlanRuns(ctx, planID)
	if err != nil {
		return nil, &routeProblem{http.StatusBadGateway, "fleet_target_observation_failed", "the GitHub run listing snapshot could not be observed"}
	}
	if runs == nil {
		runs = []githubapp.RunSummary{}
	}
	encoded, err := json.Marshal(runs)
	if err != nil {
		return nil, &routeProblem{http.StatusInternalServerError, "fleet_target_observation_failed", "the GitHub run listing snapshot could not be encoded"}
	}
	sum := sha256.Sum256(encoded)
	return &store.FleetTargetReleaseEvidence{ListingSnapshotSHA256: hex.EncodeToString(sum[:])}, nil
}

// pgFleetTargetBackend adapts the PostgreSQL store (legacy live path).
type pgFleetTargetBackend struct{ h *Handler }

func (b *pgFleetTargetBackend) OperationStore() store.OperationStore {
	if b.h == nil || b.h.db == nil || b.h.operationStore == nil {
		return nil
	}
	return b.h.operationStore
}

func (b *pgFleetTargetBackend) Actor(w http.ResponseWriter, r *http.Request, principal AccessPrincipal) (store.OperationActor, store.AcceptanceAuditContext, bool) {
	requestContext, ok := operationAcceptanceRequestContextFromRequest(r)
	if !ok || requestContext.ReceiptID == "" {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "durable signed operation acceptance is unavailable")
		return store.OperationActor{}, store.AcceptanceAuditContext{}, false
	}
	if requestContext.ActorErr != nil || requestContext.Actor.Issuer == "" || requestContext.Actor.Subject == "" {
		WriteControlProblem(w, r, http.StatusConflict, "operation_actor_ambiguous", "the authenticated credential does not establish a stable operation actor")
		return store.OperationActor{}, store.AcceptanceAuditContext{}, false
	}
	return store.OperationActor{Issuer: requestContext.Actor.Issuer, Subject: requestContext.Actor.Subject}, store.AcceptanceAuditContext{
		RequestReceiptID: requestContext.ReceiptID, RequestID: requestContext.RequestID,
		CredentialID: requestContext.Actor.CredentialID, DeviceID: requestContext.Actor.DeviceID, Source: requestContext.Actor.Source,
	}, true
}

func (b *pgFleetTargetBackend) GetTarget(ctx context.Context, targetID string) (*FleetTargetRecord, error) {
	target, err := b.h.db.GetFleetTarget(ctx, targetID)
	if err != nil || target == nil {
		return nil, err
	}
	return &FleetTargetRecord{TargetID: target.TargetID, Provider: target.Provider, ProviderAccount: target.ProviderAccount, StateBackend: target.StateBackend,
		CreatedAt: target.CreatedAt, RegistrationOperationID: target.RegistrationOperationID, Aliases: nonNilStrings(target.Aliases)}, nil
}

func (b *pgFleetTargetBackend) FenceView(ctx context.Context, targetID string, now time.Time) (FleetTargetFenceView, error) {
	tx, err := b.h.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return FleetTargetFenceView{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	fence, err := store.GetFleetTargetFence(ctx, tx, targetID, false)
	if err != nil {
		return FleetTargetFenceView{}, err
	}
	epoch, err := store.FleetAuthorityEpoch(ctx, tx)
	if err != nil {
		return FleetTargetFenceView{}, err
	}
	holder, err := store.FleetTargetHolderFacts(ctx, tx, fence.HolderPlanID)
	if err != nil {
		return FleetTargetFenceView{}, err
	}
	occupancy, reason := lifecycle.Outcome(fence, holder, epoch, now)
	return newFleetTargetFenceView(fence, epoch, occupancy, reason), nil
}

func (b *pgFleetTargetBackend) PlanExists(ctx context.Context, planID string) (bool, error) {
	plan, err := b.h.db.GetOperation(ctx, planID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return plan.Kind == "fleet.capacity-plan", nil
}

func (b *pgFleetTargetBackend) HolderBinding(ctx context.Context, planID string) (FleetTargetHolderBinding, error) {
	var binding FleetTargetHolderBinding
	dispatch, err := b.h.db.GetFleetGitHubDispatch(ctx, planID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return binding, err
	default:
		binding.FleetEnvironment, binding.NonceSHA256 = dispatch.FleetEnvironment, dispatch.DispatchNonceSHA256
		binding.BoundRunAttempt = int64(dispatch.RunAttempt)
		binding.Dispatch = githubapp.Dispatch{RunID: dispatch.RunID, PlanRunID: dispatch.PlanRunID, PlanSHA: dispatch.PlanSHA256, ApprovedHeadSHA: dispatch.ApprovedHeadSHA, PilotRunID: dispatch.PilotRunID}
	}
	tx, err := b.h.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return binding, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	holder, err := store.FleetTargetHolderFacts(ctx, tx, planID)
	if err != nil {
		return binding, err
	}
	binding.Attempts = holder.Attempts
	return binding, nil
}

// etcdFleetTargetBackend adapts the etcd store.
type etcdFleetTargetBackend struct {
	cfg        *config.Config
	operations *etcdstore.V3OperationStore
}

func (b *etcdFleetTargetBackend) OperationStore() store.OperationStore {
	if b.operations == nil {
		return nil
	}
	return b.operations
}

func (b *etcdFleetTargetBackend) Actor(w http.ResponseWriter, r *http.Request, principal AccessPrincipal) (store.OperationActor, store.AcceptanceAuditContext, bool) {
	if principal.Source != AccessPrincipalSourceManagedToken || principal.TokenID == "" || principal.CI != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "Fleet target mutations require a non-CI managed token")
		return store.OperationActor{}, store.AcceptanceAuditContext{}, false
	}
	return store.OperationActor{Issuer: "norn://managed-token", Subject: principal.TokenID}, store.AcceptanceAuditContext{
		RequestReceiptID: uuid.NewString(), RequestID: middleware.GetReqID(r.Context()),
		CredentialID: principal.TokenID, DeviceID: principal.DeviceID, Source: "etcd-normal-fleet",
	}, true
}

func (b *etcdFleetTargetBackend) GetTarget(ctx context.Context, targetID string) (*FleetTargetRecord, error) {
	target, err := b.operations.GetFleetTarget(ctx, targetID)
	if err != nil || target == nil {
		return nil, err
	}
	return &FleetTargetRecord{TargetID: target.TargetID, Provider: target.Provider, ProviderAccount: target.ProviderAccount, StateBackend: target.StateBackend,
		CreatedAt: target.CreatedAt, RegistrationOperationID: target.RegistrationOperationID, Aliases: nonNilStrings(target.Aliases)}, nil
}

func (b *etcdFleetTargetBackend) FenceView(ctx context.Context, targetID string, now time.Time) (FleetTargetFenceView, error) {
	fence, _, err := b.operations.GetFleetTargetFence(ctx, targetID)
	if err != nil {
		return FleetTargetFenceView{}, err
	}
	epoch, err := b.operations.FleetAuthorityEpoch(ctx)
	if err != nil {
		return FleetTargetFenceView{}, err
	}
	occupancy, reason, err := b.operations.FleetTargetOutcome(ctx, targetID, now)
	if err != nil {
		return FleetTargetFenceView{}, err
	}
	return newFleetTargetFenceView(fence, epoch, occupancy, reason), nil
}

func (b *etcdFleetTargetBackend) PlanExists(ctx context.Context, planID string) (bool, error) {
	plan, err := b.operations.GetOperation(ctx, planID)
	if errors.Is(err, etcdstore.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return plan.Kind == "fleet.capacity-plan", nil
}

func (b *etcdFleetTargetBackend) HolderBinding(ctx context.Context, planID string) (FleetTargetHolderBinding, error) {
	var binding FleetTargetHolderBinding
	preparation, err := b.operations.GetFleetGitHubDispatchPreparation(ctx, planID)
	switch {
	case errors.Is(err, etcdstore.ErrNotFound):
		return binding, nil
	case err != nil:
		return binding, err
	}
	binding.FleetEnvironment, binding.NonceSHA256 = preparation.FleetEnvironment, preparation.DispatchNonceSHA256
	runBinding, err := b.operations.GetFleetRunnerDispatchBinding(ctx, planID)
	switch {
	case errors.Is(err, etcdstore.ErrNotFound):
	case err != nil:
		return binding, err
	default:
		pilot := ""
		if b.cfg != nil {
			pilot = b.cfg.FleetGitHubPilotRunID
		}
		binding.BoundRunAttempt = 1
		binding.Dispatch = githubapp.Dispatch{RunID: runBinding.RunID, PlanRunID: preparation.PlanRunID, PlanSHA: preparation.PlanSHA256, ApprovedHeadSHA: preparation.ApprovedHeadSHA, PilotRunID: pilot}
	}
	attempts, err := b.operations.ListFleetRunnerAttempts(ctx, planID)
	if err != nil {
		return binding, err
	}
	binding.Attempts = attempts
	return binding, nil
}

func newFleetTargetFenceView(fence lifecycle.FenceFacts, epoch int64, occupancy lifecycle.Occupancy, reason string) FleetTargetFenceView {
	view := FleetTargetFenceView{
		TargetID: fence.TargetID, Generation: fence.Generation, Held: fence.Held, HolderPlanID: fence.HolderPlanID,
		AuthorityEpoch: fence.AuthorityEpoch, CurrentAuthorityEpoch: epoch, Revision: fence.Revision,
		Occupancy: occupancy, OccupancyReason: reason,
	}
	if fence.LastRelease != nil {
		view.LastRelease = &FleetTargetLastReleaseDTO{PlanID: fence.LastRelease.PlanID, Reason: fence.LastRelease.Reason, At: fence.LastRelease.At}
	}
	return view
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
