package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

func platformReleaseJSON(w http.ResponseWriter, r *http.Request, value interface{}) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_platform_release_request", "invalid JSON request")
		return false
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_platform_release_request", "request must contain one JSON value")
		return false
	}
	return true
}

func platformReleaseResponse(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func platformReleaseError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, etcdstore.ErrPlatformReleaseInvalid):
		handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_platform_release_request", "invalid platform release request")
	case errors.Is(err, etcdstore.ErrPlatformReleaseConflict):
		handler.WriteControlProblem(w, r, http.StatusConflict, "platform_release_conflict", "platform release record changed or conflicts with this request")
	case errors.Is(err, etcdstore.ErrPlatformReleaseNotFound):
		handler.WriteControlProblem(w, r, http.StatusNotFound, "platform_release_not_found", "platform release record not found")
	default:
		handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "platform_release_store_failed", "platform release store unavailable")
	}
}

// platformReleaseExecutorAuth deliberately does not consult generic scopes.
// The enrolled device identity is checked on each request against the durable
// revocation store by VerifyAccessTokenWithIdentityStore.
func platformReleaseExecutorAuth(cfg *config.Config, identities store.IdentityStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			principal, ok := handler.VerifyAccessTokenWithIdentityStore(cfg.APIToken, value, cfg.LegacyTokenSigningUntil, identities)
			if !ok || !handler.IsFleetExecutorPrincipal(*principal) || !fleetLaneSafePath(r) {
				handler.WriteControlProblem(w, r, http.StatusForbidden, "platform_release_executor_required", "an enrolled Fleet executor device is required")
				return
			}
			next.ServeHTTP(w, handler.WithAccessPrincipal(r, principal))
		})
	}
}

func platformReleaseReadOrExecutorAuth(cfg *config.Config, identities store.IdentityStore) func(http.Handler) http.Handler {
	read := etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIRead)
	return func(next http.Handler) http.Handler {
		operatorRead := read(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			principal, ok := handler.VerifyAccessTokenWithIdentityStore(cfg.APIToken, value, cfg.LegacyTokenSigningUntil, identities)
			if ok && handler.IsFleetExecutorPrincipal(*principal) && fleetLaneSafePath(r) {
				next.ServeHTTP(w, handler.WithAccessPrincipal(r, principal))
				return
			}
			operatorRead.ServeHTTP(w, r)
		})
	}
}

func registerEtcdPlatformReleaseIntentRoutes(router chi.Router, cfg *config.Config, identities store.IdentityStore, operations *etcdstore.V3OperationStore) {
	if !cfg.FleetPlatformReleaseIntentEnabled {
		return
	}
	operate := etcdManagedTokenAuth(cfg, identities, handler.ScopePlatformOperate)
	// The platform release effect surface accepts only a live enrolled norn-exec
	// device credential. A generic fleet:operate or admin token cannot impersonate
	// the controller worker or claim its owner lease.
	executorAuth := platformReleaseExecutorAuth(cfg, identities)
	readOrExecutor := platformReleaseReadOrExecutorAuth(cfg, identities)
	requireOwner := func(w http.ResponseWriter, r *http.Request, ownerID string) bool {
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.DeviceID != ownerID {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "platform_release_owner_mismatch", "attempt owner must be this executor device")
			return false
		}
		return true
	}
	root := "/api/v1/fleet/platform-releases"
	router.With(readOrExecutor).Get(root, func(w http.ResponseWriter, r *http.Request) {
		records, err := operations.ListPlatformReleaseIntents(r.Context())
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		platformReleaseResponse(w, http.StatusOK, map[string]interface{}{"items": records})
	})
	router.With(readOrExecutor).Get(root+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		record, err := operations.GetPlatformReleaseIntent(r.Context(), chi.URLParam(r, "name"))
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		platformReleaseResponse(w, http.StatusOK, record)
	})
	router.With(operate).Post(root+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		var request etcdstore.PlatformReleaseIntent
		if !platformReleaseJSON(w, r, &request) {
			return
		}
		name := chi.URLParam(r, "name")
		if request.Name != name {
			platformReleaseError(w, r, etcdstore.ErrPlatformReleaseInvalid)
			return
		}
		record, created, err := operations.EnsurePlatformReleaseIntent(r.Context(), request)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		platformReleaseResponse(w, status, record)
	})
	router.With(operate).Post(root+"/{name}/approval", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			IntentDigest string `json:"intentDigest"`
		}
		if !platformReleaseJSON(w, r, &request) {
			return
		}
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok {
			handler.WriteControlProblem(w, r, http.StatusUnauthorized, "unauthorized", "managed principal required")
			return
		}
		record, err := operations.ApprovePlatformReleaseIntent(r.Context(), chi.URLParam(r, "name"), request.IntentDigest, principal.Subject)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		platformReleaseResponse(w, http.StatusOK, record)
	})
	router.With(executorAuth).Get(root+"/{name}/attempt", func(w http.ResponseWriter, r *http.Request) {
		record, err := operations.GetPlatformReleaseAttempt(r.Context(), chi.URLParam(r, "name"))
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		platformReleaseResponse(w, http.StatusOK, record)
	})
	router.With(executorAuth).Post(root+"/{name}/attempt", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			IntentDigest   string `json:"intentDigest"`
			IdempotencyKey string `json:"idempotencyKey"`
			OwnerID        string `json:"ownerId"`
			OwnerSession   string `json:"ownerSession"`
		}
		if !platformReleaseJSON(w, r, &request) {
			return
		}
		if !requireOwner(w, r, request.OwnerID) {
			return
		}
		record, created, err := operations.StartPlatformReleaseAttempt(r.Context(), chi.URLParam(r, "name"), request.IntentDigest, request.IdempotencyKey, request.OwnerID, request.OwnerSession)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		platformReleaseResponse(w, status, record)
	})
	router.With(executorAuth).Post(root+"/{name}/attempt/renew", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			AttemptID    string `json:"attemptId"`
			OwnerID      string `json:"ownerId"`
			OwnerSession string `json:"ownerSession"`
			FenceEpoch   int64  `json:"fenceEpoch"`
		}
		if !platformReleaseJSON(w, r, &request) {
			return
		}
		if !requireOwner(w, r, request.OwnerID) {
			return
		}
		record, err := operations.RenewPlatformReleaseAttempt(r.Context(), chi.URLParam(r, "name"), request.AttemptID, request.OwnerID, request.OwnerSession, request.FenceEpoch)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		platformReleaseResponse(w, http.StatusOK, record)
	})
	router.With(readOrExecutor).Get(root+"/{name}/attempt/checkpoints", func(w http.ResponseWriter, r *http.Request) {
		records, err := operations.ListPlatformReleaseCheckpoints(r.Context(), chi.URLParam(r, "name"))
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		platformReleaseResponse(w, http.StatusOK, map[string]interface{}{"items": records})
	})
	router.With(executorAuth).Post(root+"/{name}/attempt/checkpoints", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			AttemptID    string                              `json:"attemptId"`
			OwnerID      string                              `json:"ownerId"`
			OwnerSession string                              `json:"ownerSession"`
			FenceEpoch   int64                               `json:"fenceEpoch"`
			Checkpoint   etcdstore.PlatformReleaseCheckpoint `json:"checkpoint"`
		}
		if !platformReleaseJSON(w, r, &request) {
			return
		}
		if !requireOwner(w, r, request.OwnerID) {
			return
		}
		record, err := operations.AppendPlatformReleaseCheckpoint(r.Context(), chi.URLParam(r, "name"), request.AttemptID, request.OwnerID, request.OwnerSession, request.FenceEpoch, request.Checkpoint)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		platformReleaseResponse(w, http.StatusCreated, record)
	})
	router.With(executorAuth).Post(root+"/{name}/attempt/finish", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			AttemptID    string `json:"attemptId"`
			OwnerID      string `json:"ownerId"`
			OwnerSession string `json:"ownerSession"`
			FenceEpoch   int64  `json:"fenceEpoch"`
			Status       string `json:"status"`
		}
		if !platformReleaseJSON(w, r, &request) {
			return
		}
		if !requireOwner(w, r, request.OwnerID) {
			return
		}
		record, err := operations.FinishPlatformReleaseAttempt(r.Context(), chi.URLParam(r, "name"), request.AttemptID, request.OwnerID, request.OwnerSession, request.FenceEpoch, request.Status)
		if err != nil {
			platformReleaseError(w, r, err)
			return
		}
		platformReleaseResponse(w, http.StatusOK, record)
	})
}
