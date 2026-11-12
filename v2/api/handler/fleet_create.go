package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/fleetcreate"
)

const (
	fleetCreateVerifierSubject = "fleet-create-verifier:work-staging"
	fleetCreateAttemptSubject  = "fleet-create-attempt:work-staging"
)

type FleetCreateIntentStore interface {
	RecordFleetCreateIntent(context.Context, fleetcreate.Intent, string) error
	GetFleetCreateIntent(context.Context, string) (fleetcreate.Intent, error)
}

type FleetCreateDispatchStore interface {
	RecordFleetCreateDispatch(context.Context, fleetcreate.DispatchBinding, string) error
	GetFleetCreateDispatch(context.Context, string) (fleetcreate.DispatchBinding, error)
}

type FleetCreateAttemptStore interface {
	GetFleetCreateIntent(context.Context, string) (fleetcreate.Intent, error)
	GetFleetCreateDispatch(context.Context, string) (fleetcreate.DispatchBinding, error)
	StartFleetCreateAttempt(context.Context, fleetcreate.AttemptStartRequest, string) (fleetcreate.Attempt, error)
	GetFleetCreateAttempt(context.Context, string, string, string) (fleetcreate.Attempt, error)
}

type FleetCreateAdmissionStore interface {
	RecordFleetCreateAdmission(context.Context, fleetcreate.Admission, string) error
	GetFleetCreateAdmission(context.Context, string, string, string) (fleetcreate.Admission, error)
}

type FleetCreateRecoveryStore interface {
	RecordFleetCreateRecoveryReadback(context.Context, fleetcreate.RecoveryReadback, string) error
}

type FleetCreateRecoveryAttemptStore interface {
	GetFleetCreateIntent(context.Context, string) (fleetcreate.Intent, error)
	GetFleetCreateDispatch(context.Context, string) (fleetcreate.DispatchBinding, error)
	StartFleetCreateRecoveryAttempt(context.Context, fleetcreate.AttemptStartRequest, string, string) (fleetcreate.Attempt, error)
}

type FleetCreateTerminalStore interface {
	RecordFleetCreateTerminalReceipt(context.Context, fleetcreate.TerminalReceipt, string) error
	GetFleetCreateTerminalReceipt(context.Context, string) (fleetcreate.TerminalReceipt, error)
	RegisterFleetCreateTerminalTarget(context.Context, fleetcreate.TerminalReceipt) (*etcdstore.FleetTarget, error)
}

type FleetCreateGrantStore interface {
	GetFleetCreateAdmission(context.Context, string, string, string) (fleetcreate.Admission, error)
	IssueFleetCreateEffectGrant(context.Context, fleetcreate.Request) (fleetcreate.Grant, error)
	GetFleetCreateEffectGrant(context.Context, string, string) (fleetcreate.Grant, bool, error)
	ConsumeFleetCreateEffectGrant(context.Context, fleetcreate.Request) (fleetcreate.Grant, error)
}

type FleetCreateGrantConsumeStore interface {
	GetFleetCreateDispatch(context.Context, string) (fleetcreate.DispatchBinding, error)
	GetFleetCreateIntent(context.Context, string) (fleetcreate.Intent, error)
	GetFleetCreateAttempt(context.Context, string, string, string) (fleetcreate.Attempt, error)
	GetFleetCreateAdmission(context.Context, string, string, string) (fleetcreate.Admission, error)
	ConsumeFleetCreateEffectGrant(context.Context, fleetcreate.Request) (fleetcreate.Grant, error)
}

type FleetCreateGrantStatusStore interface {
	GetFleetCreateDispatch(context.Context, string) (fleetcreate.DispatchBinding, error)
	GetFleetCreateIntent(context.Context, string) (fleetcreate.Intent, error)
	GetFleetCreateAttempt(context.Context, string, string, string) (fleetcreate.Attempt, error)
	GetFleetCreateAdmission(context.Context, string, string, string) (fleetcreate.Admission, error)
	GetFleetCreateEffectGrant(context.Context, string, string) (fleetcreate.Grant, bool, error)
}

func fleetCreateVerifierPrincipal(r *http.Request) (AccessPrincipal, bool) {
	p, ok := AccessPrincipalFromRequest(r)
	return p, ok && p.Source == AccessPrincipalSourceManagedToken && !p.Legacy && p.CI == nil && p.DeviceID == "" &&
		p.Subject == fleetCreateVerifierSubject && p.TokenID != "" && len(p.Scopes) == 1 && p.Scopes[0] == ScopeFleetCreateVerify &&
		!p.ExpiresAt.IsZero() && time.Now().Before(p.ExpiresAt)
}

// FleetCreateIntentHandler records and reads the immutable pre-registration
// intent through the independent work-staging verifier identity.
func FleetCreateIntentHandler(store FleetCreateIntentStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		principal, ok := fleetCreateVerifierPrincipal(r)
		if !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_verifier_required", "dedicated work-staging Fleet create verifier is required")
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_create_store_unavailable", "Fleet create intent store is unavailable")
			return
		}
		cluster := chi.URLParam(r, "cluster")
		if r.Method == http.MethodGet {
			intent, err := store.GetFleetCreateIntent(r.Context(), cluster)
			if errors.Is(err, etcdstore.ErrNotFound) {
				WriteControlProblem(w, r, http.StatusNotFound, "fleet_create_intent_not_found", "pre-registration create intent was not found")
				return
			}
			if err != nil {
				WriteControlProblem(w, r, http.StatusConflict, "fleet_create_intent_refused", "pre-registration create intent could not be verified")
				return
			}
			writeJSON(w, intent)
			return
		}
		var intent fleetcreate.Intent
		if err := decodeControlJSON(w, r, &intent); err != nil || intent.Validate() != nil || intent.Cluster != cluster {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_create_intent", "valid create intent must match the cluster path")
			return
		}
		if err := store.RecordFleetCreateIntent(r.Context(), intent, principal.Subject); err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_intent_refused")
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

// FleetCreateDispatchBindingHandler records verifier-read GitHub dispatch
// identity and provides its exact immutable readback to that verifier.
func FleetCreateDispatchBindingHandler(store FleetCreateDispatchStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		principal, ok := fleetCreateVerifierPrincipal(r)
		if !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_verifier_required", "dedicated work-staging Fleet create verifier is required")
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_create_store_unavailable", "Fleet create dispatch store is unavailable")
			return
		}
		intentID, cluster := chi.URLParam(r, "intentID"), chi.URLParam(r, "cluster")
		if r.Method == http.MethodGet {
			binding, err := store.GetFleetCreateDispatch(r.Context(), intentID)
			if errors.Is(err, etcdstore.ErrNotFound) {
				WriteControlProblem(w, r, http.StatusNotFound, "fleet_create_dispatch_not_found", "verified workflow dispatch was not bound")
				return
			}
			if err != nil || binding.Cluster != cluster {
				WriteControlProblem(w, r, http.StatusConflict, "fleet_create_dispatch_mismatch", "dispatch binding does not match the route")
				return
			}
			writeJSON(w, binding)
			return
		}
		var binding fleetcreate.DispatchBinding
		if err := decodeControlJSON(w, r, &binding); err != nil || binding.Validate() != nil ||
			binding.IntentID != intentID || binding.Cluster != cluster {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_create_dispatch", "valid exact dispatch binding must match the intent and cluster path")
			return
		}
		if err := store.RecordFleetCreateDispatch(r.Context(), binding, principal.Subject); err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_dispatch_refused")
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

// FleetCreateAttemptHandler admits an initial attempt only when the verified
// GitHub OIDC identity is exactly the immutable verifier-bound dispatch.
func FleetCreateAttemptHandler(store FleetCreateAttemptStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		principal, ok := fleetCreateAttemptPrincipal(w, r)
		if !ok {
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_create_store_unavailable", "Fleet create attempt store is unavailable")
			return
		}
		cluster, intentID := chi.URLParam(r, "cluster"), chi.URLParam(r, "intentID")
		if r.Method == http.MethodGet {
			attemptID := chi.URLParam(r, "attemptID")
			attempt, err := store.GetFleetCreateAttempt(r.Context(), cluster, intentID, attemptID)
			if errors.Is(err, etcdstore.ErrNotFound) {
				WriteControlProblem(w, r, http.StatusNotFound, "fleet_create_attempt_not_found", "pre-registration attempt was not found")
				return
			}
			if err != nil {
				WriteControlProblem(w, r, http.StatusConflict, "fleet_create_attempt_refused", "pre-registration attempt could not be verified")
				return
			}
			binding, bindErr := store.GetFleetCreateDispatch(r.Context(), intentID)
			if bindErr != nil || !fleetCreateOIDCMatchesDispatch(principal.CI, binding) || attempt.RunnerAttemptID != binding.RunnerAttemptID() {
				WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_dispatch_identity_mismatch", "runner identity does not match the verifier-bound protected dispatch")
				return
			}
			writeJSON(w, attempt)
			return
		}
		var request fleetcreate.AttemptStartRequest
		if err := decodeControlJSON(w, r, &request); err != nil || request.Validate() != nil ||
			request.Cluster != cluster || request.IntentID != intentID {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_create_attempt", "valid attempt start must match the intent and cluster path")
			return
		}
		binding, err := store.GetFleetCreateDispatch(r.Context(), intentID)
		if err != nil || !binding.MatchesStart(request) || !fleetCreateOIDCMatchesDispatch(principal.CI, binding) {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_dispatch_identity_mismatch", "runner identity does not match the verifier-bound protected dispatch")
			return
		}
		intent, err := store.GetFleetCreateIntent(r.Context(), cluster)
		if err != nil || intent.IntentID != intentID || intent.PlanID != request.PlanID {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_create_intent_mismatch", "attempt does not match the saved create intent")
			return
		}
		attempt, err := store.StartFleetCreateAttempt(r.Context(), request, fleetCreateAttemptSubject)
		if err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_attempt_refused")
			return
		}
		writeJSONStatus(w, http.StatusCreated, attempt)
	}
}

// FleetCreateAdmissionHandler records and reads the verifier's exact provider
// and state readback for one attempt. This grants no provider effect.
func FleetCreateAdmissionHandler(store FleetCreateAdmissionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		principal, ok := fleetCreateVerifierPrincipal(r)
		if !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_verifier_required", "dedicated work-staging Fleet create verifier is required")
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_create_store_unavailable", "Fleet create admission store is unavailable")
			return
		}
		cluster, intentID, attemptID := chi.URLParam(r, "cluster"), chi.URLParam(r, "intentID"), chi.URLParam(r, "attemptID")
		if r.Method == http.MethodGet {
			admission, err := store.GetFleetCreateAdmission(r.Context(), cluster, intentID, attemptID)
			if errors.Is(err, etcdstore.ErrNotFound) {
				WriteControlProblem(w, r, http.StatusNotFound, "fleet_create_admission_not_found", "verifier admission was not found")
				return
			}
			if err != nil {
				WriteControlProblem(w, r, http.StatusConflict, "fleet_create_admission_refused", "verifier admission could not be verified")
				return
			}
			writeJSON(w, admission)
			return
		}
		var admission fleetcreate.Admission
		if err := decodeControlJSON(w, r, &admission); err != nil || admission.Validate() != nil ||
			admission.Cluster != cluster || admission.IntentID != intentID || admission.AttemptID != attemptID {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_create_admission", "exact admission must match the cluster, intent, and attempt path")
			return
		}
		if err := store.RecordFleetCreateAdmission(r.Context(), admission, principal.Subject); err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_admission_refused")
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

// FleetCreateRecoveryReadbackHandler records verifier-owned exact post-effect
// inventory evidence for the current attempt. It grants no provider effect.
func FleetCreateRecoveryReadbackHandler(store FleetCreateRecoveryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		principal, ok := fleetCreateVerifierPrincipal(r)
		if !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_verifier_required", "dedicated work-staging Fleet create verifier is required")
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_create_store_unavailable", "Fleet create recovery store is unavailable")
			return
		}
		cluster, intentID, attemptID := chi.URLParam(r, "cluster"), chi.URLParam(r, "intentID"), chi.URLParam(r, "attemptID")
		var readback fleetcreate.RecoveryReadback
		if err := decodeControlJSON(w, r, &readback); err != nil || readback.Validate() != nil || readback.Cluster != cluster ||
			readback.IntentID != intentID || readback.PriorAttemptID != attemptID {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_create_recovery_readback", "valid recovery readback must match the cluster, intent, and prior-attempt path")
			return
		}
		if err := store.RecordFleetCreateRecoveryReadback(r.Context(), readback, principal.Subject); err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_recovery_readback_refused")
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

// FleetCreateRecoveryAttemptHandler admits a successor only for the exact
// verifier-bound GitHub run and prior-attempt recovery readback.
func FleetCreateRecoveryAttemptHandler(store FleetCreateRecoveryAttemptStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		principal, ok := fleetCreateAttemptPrincipal(w, r)
		if !ok {
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_create_store_unavailable", "Fleet create recovery store is unavailable")
			return
		}
		cluster, intentID, priorAttemptID := chi.URLParam(r, "cluster"), chi.URLParam(r, "intentID"), chi.URLParam(r, "priorAttemptID")
		var request fleetcreate.AttemptStartRequest
		if err := decodeControlJSON(w, r, &request); err != nil || request.Validate() != nil || request.Cluster != cluster || request.IntentID != intentID {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_create_recovery_attempt", "valid successor attempt must match the intent and cluster path")
			return
		}
		binding, err := store.GetFleetCreateDispatch(r.Context(), intentID)
		if err != nil || !binding.MatchesStart(request) || !fleetCreateOIDCMatchesDispatch(principal.CI, binding) {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_dispatch_identity_mismatch", "runner identity does not match the verifier-bound protected dispatch")
			return
		}
		intent, err := store.GetFleetCreateIntent(r.Context(), cluster)
		if err != nil || intent.IntentID != intentID || intent.PlanID != request.PlanID {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_create_intent_mismatch", "successor attempt does not match the saved create intent")
			return
		}
		attempt, err := store.StartFleetCreateRecoveryAttempt(r.Context(), request, priorAttemptID, fleetCreateAttemptSubject)
		if err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_recovery_attempt_refused")
			return
		}
		writeJSONStatus(w, http.StatusCreated, attempt)
	}
}

// FleetCreateTerminalReceiptHandler records/reads complete verifier evidence
// and performs the no-provider atomic transition to an ordinary Fleet target.
func FleetCreateTerminalReceiptHandler(store FleetCreateTerminalStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		principal, ok := fleetCreateVerifierPrincipal(r)
		if !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_verifier_required", "dedicated work-staging Fleet create verifier is required")
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_create_store_unavailable", "Fleet create terminal store is unavailable")
			return
		}
		cluster, intentID := chi.URLParam(r, "cluster"), chi.URLParam(r, "intentID")
		if r.Method == http.MethodGet {
			receipt, err := store.GetFleetCreateTerminalReceipt(r.Context(), intentID)
			if errors.Is(err, etcdstore.ErrNotFound) {
				WriteControlProblem(w, r, http.StatusNotFound, "fleet_create_terminal_not_found", "complete Fleet create terminal receipt was not found")
				return
			}
			if err != nil || receipt.Cluster != cluster {
				WriteControlProblem(w, r, http.StatusConflict, "fleet_create_terminal_refused", "terminal receipt does not match the route")
				return
			}
			writeJSON(w, receipt)
			return
		}
		var receipt fleetcreate.TerminalReceipt
		if err := decodeControlJSON(w, r, &receipt); err != nil || receipt.Validate() != nil ||
			receipt.Cluster != cluster || receipt.IntentID != intentID {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_create_terminal", "complete terminal evidence must match the cluster and intent path")
			return
		}
		if err := store.RecordFleetCreateTerminalReceipt(r.Context(), receipt, principal.Subject); err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_terminal_refused")
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

// FleetCreateTerminalTargetHandler executes only Norn's atomic target/fence
// handoff, using the already recorded terminal receipt.
func FleetCreateTerminalTargetHandler(store FleetCreateTerminalStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		if _, ok := fleetCreateVerifierPrincipal(r); !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_verifier_required", "dedicated work-staging Fleet create verifier is required")
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_create_store_unavailable", "Fleet create terminal store is unavailable")
			return
		}
		cluster, intentID := chi.URLParam(r, "cluster"), chi.URLParam(r, "intentID")
		receipt, err := store.GetFleetCreateTerminalReceipt(r.Context(), intentID)
		if err != nil || receipt.Cluster != cluster {
			writeFleetCreateStoreError(w, r, etcdstore.ErrFleetCreateIntentRefused, "fleet_create_terminal_refused")
			return
		}
		target, err := store.RegisterFleetCreateTerminalTarget(r.Context(), receipt)
		if err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_target_handoff_refused")
			return
		}
		writeJSONStatus(w, http.StatusCreated, target)
	}
}

// FleetCreateEffectGrantHandler issues a verifier-authorized exact-remainder
// grant or reads back its durable issued/consumed state.
func FleetCreateEffectGrantHandler(store FleetCreateGrantStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		_, ok := fleetCreateVerifierPrincipal(r)
		if !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_verifier_required", "dedicated work-staging Fleet create verifier is required")
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_create_store_unavailable", "Fleet create grant store is unavailable")
			return
		}
		cluster, intentID, attemptID := chi.URLParam(r, "cluster"), chi.URLParam(r, "intentID"), chi.URLParam(r, "attemptID")
		admission, err := store.GetFleetCreateAdmission(r.Context(), cluster, intentID, attemptID)
		if errors.Is(err, etcdstore.ErrNotFound) {
			WriteControlProblem(w, r, http.StatusNotFound, "fleet_create_admission_not_found", "verifier admission was not found")
			return
		}
		if err != nil || admission.Cluster != cluster || admission.IntentID != intentID || admission.AttemptID != attemptID {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_create_admission_refused", "verifier admission does not match the grant route")
			return
		}
		if r.Method == http.MethodGet {
			grant, consumed, err := store.GetFleetCreateEffectGrant(r.Context(), intentID, attemptID)
			if errors.Is(err, etcdstore.ErrNotFound) {
				WriteControlProblem(w, r, http.StatusNotFound, "fleet_create_grant_not_found", "effect grant was not issued")
				return
			}
			if err != nil || !grant.MatchesRequest(fleetCreateRequestFromAdmission(admission)) {
				WriteControlProblem(w, r, http.StatusConflict, "fleet_create_grant_refused", "effect grant could not be verified against admission")
				return
			}
			writeJSON(w, fleetcreate.GrantReadback{Grant: grant, Consumed: consumed})
			return
		}
		var request fleetcreate.Request
		if err := decodeControlJSON(w, r, &request); err != nil || request.Validate() != nil ||
			request.IntentID != intentID || request.AttemptID != attemptID || !request.MatchesAdmission(admission) {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_create_grant", "exact grant request must match the admitted remainder and route")
			return
		}
		grant, err := store.IssueFleetCreateEffectGrant(r.Context(), request)
		if err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_grant_refused")
			return
		}
		writeJSONStatus(w, http.StatusCreated, fleetcreate.GrantReadback{Grant: grant, Consumed: false})
	}
}

// FleetCreateEffectGrantConsumeHandler durably consumes the exact grant only
// for the runner authenticated by its immutable protected dispatch binding.
func FleetCreateEffectGrantConsumeHandler(store FleetCreateGrantConsumeStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		principal, ok := fleetCreateAttemptPrincipal(w, r)
		if !ok {
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_create_store_unavailable", "Fleet create grant store is unavailable")
			return
		}
		cluster, intentID, attemptID := chi.URLParam(r, "cluster"), chi.URLParam(r, "intentID"), chi.URLParam(r, "attemptID")
		var request fleetcreate.Request
		if err := decodeControlJSON(w, r, &request); err != nil || request.Validate() != nil ||
			request.IntentID != intentID || request.AttemptID != attemptID {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_create_grant", "exact grant request must match the intent and attempt path")
			return
		}
		binding, err := store.GetFleetCreateDispatch(r.Context(), intentID)
		if err != nil || !fleetCreateOIDCMatchesDispatch(principal.CI, binding) {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_dispatch_identity_mismatch", "runner identity does not match the verifier-bound protected dispatch")
			return
		}
		intent, err := store.GetFleetCreateIntent(r.Context(), cluster)
		if err != nil || intent.IntentID != intentID || intent.PlanID != request.PlanID || binding.Cluster != cluster {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_create_intent_mismatch", "grant request does not match the saved create intent")
			return
		}
		if _, err := store.GetFleetCreateAttempt(r.Context(), cluster, intentID, attemptID); err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_attempt_refused")
			return
		}
		admission, err := store.GetFleetCreateAdmission(r.Context(), cluster, intentID, attemptID)
		if err != nil || !request.MatchesAdmission(admission) {
			writeFleetCreateStoreError(w, r, etcdstore.ErrFleetCreateIntentRefused, "fleet_create_admission_refused")
			return
		}
		grant, err := store.ConsumeFleetCreateEffectGrant(r.Context(), request)
		if err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_grant_consume_refused")
			return
		}
		writeJSON(w, fleetcreate.GrantReadback{Grant: grant, Consumed: true})
	}
}

// FleetCreateEffectGrantStatusHandler gives the exact bound runner a
// non-capability readback after an ambiguous grant-consume response.
func FleetCreateEffectGrantStatusHandler(store FleetCreateGrantStatusStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preventSensitiveResponseCaching(w)
		principal, ok := fleetCreateAttemptPrincipal(w, r)
		if !ok {
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_create_store_unavailable", "Fleet create grant status store is unavailable")
			return
		}
		cluster, intentID, attemptID := chi.URLParam(r, "cluster"), chi.URLParam(r, "intentID"), chi.URLParam(r, "attemptID")
		binding, err := store.GetFleetCreateDispatch(r.Context(), intentID)
		if err != nil || !fleetCreateOIDCMatchesDispatch(principal.CI, binding) || binding.Cluster != cluster {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_dispatch_identity_mismatch", "runner identity does not match the verifier-bound protected dispatch")
			return
		}
		intent, err := store.GetFleetCreateIntent(r.Context(), cluster)
		if err != nil || intent.IntentID != intentID {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_create_intent_mismatch", "grant status does not match the saved create intent")
			return
		}
		if _, err := store.GetFleetCreateAttempt(r.Context(), cluster, intentID, attemptID); err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_attempt_refused")
			return
		}
		admission, err := store.GetFleetCreateAdmission(r.Context(), cluster, intentID, attemptID)
		if err != nil {
			writeFleetCreateStoreError(w, r, err, "fleet_create_admission_refused")
			return
		}
		grant, consumed, err := store.GetFleetCreateEffectGrant(r.Context(), intentID, attemptID)
		if errors.Is(err, etcdstore.ErrNotFound) {
			WriteControlProblem(w, r, http.StatusNotFound, "fleet_create_grant_not_found", "effect grant was not issued")
			return
		}
		if err != nil || !grant.MatchesRequest(fleetCreateRequestFromAdmission(admission)) {
			WriteControlProblem(w, r, http.StatusConflict, "fleet_create_grant_refused", "grant status could not be verified against admission")
			return
		}
		writeJSON(w, fleetcreate.GrantStatus{IntentID: intentID, AttemptID: attemptID, GrantSHA256: grant.Digest(), Consumed: consumed})
	}
}

func fleetCreateRequestFromAdmission(admission fleetcreate.Admission) fleetcreate.Request {
	return fleetcreate.Request{SchemaVersion: fleetcreate.RequestSchema, IntentID: admission.IntentID,
		PlanID: admission.PlanID, AttemptID: admission.AttemptID, IntentSHA256: admission.IntentSHA256,
		AdmissionSHA256: admission.Digest(), RemainderSHA256: admission.RemainderSHA256}
}

func fleetCreateAttemptPrincipal(w http.ResponseWriter, r *http.Request) (AccessPrincipal, bool) {
	p, ok := AccessPrincipalFromRequest(r)
	if !ok || p.Source != AccessPrincipalSourceManagedToken || p.Legacy || p.CI == nil || p.TokenID == "" ||
		p.DeviceID != "" || len(p.Scopes) != 1 || p.Scopes[0] != ScopeFleetOperate ||
		p.ExpiresAt.IsZero() || !time.Now().Before(p.ExpiresAt) || p.CI.Provider != "github-actions" || p.CI.Intent != "create" {
		WriteControlProblem(w, r, http.StatusForbidden, "fleet_create_runner_required", "protected GitHub Actions Fleet-create identity is required")
		return AccessPrincipal{}, false
	}
	return p, true
}

func fleetCreateOIDCMatchesDispatch(ci *CIIdentity, binding fleetcreate.DispatchBinding) bool {
	return ci != nil && ci.Provider == "github-actions" && ci.Repository == binding.Repository &&
		ci.RepositoryID == binding.RepositoryID && ci.RepositoryOwnerID == binding.RepositoryOwnerID &&
		ci.WorkflowRef == fmt.Sprintf("%s/%s@refs/heads/main", binding.Repository, binding.WorkflowPath) &&
		ci.WorkflowSHA == binding.WorkflowSHA && ci.Environment == binding.Environment &&
		(ci.GitHubEnvironment == "" || ci.GitHubEnvironment == binding.Environment) &&
		ci.RunID == binding.RunID && ci.RunAttempt == binding.RunAttempt && ci.SHA == binding.CommitSHA &&
		ci.Ref == "refs/heads/main" && ci.RefType == "branch" && ci.EventName == "workflow_dispatch" && ci.RefProtected
}

func writeFleetCreateStoreError(w http.ResponseWriter, r *http.Request, err error, code string) {
	if errors.Is(err, etcdstore.ErrFleetCreateIntentRefused) {
		WriteControlProblem(w, r, http.StatusConflict, code, "pre-registration create state changed or conflicts with the immutable binding")
		return
	}
	WriteControlProblem(w, r, http.StatusServiceUnavailable, code+"_uncertain", "pre-registration create state could not be durably confirmed")
}
