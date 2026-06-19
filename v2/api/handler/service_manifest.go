package handler

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"norn/v2/api/consul"
	"norn/v2/api/model"
)

func (h *Handler) ServiceManifest(w http.ResponseWriter, r *http.Request) {
	manifest, err := h.buildServiceManifest()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, manifest)
}

func (h *Handler) buildServiceManifest() (model.ServiceManifest, error) {
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		return model.ServiceManifest{}, err
	}

	manifest := model.ServiceManifest{
		Version:     1,
		GeneratedAt: time.Now().UTC(),
		NetworkMode: h.cfg.NetworkMode,
		Contract: model.ServiceManifestContract{
			Schema:             "norn.service-manifest.v1",
			ProcessTypes:       []string{"service", "worker", "cron", "function"},
			ReachabilityScopes: []string{"none", "local", "private", "public"},
		},
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
			entry.Metadata["networkMode"] = h.cfg.NetworkMode
			entry.Metadata["instanceScope"] = "none"
			if processType == "service" {
				entry.Endpoints = usableEndpoints(spec.Endpoints)
				entry.Metadata["endpointScope"] = endpointScope(entry.Endpoints)
			} else {
				entry.Metadata["endpointScope"] = "none"
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
					entry.Metadata["instanceScope"] = instanceScope(entry.Instances)
				}
			}
			entry.Metrics = h.serviceMetrics(spec.App, processName, process, entry.Instances)
			entry.Reachability = serviceReachability(entry.Metadata["endpointScope"], entry.Metadata["instanceScope"])

			manifest.Services = append(manifest.Services, entry)
		}
	}

	return manifest, nil
}

func (h *Handler) serviceMetrics(app, processName string, process model.Process, fallbackInstances []model.ServiceInstance) *model.ServiceMetrics {
	if process.Metrics == nil || !process.Metrics.Enabled {
		return nil
	}
	path := process.Metrics.Path
	if path == "" {
		path = "/metrics"
	}
	serviceName := fmt.Sprintf("%s-%s-metrics", app, processName)
	metrics := &model.ServiceMetrics{
		Enabled:     true,
		Path:        path,
		ServiceName: serviceName,
	}
	if h.consul != nil {
		if health, err := h.consul.ServiceHealthChecks(serviceName); err == nil {
			for _, instance := range health {
				metrics.Instances = append(metrics.Instances, model.ServiceInstance{
					Node:    instance.Node,
					Address: instance.Address,
					Port:    instance.Port,
					Status:  instance.Status,
				})
			}
		}
	}
	if len(metrics.Instances) == 0 && process.Metrics.Port == 0 && process.Port > 0 {
		metrics.Instances = append(metrics.Instances, fallbackInstances...)
	}
	metrics.Reachability = serviceReachability("none", instanceScope(metrics.Instances))
	return metrics
}

func serviceReachability(endpointScope, instanceScope string) model.ServiceReachability {
	if endpointScope == "" {
		endpointScope = "none"
	}
	if instanceScope == "" {
		instanceScope = "none"
	}
	exposure := endpointScope
	if exposure == "none" {
		exposure = "internal"
	}
	return model.ServiceReachability{
		EndpointScope: endpointScope,
		InstanceScope: instanceScope,
		Exposure:      exposure,
		Routable:      endpointScope != "none" || instanceScope != "none",
	}
}

func endpointScope(endpoints []model.Endpoint) string {
	if len(endpoints) == 0 {
		return "none"
	}
	scope := "public"
	for _, endpoint := range endpoints {
		host := endpointHostname(endpoint.URL)
		switch classifyHostScope(host) {
		case "local":
			return "local"
		case "private":
			scope = "private"
		}
	}
	return scope
}

func endpointHostname(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
		return strings.ToLower(parsed.Hostname())
	}
	if strings.Contains(raw, "://") || strings.ContainsAny(raw, "/?#") {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(raw), "[]"), "."))
}

func instanceScope(instances []model.ServiceInstance) string {
	if len(instances) == 0 {
		return "none"
	}
	scope := "public"
	for _, instance := range instances {
		switch classifyHostScope(instance.Address) {
		case "local":
			return "local"
		case "private":
			scope = "private"
		}
	}
	return scope
}

func classifyHostScope(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || host == "localhost" {
		return "local"
	}
	if strings.HasSuffix(host, ".ts.net") {
		return "private"
	}
	if strings.HasSuffix(host, ".norn") {
		return "private"
	}
	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsLoopback():
			return "local"
		case ip.IsPrivate() || isTailnetIP(ip):
			return "private"
		default:
			return "public"
		}
	}
	return "public"
}

func isTailnetIP(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 64
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
