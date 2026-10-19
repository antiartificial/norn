package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
)

// The transport contains no provider client and accepts no runner proof.
// Absence of complete authority-side read credentials leaves issuance off.
func registerEtcdFleetOfflineEffectRoutes(router chi.Router, runnerAuth func(http.Handler) http.Handler, identities offlineIdentityStore, trust *offlineFleetTrust, authority, recovery *etcdstore.OfflineEffectAuthority) {
	base := "/api/v1/fleet/offline/attempts/{planID}/{attemptID}/effect-grants"
	write := func(w http.ResponseWriter, grant offline.EffectGrant, status int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Grant            offline.EffectGrant `json:"grant"`
			AuthorizesEffect bool                `json:"authorizesEffect"`
		}{grant, false})
	}
	valid := func(w http.ResponseWriter, r *http.Request) (etcdstore.OfflineRunnerIdentity, bool) {
		principal, ok := offlineRunnerPrincipal(r, trust, identities)
		if !ok {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_runner_invalid", "a pinned active device-bound runner is required")
			return etcdstore.OfflineRunnerIdentity{}, false
		}
		return etcdstore.OfflineRunnerIdentity{Subject: principal.Subject, TokenID: principal.TokenID, DeviceID: principal.DeviceID}, true
	}
	ready := func(w http.ResponseWriter, r *http.Request) bool {
		if authority == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_effect_observer_unavailable", "independent authority observation is unavailable")
			return false
		}
		return true
	}
	readyRecovery := func(w http.ResponseWriter, r *http.Request) bool {
		if recovery == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_effect_history_unavailable", "durable grant history is unavailable")
			return false
		}
		return true
	}
	emptyRequest := func(w http.ResponseWriter, r *http.Request) bool {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64))
		return err == nil && bytes.Equal(bytes.TrimSpace(body), []byte("{}"))
	}
	router.With(runnerAuth).Post(base, func(w http.ResponseWriter, r *http.Request) {
		principal, ok := valid(w, r)
		if !ok {
			return
		}
		if !ready(w, r) {
			return
		}
		if !emptyRequest(w, r) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_effect_request_invalid", "effect request must be an empty object")
			return
		}
		grant, err := authority.Issue(r.Context(), chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID"), principal, time.Now().UTC())
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_effect_grant_refused", "independent effect observation or shared fence refused issuance")
			return
		}
		write(w, grant, http.StatusOK)
	})
	router.With(runnerAuth).Post(base+"/{grantID}/consume", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := valid(w, r)
		if !ok {
			return
		}
		if !ready(w, r) {
			return
		}
		if !emptyRequest(w, r) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_effect_request_invalid", "effect request must be an empty object")
			return
		}
		grant, err := authority.Consume(r.Context(), chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID"), chi.URLParam(r, "grantID"), principal, time.Now().UTC())
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_effect_consumption_refused", "one-use grant or shared fence refused consumption")
			return
		}
		write(w, grant, http.StatusOK)
	})
	// An issue response may be lost before the runner learns the grant ID.
	// This read recovers the single immutable issue event (and event 2 if
	// consumed) without issuing a second grant.
	router.With(runnerAuth).Get(base, func(w http.ResponseWriter, r *http.Request) {
		principal, ok := valid(w, r)
		if !ok {
			return
		}
		if !readyRecovery(w, r) {
			return
		}
		grant, err := recovery.RecoverAttempt(r.Context(), chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID"), principal)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_effect_grant_unavailable", "immutable grant history is unavailable or differs")
			return
		}
		write(w, grant, http.StatusOK)
	})
	router.With(runnerAuth).Get(base+"/{grantID}", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := valid(w, r)
		if !ok {
			return
		}
		if !readyRecovery(w, r) {
			return
		}
		grant, err := recovery.Recover(r.Context(), chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID"), chi.URLParam(r, "grantID"), principal)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_effect_grant_unavailable", "immutable grant history is unavailable or differs")
			return
		}
		write(w, grant, http.StatusOK)
	})
}

func offlineEffectAuthorityForRuntime(cfg *config.Config, store *etcdstore.V3OperationStore, origin ...*githubapp.Client) *etcdstore.OfflineEffectAuthority {
	if cfg == nil || !cfg.FleetAuthorityOnly || cfg.Production() || cfg.ProfileID() != "staging" ||
		cfg.EnvironmentID() != "staging" || cfg.FleetOfflineEffectConfigFile == "" || len(origin) != 1 || origin[0] == nil {
		return nil
	}
	authority, err := etcdstore.NewOfflineEffectAuthorityFromFile(store, cfg.FleetOfflineEffectConfigFile, origin[0])
	if err != nil {
		return nil
	}
	return authority
}
