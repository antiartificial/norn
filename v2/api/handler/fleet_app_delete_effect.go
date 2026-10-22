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
