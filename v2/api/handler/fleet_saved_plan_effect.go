package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/savedplaneffect"
)

const savedPlanEffectVerifierSubject = "fleet-saved-plan-effect-verifier:work-staging"

type SavedPlanEffectStore interface {
	RecordSavedPlanEffectPlanBinding(context.Context, savedplaneffect.PlanBinding, string) error
	GetSavedPlanEffectPlanBinding(context.Context, string) (savedplaneffect.PlanBinding, error)
	RecordSavedPlanEffectDispatchBinding(context.Context, savedplaneffect.DispatchBinding, string) error
	GetSavedPlanEffectDispatchBinding(context.Context, string) (savedplaneffect.DispatchBinding, error)
	CreateSavedPlanEffectPreparation(context.Context, string, string, time.Time) (savedplaneffect.SignedPreparationProjection, error)
	GetSavedPlanEffectPreparation(context.Context, string, string) (savedplaneffect.SignedPreparationProjection, error)
	ListSavedPlanEffectProjectionCandidates(context.Context, string) ([]etcdstore.SavedPlanEffectProjectionCandidate, error)
	RecordSavedPlanEffectClaim(context.Context, savedplaneffect.Claim, string, time.Time) error
	GetSavedPlanEffectClaim(context.Context, string, string, string) (etcdstore.SavedPlanEffectClaimRead, error)
	ConsumeSavedPlanEffectApply(context.Context, string, string, string, time.Time) (savedplaneffect.ApplyGrant, error)
	GetSavedPlanEffectRecovery(context.Context, string, string, string) (etcdstore.SavedPlanEffectRecovery, error)
	AuthorizeSavedPlanRefreshOnly(context.Context, string, string, string, string, time.Time) (savedplaneffect.RefreshOnlyAuthorization, error)
}

func SavedPlanEffectPreparationPendingHandler(store SavedPlanEffectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := savedPlanEffectVerifier(r)
		if !ok || store == nil {
			WriteControlProblem(w, r, http.StatusForbidden, "saved_plan_effect_verifier_required", "a protected saved-plan verifier service is required")
			return
		}
		pending, err := store.ListSavedPlanEffectProjectionCandidates(r.Context(), p.Subject)
		if err != nil {
			savedPlanEffectError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"pending": pending, "authorizesApply": false})
	}
}

// SavedPlanEffectProjectionHandler owns no execution authority. POST has the
// sole body `{}` and derives durable signed inbox bytes from Norn state; GET
// is the lost-response reconciliation path and never creates/re-signs.
func SavedPlanEffectPreparationHandler(store SavedPlanEffectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := savedPlanEffectVerifier(r)
		planID := chi.URLParam(r, "planID")
		if !ok || uuid.Validate(planID) != nil || store == nil {
			WriteControlProblem(w, r, http.StatusForbidden, "saved_plan_effect_verifier_required", "a protected saved-plan verifier service is required")
			return
		}
		var signed savedplaneffect.SignedPreparationProjection
		var err error
		if r.Method == http.MethodGet {
			signed, err = store.GetSavedPlanEffectPreparation(r.Context(), planID, p.Subject)
		} else {
			if !decodeSavedPlanEffect(w, r, &struct{}{}) {
				WriteControlProblem(w, r, http.StatusBadRequest, "invalid_saved_plan_effect_projection", "the projection create body must be empty")
				return
			}
			signed, err = store.CreateSavedPlanEffectPreparation(r.Context(), planID, p.Subject, time.Now().UTC())
		}
		if err != nil {
			savedPlanEffectError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusOK
		if r.Method == http.MethodPost {
			status = http.StatusCreated
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"signedPreparation": signed, "authorizesApply": false})
	}
}

func SavedPlanEffectPlanBindingHandler(store SavedPlanEffectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := savedPlanEffectVerifier(r)
		planID := chi.URLParam(r, "planID")
		if !ok || uuid.Validate(planID) != nil || store == nil {
			WriteControlProblem(w, r, http.StatusForbidden, "saved_plan_effect_verifier_required", "a protected saved-plan verifier service is required")
			return
		}
		if r.Method == http.MethodGet {
			binding, err := store.GetSavedPlanEffectPlanBinding(r.Context(), planID)
			if err != nil {
				savedPlanEffectError(w, r, err)
				return
			}
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"planBinding": binding, "authorizesApply": false})
			return
		}
		var binding savedplaneffect.PlanBinding
		if !decodeSavedPlanEffect(w, r, &binding) || binding.PlanID != planID || binding.Validate() != nil {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_saved_plan_effect_plan_binding", "an exact pre-job plan binding is required")
			return
		}
		if err := store.RecordSavedPlanEffectPlanBinding(r.Context(), binding, p.Subject); err != nil {
			savedPlanEffectError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

func SavedPlanEffectDispatchBindingHandler(store SavedPlanEffectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := savedPlanEffectVerifier(r)
		planID := chi.URLParam(r, "planID")
		if !ok || uuid.Validate(planID) != nil || store == nil {
			WriteControlProblem(w, r, http.StatusForbidden, "saved_plan_effect_verifier_required", "a protected saved-plan verifier service is required")
			return
		}
		if r.Method == http.MethodGet {
			binding, err := store.GetSavedPlanEffectDispatchBinding(r.Context(), planID)
			if err != nil {
				savedPlanEffectError(w, r, err)
				return
			}
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"dispatchBinding": binding, "authorizesApply": false})
			return
		}
		var binding savedplaneffect.DispatchBinding
		if !decodeSavedPlanEffect(w, r, &binding) || binding.PlanID != planID || binding.Validate() != nil {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_saved_plan_effect_dispatch_binding", "an exact post-dispatch binding is required")
			return
		}
		if err := store.RecordSavedPlanEffectDispatchBinding(r.Context(), binding, p.Subject); err != nil {
			savedPlanEffectError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

func SavedPlanEffectClaimReadHandler(store SavedPlanEffectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, verifier := savedPlanEffectVerifier(r)
		planID, attemptID, pathOK := savedPlanEffectPath(r)
		if !verifier || !pathOK || store == nil {
			WriteControlProblem(w, r, http.StatusForbidden, "saved_plan_effect_verifier_required", "a dedicated saved-plan verifier credential is required")
			return
		}
		state, err := store.GetSavedPlanEffectClaim(r.Context(), planID, attemptID, p.Subject)
		if err != nil {
			savedPlanEffectError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	}
}

func savedPlanEffectVerifier(r *http.Request) (AccessPrincipal, bool) {
	p, ok := AccessPrincipalFromRequest(r)
	return p, ok && p.Source == AccessPrincipalSourceManagedToken && !p.Legacy && p.CI == nil && p.DeviceID == "" && p.Subject == savedPlanEffectVerifierSubject &&
		p.TokenID != "" && len(p.Scopes) == 1 && p.Scopes[0] == ScopeFleetSavedPlanEffectVerify && !p.ExpiresAt.IsZero() && time.Now().Before(p.ExpiresAt)
}

func savedPlanEffectAttempt(r *http.Request) (string, bool) {
	p, ok := AccessPrincipalFromRequest(r)
	if !ok {
		return "", false
	}
	return FleetAttemptCredentialAttemptID(p)
}

func savedPlanEffectPath(r *http.Request) (string, string, bool) {
	planID, attemptID := chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID")
	if uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil {
		return "", "", false
	}
	return planID, attemptID, true
}

func decodeSavedPlanEffect(w http.ResponseWriter, r *http.Request, value any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16*1024))
	if err != nil || !json.Valid(raw) || savedPlanEffectDuplicateKey(json.NewDecoder(bytes.NewReader(raw))) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

// Duplicate fields make an effect claim ambiguous even when Go's decoder
// happens to select the last one. Reject them before typed validation.
func savedPlanEffectDuplicateKey(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return true
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return false
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return true
			}
			if _, duplicate := seen[name]; duplicate {
				return true
			}
			seen[name] = struct{}{}
		}
		if savedPlanEffectDuplicateKey(decoder) {
			return true
		}
	}
	_, err = decoder.Token()
	return err != nil
}

func savedPlanEffectError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := http.StatusConflict, "saved_plan_effect_refused"
	if errors.Is(err, etcdstore.ErrNotFound) {
		status, code = http.StatusNotFound, "saved_plan_effect_not_found"
	}
	if !errors.Is(err, savedplaneffect.ErrInvalid) && !errors.Is(err, savedplaneffect.ErrConsumed) && !errors.Is(err, etcdstore.ErrSavedPlanEffectConflict) && !errors.Is(err, etcdstore.ErrNotFound) {
		status, code = http.StatusServiceUnavailable, "saved_plan_effect_uncertain"
	}
	WriteControlProblem(w, r, status, code, "saved-plan effect transition failed; recover exact state with GET")
}

func SavedPlanEffectClaimHandler(store SavedPlanEffectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := savedPlanEffectVerifier(r)
		if !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "saved_plan_effect_verifier_required", "a dedicated saved-plan verifier credential is required")
			return
		}
		planID, attemptID, ok := savedPlanEffectPath(r)
		if !ok || store == nil {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_saved_plan_effect_claim", "an exact claim path and store are required")
			return
		}
		var claim savedplaneffect.Claim
		if !decodeSavedPlanEffect(w, r, &claim) || claim.PlanID != planID || claim.AttemptID != attemptID || claim.Validate() != nil {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_saved_plan_effect_claim", "an exact app-pool saved-plan claim is required")
			return
		}
		if err := store.RecordSavedPlanEffectClaim(r.Context(), claim, p.Subject, time.Now().UTC()); err != nil {
			savedPlanEffectError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

func SavedPlanEffectApplyHandler(store SavedPlanEffectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bound, ok := savedPlanEffectAttempt(r)
		planID, attemptID, pathOK := savedPlanEffectPath(r)
		if !ok || !pathOK || bound != attemptID || store == nil || !decodeSavedPlanEffect(w, r, &struct{}{}) {
			WriteControlProblem(w, r, http.StatusForbidden, "saved_plan_effect_attempt_required", "an exact live attempt credential and empty request are required")
			return
		}
		grant, err := store.ConsumeSavedPlanEffectApply(r.Context(), planID, attemptID, "fleet-attempt:"+bound, time.Now().UTC())
		if err != nil {
			savedPlanEffectError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"grant": grant, "authorizesApply": true})
	}
}

func SavedPlanEffectRecoveryHandler(store SavedPlanEffectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bound, ok := savedPlanEffectAttempt(r)
		planID, attemptID, pathOK := savedPlanEffectPath(r)
		if !ok || !pathOK || bound != attemptID || store == nil {
			WriteControlProblem(w, r, http.StatusForbidden, "saved_plan_effect_attempt_required", "an exact live attempt credential is required")
			return
		}
		state, err := store.GetSavedPlanEffectRecovery(r.Context(), planID, attemptID, "fleet-attempt:"+bound)
		if err != nil {
			savedPlanEffectError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	}
}

func SavedPlanRefreshOnlyHandler(store SavedPlanEffectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := savedPlanEffectVerifier(r)
		planID, attemptID, pathOK := savedPlanEffectPath(r)
		if !ok || !pathOK || store == nil {
			WriteControlProblem(w, r, http.StatusForbidden, "saved_plan_effect_verifier_required", "a dedicated saved-plan verifier credential is required")
			return
		}
		var request struct {
			ProviderAbsenceSHA256 string `json:"providerAbsenceSha256"`
		}
		if !decodeSavedPlanEffect(w, r, &request) {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_saved_plan_refresh_only", "an exact provider absence digest is required")
			return
		}
		refresh, err := store.AuthorizeSavedPlanRefreshOnly(r.Context(), planID, attemptID, p.Subject, request.ProviderAbsenceSHA256, time.Now().UTC())
		if err != nil {
			savedPlanEffectError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"refreshOnly": refresh, "authorizesApply": false})
	}
}
