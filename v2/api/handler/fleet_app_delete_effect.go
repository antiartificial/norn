package handler

// This handler is mounted only behind an explicit default-off gate. Norn's
// one-use permit proves only its own attempt/fence authority. The independent
// Fleet PostgreSQL verifier and writer reservation prove the provider and
// workload facts before an executor may use this permit.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/fleet/appdelete"
)

// AppDeleteEffectStore consumes one attempt-bound Norn co-permit. It must
// atomically compare the current attempt, target fence, epoch, dispatch and
// absence of prior consumption. It does not attest the request's PostgreSQL
// writer identity, provider identity, or workload digest.
type AppDeleteEffectStore interface {
	ConsumeAppDeleteCoPermit(context.Context, appdelete.Request, string) error
}
type App04DeleteAdmissionStore interface {
	RecordApp04DeleteAdmission(context.Context, appdelete.App04Admission, string) error
	GetApp04DeleteAdmission(context.Context, string) (appdelete.App04Admission, error)
}
type App03DeleteAdmissionStore interface {
	RecordApp03DeleteAdmission(context.Context, appdelete.App03Admission, string) error
	GetApp03DeleteAdmission(context.Context, string) (appdelete.App03Admission, error)
}
type AppDeleteTerminalStore interface {
	RecordAppDeleteTerminal(context.Context, appdelete.TerminalRequest, string) (appdelete.TerminalReceipt, error)
	GetAppDeleteTerminal(context.Context, string) (appdelete.TerminalReceipt, error)
}
type AppDeleteFinalizeStore interface {
	FinalizeAppDelete(context.Context, appdelete.FinalizeRequest, string) (appdelete.FinalizeReceipt, error)
	GetAppDeleteFinalize(context.Context, string) (appdelete.FinalizeReceipt, error)
}
type AppDeleteSnapshotStore interface {
	GetAppDeleteSnapshot(context.Context, string, string) (appdelete.Snapshot, error)
}

// AppDeleteSnapshotHandler gives the verifier a coherent, read-only view of
// Norn authority before it forms independent provider and workload evidence.
func AppDeleteSnapshotHandler(store AppDeleteSnapshotStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")
		if _, ok := appDeleteVerifierPrincipal(r); !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "app_delete_verifier_required", "a dedicated work-staging app delete verifier token is required")
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "app_delete_snapshot_unavailable", "durable app delete snapshot is unavailable")
			return
		}
		v, err := store.GetAppDeleteSnapshot(r.Context(), chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID"))
		if errors.Is(err, appdelete.ErrNotFound) {
			WriteControlProblem(w, r, http.StatusNotFound, "app_delete_snapshot_not_found", "app delete plan or attempt is absent")
			return
		}
		if err != nil {
			WriteControlProblem(w, r, http.StatusConflict, "app_delete_snapshot_refused", "app delete snapshot could not be established")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

const appDeleteVerifierSubject = "fleet-app-delete-verifier:work-staging"

func appDeleteVerifierPrincipal(r *http.Request) (AccessPrincipal, bool) {
	p, ok := AccessPrincipalFromRequest(r)
	return p, ok && p.Source == AccessPrincipalSourceManagedToken && !p.Legacy && p.CI == nil && p.DeviceID == "" &&
		p.Subject == appDeleteVerifierSubject && p.TokenID != "" && len(p.Scopes) == 1 && p.Scopes[0] == ScopeFleetAppDeleteVerify &&
		!p.ExpiresAt.IsZero() && time.Now().Before(p.ExpiresAt)
}

// App04DeleteAdmissionHandler is default off with the app-delete route group.
// The verifier attests saved provider plan and workload facts; Norn compares
// its own plan, live attempt and fence before recording the one-use binding.
func App04DeleteAdmissionHandler(store App04DeleteAdmissionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")
		principal, ok := appDeleteVerifierPrincipal(r)
		if !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "app_delete_verifier_required", "a dedicated work-staging app delete verifier token is required")
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "app04_delete_admission_unavailable", "durable app04 admission is unavailable")
			return
		}
		if r.Method == http.MethodGet {
			admission, err := store.GetApp04DeleteAdmission(r.Context(), chi.URLParam(r, "planID"))
			if errors.Is(err, appdelete.ErrNotFound) {
				WriteControlProblem(w, r, http.StatusNotFound, "app04_delete_admission_not_found", "app04 admission is absent")
				return
			}
			if err != nil || admission.AttemptID != chi.URLParam(r, "attemptID") {
				WriteControlProblem(w, r, http.StatusConflict, "app04_delete_admission_mismatch", "app04 admission differs from route")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(admission)
			return
		}
		var admission appdelete.App04Admission
		if err := decodeControlJSON(w, r, &admission); err != nil || admission.Validate() != nil || admission.Target != "app-04" ||
			admission.PlanID != chi.URLParam(r, "planID") || admission.AttemptID != chi.URLParam(r, "attemptID") {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app04_delete_admission", "exact app04 admission is required")
			return
		}
		if err := store.RecordApp04DeleteAdmission(r.Context(), admission, principal.Subject); err != nil {
			status, code := http.StatusConflict, "app04_delete_admission_refused"
			if !errors.Is(err, appdelete.ErrRefused) {
				status, code = http.StatusServiceUnavailable, "app04_delete_admission_uncertain"
			}
			WriteControlProblem(w, r, status, code, "app04 admission was not recorded")
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

// App03DeleteAdmissionHandler records the verifier's exact app03 binding.
func App03DeleteAdmissionHandler(store App03DeleteAdmissionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")
		principal, ok := appDeleteVerifierPrincipal(r)
		if !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "app_delete_verifier_required", "a dedicated work-staging app delete verifier token is required")
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "app03_delete_admission_unavailable", "durable app03 admission is unavailable")
			return
		}
		if r.Method == http.MethodGet {
			admission, err := store.GetApp03DeleteAdmission(r.Context(), chi.URLParam(r, "planID"))
			if errors.Is(err, appdelete.ErrNotFound) {
				WriteControlProblem(w, r, http.StatusNotFound, "app03_delete_admission_not_found", "app03 admission is absent")
				return
			}
			if err != nil || admission.AttemptID != chi.URLParam(r, "attemptID") {
				WriteControlProblem(w, r, http.StatusConflict, "app03_delete_admission_mismatch", "app03 admission differs from route")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(admission)
			return
		}
		var admission appdelete.App03Admission
		if err := decodeControlJSON(w, r, &admission); err != nil || admission.Validate() != nil ||
			admission.Target != "app-03" || admission.PlanID != chi.URLParam(r, "planID") || admission.AttemptID != chi.URLParam(r, "attemptID") {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app03_delete_admission", "exact app03 admission is required")
			return
		}
		if err := store.RecordApp03DeleteAdmission(r.Context(), admission, principal.Subject); err != nil {
			status, code := http.StatusConflict, "app03_delete_admission_refused"
			if !errors.Is(err, appdelete.ErrRefused) {
				status, code = http.StatusServiceUnavailable, "app03_delete_admission_uncertain"
			}
			WriteControlProblem(w, r, status, code, "app03 admission was not recorded")
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

func AppDeleteTerminalHandler(store AppDeleteTerminalStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")
		principal, ok := appDeleteVerifierPrincipal(r)
		if !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "app_delete_verifier_required", "a dedicated work-staging app delete verifier token is required")
			return
		}
		planID, attemptID := chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID")
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "app_delete_terminal_unavailable", "durable terminal evidence is unavailable")
			return
		}
		if r.Method == http.MethodGet {
			receipt, err := store.GetAppDeleteTerminal(r.Context(), planID)
			if errors.Is(err, appdelete.ErrNotFound) {
				WriteControlProblem(w, r, http.StatusNotFound, "app_delete_terminal_not_found", "terminal evidence is not recorded")
				return
			}
			if err != nil || receipt.Evidence.AttemptID != attemptID {
				WriteControlProblem(w, r, http.StatusConflict, "app_delete_terminal_mismatch", "terminal evidence differs from the route")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(receipt)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16*1024))
		request, decodeErr := appdelete.DecodeTerminalRequest(raw)
		if err != nil || decodeErr != nil || request.PlanID != planID || request.AttemptID != attemptID {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app_delete_terminal_request", "the request must exactly match the route and typed contract")
			return
		}
		receipt, err := store.RecordAppDeleteTerminal(r.Context(), request, principal.Subject)
		if err != nil {
			status, code := http.StatusConflict, "app_delete_terminal_refused"
			if !errors.Is(err, appdelete.ErrRefused) {
				status, code = http.StatusServiceUnavailable, "app_delete_terminal_uncertain"
			}
			WriteControlProblem(w, r, status, code, "app delete terminal evidence was not recorded")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(receipt)
	}
}

func AppDeleteFinalizeHandler(store AppDeleteFinalizeStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")
		principal, ok := appDeleteVerifierPrincipal(r)
		if !ok {
			WriteControlProblem(w, r, http.StatusForbidden, "app_delete_verifier_required", "a dedicated work-staging app delete verifier token is required")
			return
		}
		planID, attemptID := chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID")
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "app_delete_finalize_unavailable", "durable app delete completion is unavailable")
			return
		}
		if r.Method == http.MethodGet {
			receipt, err := store.GetAppDeleteFinalize(r.Context(), planID)
			if errors.Is(err, appdelete.ErrNotFound) {
				WriteControlProblem(w, r, http.StatusNotFound, "app_delete_finalize_not_found", "completion is not recorded")
				return
			}
			if err != nil || receipt.AttemptID != attemptID {
				WriteControlProblem(w, r, http.StatusConflict, "app_delete_finalize_mismatch", "completion differs from the route")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(receipt)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16*1024))
		request, decodeErr := appdelete.DecodeFinalizeRequest(raw)
		if err != nil || decodeErr != nil || request.PlanID != planID || request.AttemptID != attemptID {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app_delete_finalize_request", "the request must exactly match the route and typed contract")
			return
		}
		receipt, err := store.FinalizeAppDelete(r.Context(), request, principal.Subject)
		if err != nil {
			status, code := http.StatusConflict, "app_delete_finalize_refused"
			if !errors.Is(err, appdelete.ErrRefused) {
				status, code = http.StatusServiceUnavailable, "app_delete_finalize_uncertain"
			}
			WriteControlProblem(w, r, status, code, "app delete completion was not recorded")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(receipt)
	}
}

// AppDeleteEffectHandler is the narrow controller/executor authorization
// endpoint. It never contacts DigitalOcean and has no retry/readback route.
func AppDeleteEffectHandler(store AppDeleteEffectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")
		principal, ok := AccessPrincipalFromRequest(r)
		boundAttempt, bound := FleetAttemptCredentialAttemptID(principal)
		if !ok || !bound {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_attempt_credential_required", "an exact live attempt credential is required")
			return
		}
		planID, attemptID := chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID")
		if planID == "" || attemptID == "" || attemptID != boundAttempt {
			WriteControlProblem(w, r, http.StatusForbidden, "fleet_attempt_binding_mismatch", "the credential is not bound to this attempt")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16*1024))
		request, decodeErr := appdelete.DecodeRequest(raw)
		if err != nil || decodeErr != nil || request.PlanID != planID || request.AttemptID != attemptID {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app_delete_effect_request", "the request must exactly match the route and typed contract")
			return
		}
		if store == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "app_delete_effect_unavailable", "durable app delete authorization is unavailable")
			return
		}
		err = store.ConsumeAppDeleteCoPermit(r.Context(), request, principal.Subject)
		if err != nil {
			status, code := http.StatusConflict, "app_delete_effect_refused"
			if err != nil && !errors.Is(err, appdelete.ErrRefused) {
				status, code = http.StatusServiceUnavailable, "app_delete_effect_uncertain"
			}
			WriteControlProblem(w, r, status, code, "app delete effect authorization was not issued")
			return
		}
		grant := appdelete.GrantFromRequest(request)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(grant)
	}
}
