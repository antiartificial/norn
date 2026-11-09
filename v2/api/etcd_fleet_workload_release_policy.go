package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type workloadReleasePolicyRequest struct {
	ExpectedRevision int64                             `json:"expectedRevision"`
	Mode             string                            `json:"mode"`
	MinReplicas      int                               `json:"minReplicas"`
	MaxReplicas      int                               `json:"maxReplicas"`
	SurgeLimit       int                               `json:"surgeLimit"`
	RouteSteps       []store.WorkloadReleasePolicyStep `json:"routeSteps"`
}

func etcdFleetWorkloadReleasePolicyRead(cfg *config.Config, operations *etcdstore.V3OperationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app := chi.URLParam(r, "id")
		if cfg == nil || cfg.EnvironmentID() != "staging" || operations == nil || !etcdFleetName(app) {
			if cfg != nil && cfg.EnvironmentID() != "staging" {
				handler.WriteControlProblem(w, r, http.StatusNotFound, "workload_release_policy_unavailable", "workload release policy is available only in staging")
				return
			}
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_workload_release_policy", "app ID is invalid")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		policy, revision, err := operations.CurrentWorkloadReleasePolicy(r.Context(), app, cfg.EnvironmentID())
		if errors.Is(err, etcdstore.ErrWorkloadReleasePolicyNotFound) {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "workload_release_policy_missing", "no approved workload release policy is configured")
			return
		}
		if errors.Is(err, etcdstore.ErrWorkloadReleasePolicyStale) {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_policy_stale", "workload release policy must be approved for the current Fleet placement")
			return
		}
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "workload_release_policy_unavailable", "workload release policy store is unavailable")
			return
		}
		writeEtcdSourceJSON(w, http.StatusOK, map[string]interface{}{"policy": policy, "revision": revision})
	}
}

func etcdFleetWorkloadReleasePolicyConfigure(cfg *config.Config, operations *etcdstore.V3OperationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app := chi.URLParam(r, "id")
		if cfg == nil || cfg.EnvironmentID() != "staging" || operations == nil || !etcdFleetName(app) {
			if cfg != nil && cfg.EnvironmentID() != "staging" {
				handler.WriteControlProblem(w, r, http.StatusNotFound, "workload_release_policy_unavailable", "workload release policy is available only in staging")
				return
			}
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_workload_release_policy", "app ID is invalid")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.TokenID == "" || principal.Subject == "" || principal.CI != nil || !principal.Allows(handler.ScopePlatformOperate) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "a non-CI platform operator identity is required")
			return
		}
		var request workloadReleasePolicyRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.ExpectedRevision < 0 {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_workload_release_policy", "policy request is malformed or has an invalid revision")
			return
		}
		target, _, err := operations.CurrentFleetAppTarget(r.Context(), app, cfg.EnvironmentID())
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_app_target_unavailable", "a current Fleet app target is required")
			return
		}
		policy := store.WorkloadReleasePolicy{
			App: app, ControlEnvironment: cfg.EnvironmentID(), FleetTargetGeneration: target.Generation,
			SchemaVersion: store.WorkloadReleasePolicySchema, ApprovalID: uuid.NewString(), ApprovedBy: principal.Subject,
			ApprovedAt: time.Now().UTC(), Mode: request.Mode, MinReplicas: request.MinReplicas,
			MaxReplicas: request.MaxReplicas, SurgeLimit: request.SurgeLimit,
			RouteSteps: append([]store.WorkloadReleasePolicyStep(nil), request.RouteSteps...),
		}
		revision, err := operations.ConfigureWorkloadReleasePolicy(r.Context(), policy, request.ExpectedRevision)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_policy_refused", "policy is invalid, stale, or changed during configuration")
			return
		}
		status := http.StatusOK
		if request.ExpectedRevision == 0 {
			status = http.StatusCreated
		}
		writeEtcdSourceJSON(w, status, map[string]interface{}{"policy": policy, "revision": revision})
	}
}
