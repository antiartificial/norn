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
		if h.replayCloudflaredWithoutSpec(w, r, id, "forge", "") {
			return
		}
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
		if h.replayCloudflaredWithoutSpec(w, r, id, "forge", "") {
			return
		}
		writeJSON(w, map[string]string{"status": "skipped", "reason": "no public endpoints"})
		return
	}
	h.queueCloudflaredMutation(w, r, spec, "forge", hostnames, func() (string, error) { return h.cloudflaredService(spec) })
}

func (h *Handler) Teardown(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	spec := h.findSpec(id)
	if spec == nil {
		if h.replayCloudflaredWithoutSpec(w, r, id, "teardown", "") {
			return
		}
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}
	hostnames := make([]string, 0, len(spec.Endpoints))
	for _, endpoint := range spec.Endpoints {
		hostnames = append(hostnames, endpoint.URL)
	}
	if len(hostnames) == 0 {
		if h.replayCloudflaredWithoutSpec(w, r, id, "teardown", "") {
			return
		}
		writeJSON(w, map[string]string{"status": "skipped", "reason": "no endpoints"})
		return
	}
	h.queueCloudflaredMutation(w, r, spec, "teardown", hostnames, nil)
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
		if h.replayCloudflaredWithoutSpec(w, r, id, cloudflaredToggleAction(req.Enabled), cloudflared.NormalizeHostname(req.Hostname)) {
			return
		}
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
		if h.replayCloudflaredWithoutSpec(w, r, id, cloudflaredToggleAction(req.Enabled), hostname) {
			return
		}
		writeError(w, http.StatusBadRequest, fmt.Sprintf("hostname %s not configured for app %s", hostname, id))
		return
	}
	action := "disable"
	var service func() (string, error)
	if req.Enabled {
		if !cloudflared.IsPublicEndpoint(matchedURL) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("hostname %s is private and cannot be enabled in cloudflared", hostname))
			return
		}
		action = "enable"
		service = func() (string, error) { return h.cloudflaredService(spec) }
	}
	h.queueCloudflaredMutation(w, r, spec, action, []string{matchedURL}, service)
}

func cloudflaredToggleAction(enabled bool) string {
	if enabled {
		return "enable"
	}
	return "disable"
}

// replayCloudflaredWithoutSpec resolves only an existing signed acceptance.
// Forge and teardown carry no request hostname; toggle binds its hostname and
// action before returning the historical receipt. New work still needs a live
// spec and the ordinary mutation preflight.
func (h *Handler) replayCloudflaredWithoutSpec(w http.ResponseWriter, r *http.Request, app, action, hostname string) bool {
	if r.Header.Get("Idempotency-Key") == "" || h.pipeline == nil || !h.pipeline.CloudflaredMutationAvailable() {
		return false
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), nil)
	if !ok {
		return true
	}
	accepted, err := h.pipeline.ResolveEnqueue(r.Context(), enqueue, "app.cloudflared-mutate", app)
	if errors.Is(err, store.ErrAcceptanceNotFound) {
		return false
	}
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return true
	}
	valid := accepted.Operation.Kind == "app.cloudflared-mutate" && accepted.Operation.App == app && accepted.Operation.Payload["action"] == action
	if hostname != "" {
		items, ok := accepted.Operation.Payload["hostnames"].([]interface{})
		valid = valid && ok && len(items) == 1
		if valid {
			stored, ok := items[0].(string)
			valid = ok && cloudflared.NormalizeHostname(stored) == hostname
		}
	}
	if !valid {
		writeOperationAcceptanceError(w, r, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "app.cloudflared-mutate", Resource: app}})
		return true
	}
	accepted.Operation.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	writeJSON(w, accepted.Operation)
	return true
}

func (h *Handler) queueCloudflaredMutation(w http.ResponseWriter, r *http.Request, spec *model.InfraSpec, action string, hostnames []string, resolveService func() (string, error)) {
	if h.pipeline == nil || !h.pipeline.CloudflaredMutationAvailable() {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "durable_ingress_unavailable", "durable local ingress mutation is unavailable")
		return
	}
	sort.Strings(hostnames)
	semantics := map[string]interface{}{"app": spec.App, "action": action, "hostnames": hostnames}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), semantics)
	if !ok {
		return
	}
	if accepted, err := h.pipeline.ResolveEnqueue(r.Context(), enqueue, "app.cloudflared-mutate", spec.App); err == nil {
		if accepted.Operation.Kind != "app.cloudflared-mutate" || accepted.Operation.App != spec.App ||
			accepted.Operation.Payload["action"] != action ||
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
	service := ""
	if resolveService != nil {
		var err error
		service, err = resolveService()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	enqueue.Semantics["service"] = service
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
