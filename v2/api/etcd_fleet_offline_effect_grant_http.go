package main

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
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
)

type offlineEffectGrantStore interface {
	IssueOfflineEffectGrant(context.Context, string, string, etcdstore.OfflineRunnerProof, time.Time) (offline.EffectGrant, error)
	ConsumeOfflineEffectGrant(context.Context, string, string, string, etcdstore.OfflineRunnerProof, time.Time) (offline.EffectGrant, error)
	RecoverOfflineEffectGrant(context.Context, string, string, etcdstore.OfflineRunnerProof, time.Time) (offline.EffectGrant, error)
}

type offlineEffectIdentityStore interface {
	offlineAttemptDeviceStore
}

func offlineEffectNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func registerEtcdFleetOfflineEffectGrantRoutes(router chi.Router, auth func(http.Handler) http.Handler,
	grants offlineEffectGrantStore, identities *etcdstore.AuthStore) {
	base := "/api/v1/fleet/offline/attempts/{planID}/{attemptID}/effect-grants"
	router.With(offlineEffectNoStore, auth).Post(base, etcdOfflineEffectGrantIssue(grants, identities))
	router.With(offlineEffectNoStore, auth).Get(base, etcdOfflineEffectGrantRecover(grants, identities, false))
	router.With(offlineEffectNoStore, auth).Get(base+"/{grantID}", etcdOfflineEffectGrantRecover(grants, identities, true))
	router.With(offlineEffectNoStore, auth).Post(base+"/{grantID}/consume", etcdOfflineEffectGrantConsume(grants, identities))
}

func offlineEffectGrantProof(w http.ResponseWriter, r *http.Request, identities offlineEffectIdentityStore) (etcdstore.OfflineRunnerProof, bool) {
	p, ok := offlineAttemptPrincipal(w, r, identities)
	if !ok {
		return etcdstore.OfflineRunnerProof{}, false
	}
	now := time.Now().UTC()
	proof, err := identities.ProveOfflineRunner(r.Context(), p.Subject, now)
	if err != nil || proof.TokenID != p.TokenID || proof.DeviceID != p.DeviceID {
		handler.WriteControlProblem(w, r, http.StatusPreconditionFailed, "offline_runner_not_current", "the authenticated offline runner token is not the reserved live credential")
		return etcdstore.OfflineRunnerProof{}, false
	}
	return proof, true
}

func offlineEffectGrantPath(w http.ResponseWriter, r *http.Request, withGrant bool) (string, string, string, bool) {
	planID, attemptID, grantID := chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID"), chi.URLParam(r, "grantID")
	if uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || (withGrant && uuid.Validate(grantID) != nil) {
		handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_offline_effect_path", "plan, attempt, and grant IDs must be UUIDs")
		return "", "", "", false
	}
	return planID, attemptID, grantID, true
}

func offlineEffectEmptyRequest(w http.ResponseWriter, r *http.Request) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64))
	if err != nil || !bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) {
		handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_offline_effect_request", "request body must be exactly an empty object")
		return false
	}
	return true
}

func writeOfflineEffectGrant(w http.ResponseWriter, grant offline.EffectGrant, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Grant            offline.EffectGrant `json:"grant"`
		AuthorizesEffect bool                `json:"authorizesEffect"`
	}{Grant: grant, AuthorizesEffect: false})
}

func offlineEffectGrantError(w http.ResponseWriter, r *http.Request, err error, action string) {
	status, code := http.StatusConflict, "offline_effect_grant_conflict"
	if errors.Is(err, etcdstore.ErrNotFound) {
		status, code = http.StatusNotFound, "offline_effect_grant_not_found"
	} else if !errors.Is(err, offline.ErrEffectGrantMismatch) && !errors.Is(err, etcdstore.ErrOfflineEffectGrantConflict) {
		status, code = http.StatusServiceUnavailable, "offline_effect_grant_uncertain"
	}
	handler.WriteControlProblem(w, r, status, code, "offline effect grant "+action+" failed; recover exact state with GET")
}

func etcdOfflineEffectGrantIssue(grants offlineEffectGrantStore, identities offlineEffectIdentityStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, attemptID, _, ok := offlineEffectGrantPath(w, r, false)
		if !ok || !offlineEffectEmptyRequest(w, r) {
			return
		}
		if grants == nil || identities == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_effect_grant_unavailable", "offline effect grant store is unavailable")
			return
		}
		proof, ok := offlineEffectGrantProof(w, r, identities)
		if !ok {
			return
		}
		grant, err := grants.IssueOfflineEffectGrant(r.Context(), planID, attemptID, proof, time.Now().UTC())
		if err != nil {
			offlineEffectGrantError(w, r, err, "issue")
			return
		}
		writeOfflineEffectGrant(w, grant, http.StatusOK)
	}
}

func etcdOfflineEffectGrantConsume(grants offlineEffectGrantStore, identities offlineEffectIdentityStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, attemptID, grantID, ok := offlineEffectGrantPath(w, r, true)
		if !ok || !offlineEffectEmptyRequest(w, r) {
			return
		}
		if grants == nil || identities == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_effect_grant_unavailable", "offline effect grant store is unavailable")
			return
		}
		proof, ok := offlineEffectGrantProof(w, r, identities)
		if !ok {
			return
		}
		grant, err := grants.ConsumeOfflineEffectGrant(r.Context(), planID, attemptID, grantID, proof, time.Now().UTC())
		if err != nil {
			offlineEffectGrantError(w, r, err, "consume")
			return
		}
		writeOfflineEffectGrant(w, grant, http.StatusOK)
	}
}

func etcdOfflineEffectGrantRecover(grants offlineEffectGrantStore, identities offlineEffectIdentityStore, withGrant bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, attemptID, grantID, ok := offlineEffectGrantPath(w, r, withGrant)
		if !ok {
			return
		}
		if grants == nil || identities == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_effect_grant_unavailable", "offline effect grant store is unavailable")
			return
		}
		proof, ok := offlineEffectGrantProof(w, r, identities)
		if !ok {
			return
		}
		grant, err := grants.RecoverOfflineEffectGrant(r.Context(), planID, attemptID, proof, time.Now().UTC())
		if err != nil {
			offlineEffectGrantError(w, r, err, "recovery")
			return
		}
		if withGrant && grant.ID != grantID {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "offline_effect_grant_not_found", "offline effect grant not found")
			return
		}
		writeOfflineEffectGrant(w, grant, http.StatusOK)
	}
}
