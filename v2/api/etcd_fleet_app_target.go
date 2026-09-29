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
	"norn/v2/api/model"
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
		specs, err := model.DiscoverApps(cfg.AppsDir)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "app_catalog_unavailable", "deployable app catalog is unavailable")
			return
		}
		matching := 0
		for _, spec := range specs {
			if spec.App != app {
				continue
			}
			matching++
			regions := spec.ResolvedRegions()
			if len(regions) != 1 {
				handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_app_placement_mismatch", "first Fleet target requires one app region")
				return
			}
			sourceDatacenters := append([]string(nil), regions[0].Datacenters...)
			slices.Sort(sourceDatacenters)
			if regions[0].Name != request.Region || regions[0].NomadRegion != request.NomadRegion || regions[0].TrafficWeight != 100 || !slices.Equal(sourceDatacenters, datacenters) {
				handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_app_placement_mismatch", "target placement differs from the enabled app source")
				return
			}
		}
		if matching != 1 {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_app_source_unavailable", "exactly one enabled app source is required")
			return
		}
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
