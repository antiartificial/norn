package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// etcdCanaryPromote is mounted only when both the canary worker and HTTP
// preview are explicitly enabled. Its actor is the durable root of a managed
// token lineage, so rotating a credential cannot create a second acceptance
// namespace for the same Idempotency-Key.
func etcdCanaryPromote(cfg *config.Config, operations *etcdstore.V3OperationStore, identities *etcdstore.AuthStore, runtime *etcdCanaryRuntime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg == nil || operations == nil || identities == nil || runtime == nil || runtime.pipeline == nil || runtime.nomad == nil || !runtime.pipeline.CanaryPromotionAvailable() {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "durable_canary_promotion_unavailable", "durable canary promotion is unavailable")
			return
		}
		app := chi.URLParam(r, "id")
		if !etcdFleetName(app) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app_id", "app ID is invalid")
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" || len(key) > 200 {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "a non-empty Idempotency-Key of at most 200 characters is required")
			return
		}
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.Source != handler.AccessPrincipalSourceManagedToken || principal.TokenID == "" {
			handler.WriteControlProblem(w, r, http.StatusUnauthorized, "unauthorized", "a managed access token is required")
			return
		}
		if principal.CI != nil || !principal.Allows(handler.ScopeAPIWrite) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "canary promotion requires a non-CI api:write operator")
			return
		}
		root, err := identities.RootAccessToken(r.Context(), principal.TokenID)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "operation_actor_ambiguous", "managed token lineage is unavailable")
			return
		}
		authority, err := operations.Authority(r.Context())
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "authority_unavailable", "control authority is unavailable")
			return
		}
		enqueue := pipeline.EnqueueRequest{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/token-lineage", Subject: root}, Key: key,
			Audit:     store.AcceptanceAuditContext{CredentialID: principal.TokenID, DeviceID: principal.DeviceID, Source: "etcd-canary-preview", Scopes: principal.Scopes},
			Semantics: map[string]interface{}{"app": app}}
		accepted, err := runtime.pipeline.ResolveEnqueue(r.Context(), enqueue, "app.canary-promote", app)
		if err == nil {
			region, _ := accepted.Operation.Payload["region"].(string)
			nomadRegion, _ := accepted.Operation.Payload["nomadRegion"].(string)
			deploymentID, _ := accepted.Operation.Payload["deploymentId"].(string)
			if accepted.Operation.Kind != "app.canary-promote" || accepted.Operation.App != app || region == "" || nomadRegion == "" || deploymentID == "" ||
				(r.URL.Query().Get("region") != "" && r.URL.Query().Get("region") != region) {
				handler.WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for different canary promotion work")
				return
			}
			accepted.Operation.AttachReceipt()
			w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
			writeEtcdSourceJSON(w, http.StatusOK, accepted.Operation)
			return
		}
		if !errors.Is(err, store.ErrAcceptanceNotFound) {
			writeEtcdCanaryAcceptanceError(w, r, err)
			return
		}
		logicalRegion, nomadRegion, status, code, message := resolveEtcdCanaryRegion(cfg.AppsDir, app, r.URL.Query().Get("region"))
		if status != 0 {
			handler.WriteControlProblem(w, r, status, code, message)
			return
		}
		info, err := runtime.nomad.LatestDeploymentRegion(app, nomadRegion)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "canary_state_unavailable", "could not determine the current Nomad canary deployment")
			return
		}
		if info == nil || info.ID == "" || info.JobID != app || !info.IsCanary {
			handler.WriteControlProblem(w, r, http.StatusConflict, "no_current_canary", "the current app region has no promotable canary deployment")
			return
		}
		if !info.CanaryReady {
			handler.WriteControlProblem(w, r, http.StatusConflict, "canary_not_ready", "the current Nomad canary allocations are not healthy yet")
			return
		}
		enqueue.Semantics = map[string]interface{}{"app": app, "region": logicalRegion, "nomadRegion": nomadRegion, "deploymentId": info.ID}
		now := time.Now().UTC()
		op := model.Operation{ID: uuid.NewString(), Kind: "app.canary-promote", App: app, SagaID: uuid.NewString(), Ref: logicalRegion + "/" + info.ID,
			Status: model.OperationQueued, Risk: "Nomad canary promotion", Source: "etcd-canary-preview", Message: fmt.Sprintf("queued canary promotion for %s in %s", app, logicalRegion),
			StartedAt: now, MaxAttempts: 1, Payload: map[string]interface{}{"app": app, "region": logicalRegion, "nomadRegion": nomadRegion, "deploymentId": info.ID}}
		accepted, err = runtime.pipeline.QueueOperation(r.Context(), op, enqueue)
		if err != nil {
			writeEtcdCanaryAcceptanceError(w, r, err)
			return
		}
		accepted.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
		status = http.StatusAccepted
		if accepted.Replayed {
			status = http.StatusOK
		}
		writeEtcdSourceJSON(w, status, accepted.Operation)
	}
}

func resolveEtcdCanaryRegion(appsDir, app, requested string) (logical, nomadRegion string, status int, code, message string) {
	specs, err := model.DiscoverApps(appsDir)
	if err != nil {
		return "", "", http.StatusServiceUnavailable, "app_discovery_unavailable", "app intent is unavailable"
	}
	for _, spec := range specs {
		if spec.App != app {
			continue
		}
		regions := spec.ResolvedRegions()
		if requested == "" && len(regions) == 1 {
			requested = regions[0].Name
		}
		for _, region := range regions {
			if region.Name == requested && region.NomadRegion != "" {
				return region.Name, region.NomadRegion, 0, "", ""
			}
		}
		return "", "", http.StatusBadRequest, "invalid_canary_region", "region is required and must be declared for the app"
	}
	return "", "", http.StatusNotFound, "app_not_found", "app was not found"
}

func writeEtcdCanaryAcceptanceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrAcceptanceConflict):
		handler.WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for different canary promotion work")
	case errors.Is(err, store.ErrAcceptanceExpired):
		handler.WriteControlProblem(w, r, http.StatusGone, "operation_replay_expired", "the canary promotion replay window expired")
	case errors.Is(err, store.ErrAcceptanceIndeterminate):
		handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_indeterminate", "operation acceptance is indeterminate; retry with the same Idempotency-Key")
	default:
		handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_failed", "failed to accept canary promotion")
	}
}
