package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/cloudflared"
	"norn/v2/api/model"
)

func (h *Handler) Forge(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var spec *model.InfraSpec
	for _, s := range specs {
		if s.App == id {
			spec = s
			break
		}
	}
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}

	if len(spec.Endpoints) == 0 {
		writeJSON(w, map[string]string{"status": "skipped", "reason": "no endpoints"})
		return
	}

	service, err := h.cloudflaredService(spec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	cfg, err := cloudflared.ReadConfig(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
		return
	}

	changed := false
	for _, ep := range spec.Endpoints {
		if cloudflared.AddIngress(cfg, ep.URL, service) {
			changed = true
		}
	}

	if !changed {
		writeJSON(w, map[string]string{"status": "unchanged"})
		return
	}

	if err := cloudflared.ApplyConfig(ctx, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("apply config: %v", err))
		return
	}
	if err := cloudflared.Restart(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("restart: %v", err))
		return
	}

	writeJSON(w, map[string]string{"status": "forged"})
}

func (h *Handler) Teardown(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var spec *model.InfraSpec
	for _, s := range specs {
		if s.App == id {
			spec = s
			break
		}
	}
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}

	if len(spec.Endpoints) == 0 {
		writeJSON(w, map[string]string{"status": "skipped", "reason": "no endpoints"})
		return
	}

	cfg, err := cloudflared.ReadConfig(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
		return
	}

	changed := false
	for _, ep := range spec.Endpoints {
		if cloudflared.RemoveIngress(cfg, ep.URL) {
			changed = true
		}
	}

	if !changed {
		writeJSON(w, map[string]string{"status": "unchanged"})
		return
	}

	if err := cloudflared.ApplyConfig(ctx, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("apply config: %v", err))
		return
	}
	if err := cloudflared.Restart(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("restart: %v", err))
		return
	}

	writeJSON(w, map[string]string{"status": "torn_down"})
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
	ctx := r.Context()

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

	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var spec *model.InfraSpec
	for _, s := range specs {
		if s.App == id {
			spec = s
			break
		}
	}
	if spec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
		return
	}

	// Match bare hostname against endpoint URLs (spec stores full URLs like "https://foo.example.com")
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

	cfg, err := cloudflared.ReadConfig(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
		return
	}

	var changed bool
	if req.Enabled {
		service, err := h.cloudflaredService(spec)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		changed = cloudflared.AddIngress(cfg, matchedURL, service)
	} else {
		changed = cloudflared.RemoveIngress(cfg, matchedURL)
	}

	if !changed {
		writeJSON(w, map[string]string{"status": "unchanged"})
		return
	}

	if err := cloudflared.ApplyConfig(ctx, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("apply config: %v", err))
		return
	}
	if err := cloudflared.Restart(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("restart: %v", err))
		return
	}

	action := "disabled"
	if req.Enabled {
		action = "enabled"
	}
	writeJSON(w, map[string]string{"status": action, "hostname": hostname})
}

func (h *Handler) cloudflaredService(spec *model.InfraSpec) (string, error) {
	processName, process, ok := handlerCloudflaredProcess(spec)
	if !ok {
		return "", fmt.Errorf("no port found in spec")
	}

	serviceName := fmt.Sprintf("%s-%s", spec.App, processName)
	if h.engine != nil {
		addr, err := h.engine.ServiceAddress(serviceName)
		if err == nil {
			return "http://" + addr, nil
		}
	}

	return fmt.Sprintf("http://127.0.0.1:%d", process.Port), nil
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
