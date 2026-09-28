package main

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type fleetAppTargetRequest struct {
	ExpectedRevision int64    `json:"expectedRevision"`
	Region           string   `json:"region"`
	NomadRegion      string   `json:"nomadRegion"`
	Datacenters      []string `json:"datacenters"`
}

func etcdFleetAppTargetRead(cfg *config.Config, operations *etcdstore.V3OperationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app := chi.URLParam(r, "id")
		if cfg == nil || operations == nil || !etcdFleetName(app) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_app_target", "app ID is invalid")
			return
		}
		target, revision, err := operations.CurrentFleetAppTarget(r.Context(), app, cfg.EnvironmentID())
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "fleet_app_target_unavailable", "Fleet app target is unavailable")
			return
		}
		writeEtcdSourceJSON(w, http.StatusOK, map[string]interface{}{"target": target, "revision": revision})
	}
}

func etcdFleetAppTargetConfigure(cfg *config.Config, operations *etcdstore.V3OperationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app := chi.URLParam(r, "id")
		if cfg == nil || operations == nil || !etcdFleetName(app) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_app_target", "app ID is invalid")
			return
		}
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.TokenID == "" || principal.CI != nil || !principal.Allows(handler.ScopePlatformOperate) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "a non-CI platform operator token is required")
			return
		}
		var request fleetAppTargetRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.ExpectedRevision < 0 || request.Region == "" || request.NomadRegion == "" || len(request.Datacenters) == 0 {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_app_target", "target request is incomplete")
			return
		}
		inventory, err := loadEtcdFleetInventory(cfg)
		if err != nil || !inventory.Configured || inventory.Document == nil || inventory.Validation == nil || !inventory.Validation.Valid ||
			inventory.Document.Metadata.Environment == "" || inventory.Document.Metadata.Environment != cfg.EnvironmentID() ||
			inventory.Document.Cluster.Name == "" || inventory.Document.Cluster.Region == "" {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_not_configured", "a valid Fleet document for this environment is required")
			return
		}
		generation := uint64(1)
		if request.ExpectedRevision > 0 {
			current, revision, err := operations.CurrentFleetAppTarget(r.Context(), app, cfg.EnvironmentID())
			if err != nil || revision != request.ExpectedRevision || current.Generation == ^uint64(0) {
				handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_app_target_changed", "Fleet app target revision changed")
				return
			}
			generation = current.Generation + 1
		}
		datacenters := append([]string(nil), request.Datacenters...)
		slices.Sort(datacenters)
		target := store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: app,
			ControlEnvironment: cfg.EnvironmentID(), Cluster: inventory.Document.Cluster.Name,
			FleetEnvironment: strings.Join([]string{inventory.Document.Metadata.Environment, inventory.Document.Cluster.Region}, "/"),
			Region:           request.Region, NomadRegion: request.NomadRegion, Datacenters: datacenters, Generation: generation}
		revision, err := operations.ConfigureFleetAppTarget(r.Context(), target, request.ExpectedRevision)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_app_target_refused", "Fleet app target was not configured at the expected revision")
			return
		}
		status := http.StatusOK
		if request.ExpectedRevision == 0 {
			status = http.StatusCreated
		}
		writeEtcdSourceJSON(w, status, map[string]interface{}{"target": target, "revision": revision})
	}
}
