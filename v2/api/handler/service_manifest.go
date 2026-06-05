package handler

import (
	"net/http"
	"strings"
	"time"

	"norn/v2/api/consul"
	"norn/v2/api/model"
)

func (h *Handler) ServiceManifest(w http.ResponseWriter, r *http.Request) {
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	manifest := model.ServiceManifest{
		Version:     1,
		GeneratedAt: time.Now().UTC(),
	}

	for _, spec := range specs {
		for processName, process := range spec.Processes {
			serviceName := spec.App + "-" + processName
			processType := manifestProcessType(processName, process)
			entry := model.ServiceManifestEntry{
				Name:     serviceName,
				App:      spec.App,
				Process:  processName,
				Type:     processType,
				Status:   "unknown",
				Metadata: serviceMetadata(spec.App, processName, serviceName),
			}
			if processType == "service" {
				entry.Endpoints = usableEndpoints(spec.Endpoints)
			}

			if process.Health != nil && process.Health.Path != "" {
				entry.HealthPath = process.Health.Path
			} else if process.Port > 0 || (processType == "service" && len(spec.Endpoints) > 0) {
				entry.HealthPath = "/health"
			}

			if h.consul != nil {
				health, err := h.consul.ServiceHealthChecks(serviceName)
				if (err != nil || len(health) == 0) && spec.App != serviceName {
					health, err = h.consul.ServiceHealthChecks(spec.App)
				}
				if err == nil {
					entry.Status = aggregateManifestStatus(health)
					for _, instance := range health {
						entry.Instances = append(entry.Instances, model.ServiceInstance{
							Node:    instance.Node,
							Address: instance.Address,
							Port:    instance.Port,
							Status:  instance.Status,
						})
					}
				}
			}

			manifest.Services = append(manifest.Services, entry)
		}
	}

	writeJSON(w, manifest)
}

func manifestProcessType(name string, process model.Process) string {
	lowerName := strings.ToLower(name)
	lowerCommand := strings.ToLower(process.Command)
	switch {
	case process.Function != nil:
		return "function"
	case process.Schedule != "":
		return "cron"
	case strings.Contains(lowerName, "worker") || strings.Contains(lowerCommand, " worker "):
		return "worker"
	default:
		return "service"
	}
}

func usableEndpoints(endpoints []model.Endpoint) []model.Endpoint {
	usable := make([]model.Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if strings.TrimSpace(endpoint.URL) == "" {
			continue
		}
		usable = append(usable, endpoint)
	}
	return usable
}

func serviceMetadata(app, process, serviceName string) map[string]string {
	metadata := map[string]string{}
	if strings.Contains(serviceName, "mcp") || strings.Contains(app, "mcp") || strings.Contains(process, "mcp") {
		metadata["mcpPath"] = "/mcp"
	}
	return metadata
}

func aggregateManifestStatus(health []consul.ServiceHealth) string {
	if len(health) == 0 {
		return "unknown"
	}
	status := "passing"
	for _, instance := range health {
		switch instance.Status {
		case "critical":
			return "critical"
		case "warning":
			status = "warning"
		}
	}
	return status
}
