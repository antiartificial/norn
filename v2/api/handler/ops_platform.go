package handler

import (
	"net/http"
	"os"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/runtime"
	"norn/v2/api/store"
)

type platformOpsSummary struct {
	GeneratedAt      string                   `json:"generatedAt"`
	NetworkMode      string                   `json:"networkMode,omitempty"`
	ContainerRuntime *runtime.Info             `json:"containerRuntime,omitempty"`
	Services         platformServiceSummary   `json:"services"`
	Deployments      platformDeploySummary    `json:"deployments"`
	Operations       platformOperationSummary `json:"operations"`
	Secrets          platformSecretSummary    `json:"secrets"`
	Snapshots        []platformSnapshotStatus `json:"snapshots"`
	Access           platformAccessSummary    `json:"access"`
	Observability    platformObserveSummary   `json:"observability"`
	Warnings         []string                 `json:"warnings,omitempty"`
}

type platformServiceSummary struct {
	Total    int            `json:"total"`
	ByType   map[string]int `json:"byType"`
	ByStatus map[string]int `json:"byStatus"`
	Public   int            `json:"public"`
	Private  int            `json:"private"`
	Local    int            `json:"local"`
	Internal int            `json:"internal"`
}

type platformDeploySummary struct {
	Recent     []model.Deployment `json:"recent"`
	Dirty      []model.Deployment `json:"dirty"`
	Failed     int                `json:"failed"`
	Successful int                `json:"successful"`
}

type platformOperationSummary struct {
	Recent   []model.Operation `json:"recent"`
	Active   []model.Operation `json:"active"`
	ByKind   map[string]int    `json:"byKind"`
	ByStatus map[string]int    `json:"byStatus"`
}

type platformSecretSummary struct {
	OK             int            `json:"ok"`
	NeedsAttention int            `json:"needsAttention"`
	MigrationItems int            `json:"migrationItems"`
	Apps           []SecretStatus `json:"apps"`
}

type platformSnapshotStatus struct {
	App       string         `json:"app"`
	Database  string         `json:"database,omitempty"`
	Keep      int            `json:"keep"`
	Count     int            `json:"count"`
	OverLimit int            `json:"overLimit"`
	Latest    *snapshotEntry `json:"latest,omitempty"`
}

type platformAccessSummary struct {
	Recent      []AccessEvent  `json:"recent"`
	TotalRecent int            `json:"totalRecent"`
	ByStatus    map[string]int `json:"byStatus"`
	ByClientIP  map[string]int `json:"byClientIp"`
}

type platformObserveSummary struct {
	Enabled         bool   `json:"enabled"`
	LogsEnabled     bool   `json:"logsEnabled"`
	LogFormat       string `json:"logFormat"`
	ServiceName     string `json:"serviceName,omitempty"`
	OTLPEndpoint    string `json:"otlpEndpoint,omitempty"`
	BundleAvailable bool   `json:"bundleAvailable"`
	Retention       string `json:"retention,omitempty"`
}

func (h *Handler) PlatformOps(w http.ResponseWriter, r *http.Request) {
	summary, err := h.buildPlatformOps(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, summary)
}

func (h *Handler) buildPlatformOps(r *http.Request) (platformOpsSummary, error) {
	var rtInfo *runtime.Info
	if h.rt != nil {
		rtInfo = h.rt.Info(r.Context())
	}
	out := platformOpsSummary{
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		NetworkMode:      h.cfg.NetworkMode,
		ContainerRuntime: rtInfo,
		Services: platformServiceSummary{
			ByType:   map[string]int{},
			ByStatus: map[string]int{},
		},
		Access: platformAccessSummary{
			Recent:     []AccessEvent{},
			ByStatus:   map[string]int{},
			ByClientIP: map[string]int{},
		},
		Operations: platformOperationSummary{
			Recent:   []model.Operation{},
			Active:   []model.Operation{},
			ByKind:   map[string]int{},
			ByStatus: map[string]int{},
		},
		Observability: platformObserveSummary{
			Enabled:         truthyEnv("NORN_OTEL_ENABLED"),
			LogsEnabled:     envDefaultTrue("NORN_OTEL_LOGS"),
			LogFormat:       envDefault("NORN_LOG_FORMAT", "text"),
			ServiceName:     os.Getenv("OTEL_SERVICE_NAME"),
			OTLPEndpoint:    os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
			BundleAvailable: true,
			Retention:       "30d or 8GB",
		},
	}

	manifest, err := h.buildServiceManifest()
	if err != nil {
		out.Warnings = append(out.Warnings, "service manifest: "+err.Error())
	} else {
		out.Services = summarizeServices(manifest.Services)
	}

	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		return out, err
	}
	for _, spec := range specs {
		status := h.secretStatus(spec)
		out.Secrets.Apps = append(out.Secrets.Apps, status)
		out.Secrets.MigrationItems += len(h.secretMigrationItems(spec))
		if status.OK {
			out.Secrets.OK++
		} else {
			out.Secrets.NeedsAttention++
			out.Warnings = append(out.Warnings, "secrets need attention: "+status.App)
		}
		if snapshotStatus := summarizeSnapshots(spec); snapshotStatus != nil {
			out.Snapshots = append(out.Snapshots, *snapshotStatus)
			if snapshotStatus.OverLimit > 0 {
				out.Warnings = append(out.Warnings, "snapshot retention over limit: "+snapshotStatus.App)
			}
		}
	}
	if out.Snapshots == nil {
		out.Snapshots = []platformSnapshotStatus{}
	}
	if out.Secrets.Apps == nil {
		out.Secrets.Apps = []SecretStatus{}
	}

	if h.db != nil && h.db.Pool != nil {
		deployments, err := h.db.ListDeployments(r.Context(), "", 20)
		if err != nil {
			out.Warnings = append(out.Warnings, "deployments: "+err.Error())
		} else {
			out.Deployments.Recent = deployments
			for _, deployment := range deployments {
				if deployment.SourceDirty {
					out.Deployments.Dirty = append(out.Deployments.Dirty, deployment)
				}
				switch deployment.Status {
				case model.StatusDeployed:
					out.Deployments.Successful++
				case model.StatusFailed:
					out.Deployments.Failed++
				}
			}
		}
		operations, err := h.db.ListOperations(r.Context(), store.OperationFilter{Limit: 20})
		if err != nil {
			out.Warnings = append(out.Warnings, "operations: "+err.Error())
		} else {
			out.Operations.Recent = operations
			for _, operation := range operations {
				out.Operations.ByKind[operation.Kind]++
				out.Operations.ByStatus[string(operation.Status)]++
				if operation.Active() {
					out.Operations.Active = append(out.Operations.Active, operation)
				}
			}
		}
	}
	if out.Deployments.Recent == nil {
		out.Deployments.Recent = []model.Deployment{}
	}
	if out.Deployments.Dirty == nil {
		out.Deployments.Dirty = []model.Deployment{}
	}

	if h.access != nil {
		out.Access.Recent = h.access.Recent(25)
		out.Access.TotalRecent = len(out.Access.Recent)
		for _, event := range out.Access.Recent {
			out.Access.ByStatus[statusBucket(event.Status)]++
			if event.ClientIP != "" {
				out.Access.ByClientIP[event.ClientIP]++
			}
		}
	}

	return out, nil
}

func summarizeServices(services []model.ServiceManifestEntry) platformServiceSummary {
	out := platformServiceSummary{
		Total:    len(services),
		ByType:   map[string]int{},
		ByStatus: map[string]int{},
	}
	for _, svc := range services {
		out.ByType[svc.Type]++
		out.ByStatus[svc.Status]++
		switch svc.Reachability.Exposure {
		case "public":
			out.Public++
		case "private":
			out.Private++
		case "local":
			out.Local++
		default:
			out.Internal++
		}
	}
	return out
}

func summarizeSnapshots(spec *model.InfraSpec) *platformSnapshotStatus {
	if spec == nil || spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
		return nil
	}
	keep := snapshotKeepForSpec(spec, 3)
	snapshots := listSnapshotsForSpec(spec)
	out := &platformSnapshotStatus{
		App:       spec.App,
		Database:  spec.Infrastructure.Postgres.Database,
		Keep:      keep,
		Count:     len(snapshots),
		OverLimit: maxInt(0, len(snapshots)-keep),
	}
	if len(snapshots) > 0 {
		out.Latest = &snapshots[0]
	}
	return out
}

func snapshotKeepForSpec(spec *model.InfraSpec, fallback int) int {
	if spec != nil && spec.Snapshots != nil && spec.Snapshots.Keep > 0 {
		return spec.Snapshots.Keep
	}
	if fallback > 0 {
		return fallback
	}
	return 3
}

func statusBucket(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "other"
	}
}

func truthyEnv(key string) bool {
	value := os.Getenv(key)
	return value == "1" || value == "true" || value == "TRUE" || value == "yes"
}

func envDefaultTrue(key string) bool {
	value := os.Getenv(key)
	if value == "" {
		return true
	}
	return truthyEnv(key)
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
