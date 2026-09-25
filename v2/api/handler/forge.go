package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/cloudflared"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func (h *Handler) Forge(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	hostnames := make([]string, 0, len(spec.Endpoints))
	for _, endpoint := range spec.Endpoints {
		if cloudflared.IsPublicEndpoint(endpoint.URL) {
			hostnames = append(hostnames, endpoint.URL)
		}
	}
	if len(hostnames) == 0 {
		writeJSON(w, map[string]string{"status": "skipped", "reason": "no public endpoints"})
		return
	}
	service, err := h.cloudflaredService(spec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.queueCloudflaredMutation(w, r, spec, "forge", hostnames, service)
}

func (h *Handler) Teardown(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	hostnames := make([]string, 0, len(spec.Endpoints))
	for _, endpoint := range spec.Endpoints {
		hostnames = append(hostnames, endpoint.URL)
	}
	if len(hostnames) == 0 {
		writeJSON(w, map[string]string{"status": "skipped", "reason": "no endpoints"})
		return
	}
	h.queueCloudflaredMutation(w, r, spec, "teardown", hostnames, "")
}

func (h *Handler) CloudflaredIngress(w http.ResponseWriter, r *http.Request) {
	cfg, err := cloudflared.ReadConfig(r.Context())
	if err != nil {
		writeJSON(w, map[string]any{"hostnames": []string{}})
		return
	}
	hostnames := []string{}
	for _, rule := range cfg.Ingress {
		if rule.Hostname != "" {
			hostnames = append(hostnames, rule.Hostname)
		}
	}
	writeJSON(w, map[string]any{"hostnames": hostnames})
}

func (h *Handler) ToggleEndpoint(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Hostname string `json:"hostname"`
		Enabled  bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Hostname == "" {
		writeError(w, http.StatusBadRequest, "hostname is required")
		return
	}
	spec := h.findSpec(id)
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	hostname := cloudflared.NormalizeHostname(req.Hostname)
	var matchedURL string
	for _, ep := range spec.Endpoints {
		if cloudflared.NormalizeHostname(ep.URL) == hostname {
			matchedURL = ep.URL
			break
		}
	}
	if matchedURL == "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("hostname %s not configured for app %s", hostname, id))
		return
	}
	action, service := "disable", ""
	if req.Enabled {
		if !cloudflared.IsPublicEndpoint(matchedURL) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("hostname %s is private and cannot be enabled in cloudflared", hostname))
			return
		}
		action = "enable"
		var err error
		service, err = h.cloudflaredService(spec)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	h.queueCloudflaredMutation(w, r, spec, action, []string{matchedURL}, service)
}

func (h *Handler) queueCloudflaredMutation(w http.ResponseWriter, r *http.Request, spec *model.InfraSpec, action string, hostnames []string, service string) {
	if h.pipeline == nil || !h.pipeline.CloudflaredMutationAvailable() {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "durable_ingress_unavailable", "durable local ingress mutation is unavailable")
		return
	}
	sort.Strings(hostnames)
	semantics := map[string]interface{}{"app": spec.App, "action": action, "hostnames": hostnames, "service": service}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), semantics)
	if !ok {
		return
	}
	if accepted, err := h.pipeline.ResolveEnqueue(r.Context(), enqueue, "app.cloudflared-mutate", spec.App); err == nil {
		if accepted.Operation.Kind != "app.cloudflared-mutate" || accepted.Operation.App != spec.App ||
			accepted.Operation.Payload["action"] != action || accepted.Operation.Payload["service"] != service ||
			!sameCloudflaredHostnames(accepted.Operation.Payload["hostnames"], hostnames) {
			writeOperationAcceptanceError(w, r, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "app.cloudflared-mutate", Resource: spec.App}})
			return
		}
		accepted.Operation.AttachReceipt()
		w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
		writeJSON(w, accepted.Operation)
		return
	} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	cfg, before, err := cloudflared.ReadConfigSnapshot(r.Context())
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "ingress_config_unavailable", "local ingress config is unavailable")
		return
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "ingress_host_unavailable", "local ingress host identity is unavailable")
		return
	}
	mutation := cloudflared.Mutation{Action: action, App: spec.App, Host: host, ConfigPath: cloudflared.ConfigPath(), Hostnames: hostnames, Service: service, BeforeDigest: before}
	changed, err := mutation.Apply(cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !changed {
		writeJSON(w, map[string]string{"status": "unchanged"})
		return
	}
	mutation.AfterDigest, err = cloudflared.ConfigDigest(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	payloadBytes, err := json.Marshal(mutation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.cloudflared-mutate", App: spec.App, SagaID: uuid.NewString(), Ref: action, Status: model.OperationQueued, Risk: "local cloudflared config and service restart", Source: "app-control-api", Message: fmt.Sprintf("queued cloudflared %s for %s", action, spec.App), StartedAt: now, MaxAttempts: 3, Payload: payload}
	accepted, err := h.pipeline.QueueOperation(r.Context(), op, enqueue)
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	accepted.Operation.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	if accepted.Replayed {
		writeJSON(w, accepted.Operation)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, accepted.Operation)
}

func sameCloudflaredHostnames(value interface{}, expected []string) bool {
	items, ok := value.([]interface{})
	if !ok || len(items) != len(expected) {
		return false
	}
	for i, item := range items {
		if item != expected[i] {
			return false
		}
	}
	return true
}

func (h *Handler) cloudflaredService(spec *model.InfraSpec) (string, error) {
	processName, process, ok := handlerCloudflaredProcess(spec)
	if !ok {
		return "", fmt.Errorf("no port found in spec")
	}

	serviceName := fmt.Sprintf("%s-%s", spec.App, processName)
	if h.consul != nil {
		instances, err := h.consul.ServiceHealthChecks(serviceName)
		if err == nil {
			for _, instance := range instances {
				if instance.Status == "passing" && instance.Address != "" && instance.Port > 0 {
					return fmt.Sprintf("http://%s:%d", instance.Address, instance.Port), nil
				}
			}
			for _, instance := range instances {
				if instance.Address != "" && instance.Port > 0 {
					return fmt.Sprintf("http://%s:%d", instance.Address, instance.Port), nil
				}
			}
		}
	}

	allocs, err := h.nomad.PollAllocations(spec.App)
	if err != nil {
		return "", fmt.Errorf("poll allocations: %w", err)
	}
	if len(allocs) == 0 {
		return "", fmt.Errorf("no running allocations")
	}
	nodeInfo, err := h.nomad.NodeInfo(allocs[0].NodeID)
	if err != nil {
		return "", fmt.Errorf("node info: %w", err)
	}
	return fmt.Sprintf("http://%s:%d", nodeInfo.Address, process.Port), nil
}

func handlerCloudflaredProcess(spec *model.InfraSpec) (string, model.Process, bool) {
	if process, ok := spec.Processes["web"]; ok && process.Port > 0 {
		return "web", process, true
	}
	names := make([]string, 0, len(spec.Processes))
	for name := range spec.Processes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		process := spec.Processes[name]
		if process.Port > 0 {
			return name, process, true
		}
	}
	return "", model.Process{}, false
}
