package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
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

type etcdScaleRequest struct {
	Group  string `json:"group"`
	Region string `json:"region"`
	Count  int    `json:"count"`
}

func etcdScaleApp(cfg *config.Config, operations *etcdstore.V3OperationStore, identities *etcdstore.AuthStore, runtime *etcdScaleRuntime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if runtime == nil || runtime.pipe == nil || runtime.resolveTarget == nil || !runtime.pipe.ScaleAvailable() {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "durable_scale_unavailable", "durable Fleet scale is unavailable")
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
		if !ok || principal.Source != handler.AccessPrincipalSourceManagedToken || principal.TokenID == "" || principal.CI != nil || !principal.Allows(handler.ScopeAPIWrite) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "Fleet scaling requires a non-CI managed api:write operator")
			return
		}
		var request etcdScaleRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_scale_request", "invalid scale request")
			return
		}
		var trailing json.RawMessage
		if decoder.Decode(&trailing) != io.EOF || !etcdFleetName(request.Group) || request.Count < 0 || (request.Region != "" && !etcdFleetName(request.Region)) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_scale_request", "one group and non-negative count are required")
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
			Audit: store.AcceptanceAuditContext{CredentialID: principal.TokenID, DeviceID: principal.DeviceID, Source: "etcd-scale", Scopes: principal.Scopes}}
		if prior, err := runtime.pipe.ResolveEnqueue(r.Context(), enqueue, "app.scale", app); err == nil {
			var priorCount int
			countJSON, _ := json.Marshal(prior.Operation.Payload["count"])
			countErr := json.Unmarshal(countJSON, &priorCount)
			if prior.Operation.App != app || prior.Operation.Payload["group"] != request.Group || prior.Operation.Payload["requestedRegion"] != request.Region || countErr != nil || priorCount != request.Count {
				handler.WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different scale request")
				return
			}
			prior.Operation.AttachReceipt()
			w.Header().Set("Location", "/api/v1/operations/"+prior.Operation.ID)
			writeEtcdSourceJSON(w, http.StatusOK, prior.Operation)
			return
		} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
			writeEtcdCanaryAcceptanceError(w, r, err)
			return
		}
		specs, err := model.DiscoverApps(cfg.AppsDir)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "app_discovery_unavailable", "app intent is unavailable")
			return
		}
		var spec *model.InfraSpec
		for _, candidate := range specs {
			if candidate.App == app {
				if spec != nil {
					handler.WriteControlProblem(w, r, http.StatusConflict, "duplicate_app", "app intent is duplicated")
					return
				}
				spec = candidate
			}
		}
		if spec == nil || !spec.Deploy {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "deployable app was not found")
			return
		}
		process, found := spec.Processes[request.Group]
		if !found || process.Schedule != "" || process.Function != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "process_not_scalable", "group must be a declared service process")
			return
		}
		if err := validateEtcdScaleCount(cfg, process, request.Count); err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "scale_outside_declared_bounds", err.Error())
			return
		}
		if process.Health == nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "scale_health_check_required", "managed Fleet scaling requires a declared process health check")
			return
		}
		requestedRegion := request.Region
		regions := spec.ResolvedRegions()
		if request.Region == "" && len(regions) == 1 {
			request.Region = regions[0].Name
		}
		nomadRegion := ""
		for _, region := range regions {
			if region.Name == request.Region && spec.ProcessRunsInRegion(process, region.Name) {
				nomadRegion = region.NomadRegion
			}
		}
		if nomadRegion == "" {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_scale_region", "region must be declared for the process")
			return
		}
		target, _, err := operations.CurrentFleetAppTarget(r.Context(), app, cfg.EnvironmentID())
		if err != nil || target.Region != request.Region || target.NomadRegion != nomadRegion {
			handler.WriteControlProblem(w, r, http.StatusConflict, "scale_target_mismatch", "declared app region differs from the active Fleet target")
			return
		}
		active, err := runtime.resolveTarget(r.Context(), app, cfg.EnvironmentID(), request.Region)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "active_scale_target_unavailable", "a completed active Fleet route is required")
			return
		}
		specDigest, err := model.InfraSpecDigest(spec)
		if err != nil || specDigest != active.SpecDigest {
			handler.WriteControlProblem(w, r, http.StatusConflict, "scale_spec_mismatch", "local app intent differs from the accepted active deployment")
			return
		}
		enqueue.Semantics = map[string]interface{}{"app": app, "group": request.Group, "region": request.Region, "count": request.Count, "deploymentId": active.DeploymentID, "nomadJobId": active.JobID, "specDigest": specDigest}
		enqueue.Admission.OneActiveMutablePerApp = true
		now := time.Now().UTC()
		op := model.Operation{ID: uuid.NewString(), Kind: "app.scale", App: app, SagaID: uuid.NewString(), Ref: request.Region + "/" + request.Group,
			Status: model.OperationQueued, Risk: "managed Nomad task-group scale", Source: "etcd-scale", Message: fmt.Sprintf("queued scale for %s/%s process %q to %d", app, request.Region, request.Group, request.Count),
			StartedAt: now, MaxAttempts: 1, Payload: map[string]interface{}{"app": app, "group": request.Group, "region": request.Region, "requestedRegion": requestedRegion, "nomadRegion": nomadRegion, "count": request.Count, "nomadJobId": active.JobID, "deploymentId": active.DeploymentID, "specDigest": specDigest}}
		accepted, err := runtime.pipe.QueueOperation(r.Context(), op, enqueue)
		if err != nil {
			writeEtcdCanaryAcceptanceError(w, r, err)
			return
		}
		accepted.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
		status := http.StatusAccepted
		if accepted.Replayed {
			status = http.StatusOK
		}
		writeEtcdSourceJSON(w, status, accepted.Operation)
	}
}

func validateEtcdScaleCount(cfg *config.Config, process model.Process, count int) error {
	minimum, maximum := 1, process.DeclaredReplicaCount()
	if process.Scaling != nil {
		if process.Scaling.Min > 0 {
			minimum = process.Scaling.Min
		}
		if process.Scaling.Max > 0 {
			maximum = process.Scaling.Max
		}
	}
	if maximum < minimum || count < minimum || count > maximum {
		return fmt.Errorf("requested replicas must be within declared minimum %d and maximum %d", minimum, maximum)
	}
	if process.HostPort > 0 && count > 1 {
		return fmt.Errorf("a fixed hostPort cannot admit multiple allocations in one region")
	}
	if process.Placement != nil && process.Placement.DistinctHosts && cfg != nil && strings.TrimSpace(cfg.FleetConfig) != "" {
		inventory, err := loadEtcdFleetInventory(cfg)
		if err != nil || inventory == nil || inventory.Validation == nil || !inventory.Validation.Valid {
			return fmt.Errorf("Fleet capacity policy is unavailable")
		}
		pool, ok := inventory.NodePools["app"]
		if !ok || count > pool.Desired {
			return fmt.Errorf("requested replicas exceed the declared app node pool under distinct-host placement")
		}
	}
	return nil
}

func etcdScaleStatus(cfg *config.Config, operations *etcdstore.V3OperationStore, runtime *etcdScaleRuntime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if runtime == nil || runtime.nomad == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "scale_status_unavailable", "Nomad scale status is unavailable")
			return
		}
		app := chi.URLParam(r, "id")
		specs, err := model.DiscoverApps(cfg.AppsDir)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "app_discovery_unavailable", "app intent is unavailable")
			return
		}
		var spec *model.InfraSpec
		for _, candidate := range specs {
			if candidate.App == app {
				spec = candidate
				break
			}
		}
		if spec == nil {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "app was not found")
			return
		}
		target, _, err := operations.CurrentFleetAppTarget(r.Context(), app, cfg.EnvironmentID())
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "scale_target_unavailable", "Fleet app target is unavailable")
			return
		}
		rows := make([]model.ProcessScaleStatus, 0)
		for _, region := range spec.ResolvedRegions() {
			if region.Name != target.Region || region.NomadRegion != target.NomadRegion {
				continue
			}
			active, err := runtime.resolveTarget(r.Context(), app, cfg.EnvironmentID(), region.Name)
			if err != nil {
				handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "active_scale_target_unavailable", "active Fleet route is unavailable")
				return
			}
			accepted, err := operations.DesiredReplicaCounts(r.Context(), app, region.Name)
			if err != nil {
				handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "scale_intent_unavailable", "accepted replica intent is unavailable")
				return
			}
			observed, err := runtime.nomad.JobScaleStatusRegion(active.JobID, region.NomadRegion)
			if err != nil || observed == nil {
				handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "nomad_scale_status_unavailable", "Nomad scale status is unavailable")
				return
			}
			names := make([]string, 0, len(spec.Processes))
			for name := range spec.Processes {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				proc := spec.Processes[name]
				if proc.Schedule != "" || proc.Function != nil || !spec.ProcessRunsInRegion(proc, region.Name) {
					continue
				}
				declared := proc.DeclaredReplicaCount()
				row := model.ProcessScaleStatus{Region: region.Name, NomadRegion: region.NomadRegion, Process: name, Declared: declared, Desired: declared, IntentSource: "declared"}
				if count, ok := accepted[name]; ok {
					row.Desired, row.IntentSource = count, "accepted-scale"
				}
				if group, ok := observed.TaskGroups[name]; ok {
					row.NomadPresent = true
					row.NomadDesired, row.Placed, row.Running, row.Healthy = &group.Desired, &group.Placed, &group.Running, &group.Healthy
				}
				rows = append(rows, row)
			}
		}
		writeEtcdSourceJSON(w, http.StatusOK, rows)
	}
}

func currentEtcdScaleSpecDigest(appsDir, app string) (string, error) {
	specs, err := model.DiscoverApps(appsDir)
	if err != nil {
		return "", err
	}
	var matched *model.InfraSpec
	for _, candidate := range specs {
		if candidate.App == app {
			if matched != nil {
				return "", fmt.Errorf("app intent is duplicated")
			}
			matched = candidate
		}
	}
	if matched == nil || !matched.Deploy {
		return "", fmt.Errorf("deployable app intent is unavailable")
	}
	return model.InfraSpecDigest(matched)
}

func requireCurrentEtcdScaleSpecDigest(appsDir, app, accepted string) (string, error) {
	digest, err := currentEtcdScaleSpecDigest(appsDir, app)
	if err != nil {
		return "", err
	}
	if digest != accepted {
		return "", fmt.Errorf("local app intent differs from the accepted active deployment")
	}
	return digest, nil
}
