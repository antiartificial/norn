package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
)

type offlineEvidenceReadStore interface {
	GetOfflineDeleteCommitment(context.Context, string) (offline.DeleteCommitment, []byte, error)
	GetOfflineDeleteDispatchReservation(context.Context, string) ([]byte, error)
	GetOfflineTerminal(context.Context, string) (offline.TerminalEvidence, error)
}

func registerEtcdFleetOfflineEvidenceReadRoutes(router chi.Router, cfg *config.Config, identities offlineIdentityStore, store offlineEvidenceReadStore, trust *offlineFleetTrust) {
	registerEtcdFleetOfflineEvidenceReadRoutesWithAuth(router, etcdManagedTokenAuth(cfg, identities, handler.ScopeFleetVerify), identities, store, trust)
}

func registerEtcdFleetOfflineEvidenceReadRoutesWithAuth(router chi.Router, auth func(http.Handler) http.Handler, identities offlineIdentityStore, store offlineEvidenceReadStore, trust *offlineFleetTrust) {
	if store == nil || trust == nil {
		return
	}
	base := "/api/v1/fleet/offline/attempts/{planID}/{attemptID}"
	valid := func(w http.ResponseWriter, r *http.Request) bool {
		if _, ok := offlineVerifierPrincipal(r, trust, identities); !ok {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_verifier_invalid", "a distinct pinned active device-bound verifier is required")
			return false
		}
		return true
	}
	writeRaw := func(w http.ResponseWriter, raw []byte, field string) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{field: json.RawMessage(raw), "authorizesEffect": false})
	}
	fail := func(w http.ResponseWriter, r *http.Request, err error) {
		status, code := http.StatusConflict, "offline_evidence_corrupt"
		if errors.Is(err, etcdstore.ErrNotFound) {
			status, code = http.StatusNotFound, "offline_evidence_not_found"
		}
		handler.WriteControlProblem(w, r, status, code, "offline evidence is absent or inconsistent")
	}
	router.With(auth).Get(base+"/delete-commitment", func(w http.ResponseWriter, r *http.Request) {
		if !valid(w, r) {
			return
		}
		value, raw, err := store.GetOfflineDeleteCommitment(r.Context(), chi.URLParam(r, "planID"))
		if err != nil {
			fail(w, r, err)
			return
		}
		if value.AttemptID != chi.URLParam(r, "attemptID") {
			fail(w, r, etcdstore.ErrOfflineAttemptCorrupt)
			return
		}
		writeRaw(w, raw, "commitment")
	})
	router.With(auth).Get(base+"/delete-dispatch-reservation", func(w http.ResponseWriter, r *http.Request) {
		if !valid(w, r) {
			return
		}
		raw, err := store.GetOfflineDeleteDispatchReservation(r.Context(), chi.URLParam(r, "planID"))
		if err != nil {
			fail(w, r, err)
			return
		}
		var value struct {
			AttemptID string `json:"attemptId"`
		}
		if json.Unmarshal(raw, &value) != nil || value.AttemptID != chi.URLParam(r, "attemptID") {
			fail(w, r, etcdstore.ErrOfflineAttemptCorrupt)
			return
		}
		writeRaw(w, raw, "reservation")
	})
	router.With(auth).Get(base+"/terminal-evidence", func(w http.ResponseWriter, r *http.Request) {
		if !valid(w, r) {
			return
		}
		value, err := store.GetOfflineTerminal(r.Context(), chi.URLParam(r, "planID"))
		if err != nil {
			fail(w, r, err)
			return
		}
		if value.AttemptID != chi.URLParam(r, "attemptID") {
			fail(w, r, etcdstore.ErrOfflineAttemptCorrupt)
			return
		}
		raw, err := json.Marshal(value)
		if err != nil {
			fail(w, r, err)
			return
		}
		writeRaw(w, raw, "terminalEvidence")
	})
}
