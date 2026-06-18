package handler

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/cronexpr"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

type operatorInbox struct {
	GeneratedAt string                     `json:"generatedAt"`
	Summary     operatorInboxSummary       `json:"summary"`
	Items       []operatorInboxItem        `json:"items"`
	Actions     []operatorActionDescriptor `json:"actions"`
}

type operatorInboxSummary struct {
	OpenIncidents      int `json:"openIncidents"`
	ActiveOperations   int `json:"activeOperations"`
	DeployRisks        int `json:"deployRisks"`
	CronRisks          int `json:"cronRisks"`
	SnapshotRisks      int `json:"snapshotRisks"`
	SecretRisks        int `json:"secretRisks"`
	WakeTargets        int `json:"wakeTargets"`
	RecommendedActions int `json:"recommendedActions"`
}

type operatorInboxItem struct {
	ID             string                 `json:"id"`
	Kind           string                 `json:"kind"`
	App            string                 `json:"app,omitempty"`
	Process        string                 `json:"process,omitempty"`
	Severity       string                 `json:"severity"`
	Status         string                 `json:"status,omitempty"`
	Title          string                 `json:"title"`
	Body           string                 `json:"body,omitempty"`
	CorrelationKey string                 `json:"correlationKey,omitempty"`
	DedupeKey      string                 `json:"dedupeKey,omitempty"`
	OccurredAt     string                 `json:"occurredAt,omitempty"`
	Action         string                 `json:"action,omitempty"`
	ActionURL      string                 `json:"actionUrl,omitempty"`
	Evidence       []string               `json:"evidence,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

type operatorCronOverview struct {
	GeneratedAt string              `json:"generatedAt"`
	Entries     []operatorCronEntry `json:"entries"`
}

type operatorCronEntry struct {
	App              string          `json:"app"`
	Process          string          `json:"process"`
	Schedule         string          `json:"schedule"`
	Timezone         string          `json:"timezone"`
	Paused           bool            `json:"paused"`
	Status           string          `json:"status,omitempty"`
	ParentJobID      string          `json:"parentJobId"`
	LastRunAt        string          `json:"lastRunAt,omitempty"`
	LastRunAtLocal   string          `json:"lastRunAtLocal,omitempty"`
	NextRunAt        string          `json:"nextRunAt,omitempty"`
	NextRunAtLocal   string          `json:"nextRunAtLocal,omitempty"`
	ChildrenPending  int64           `json:"childrenPending,omitempty"`
	ChildrenRunning  int64           `json:"childrenRunning,omitempty"`
	ChildrenDead     int64           `json:"childrenDead,omitempty"`
	Runs             []nomad.CronRun `json:"runs,omitempty"`
	Risk             string          `json:"risk,omitempty"`
	Evidence         []string        `json:"evidence,omitempty"`
	ManualTriggerURL string          `json:"manualTriggerUrl"`
	PauseURL         string          `json:"pauseUrl"`
	ResumeURL        string          `json:"resumeUrl"`
}

type operatorWakeTargets struct {
	GeneratedAt string               `json:"generatedAt"`
	Targets     []operatorWakeTarget `json:"targets"`
}

type operatorWakeTarget struct {
	App       string   `json:"app"`
	Process   string   `json:"process"`
	Endpoint  string   `json:"endpoint"`
	Exposure  string   `json:"exposure"`
	Status    string   `json:"status"`
	Instances int      `json:"instances"`
	Ready     bool     `json:"ready"`
	WakeURL   string   `json:"wakeUrl"`
	Evidence  []string `json:"evidence,omitempty"`
}

type operatorDeployConfidence struct {
	GeneratedAt string                        `json:"generatedAt"`
	Apps        []operatorDeployConfidenceApp `json:"apps"`
}

type operatorDeployConfidenceApp struct {
	App             string             `json:"app"`
	Confidence      string             `json:"confidence"`
	Recent          []model.Deployment `json:"recent"`
	LastStatus      string             `json:"lastStatus,omitempty"`
	AutoRollback    bool               `json:"autoRollback"`
	CanaryProcesses []string           `json:"canaryProcesses,omitempty"`
	Evidence        []string           `json:"evidence,omitempty"`
	PreflightURL    string             `json:"preflightUrl"`
	DeployURL       string             `json:"deployUrl"`
}

type operatorSnapshotReadiness struct {
	GeneratedAt string                         `json:"generatedAt"`
	Apps        []operatorSnapshotReadinessApp `json:"apps"`
}

type operatorSnapshotReadinessApp struct {
	App          string         `json:"app"`
	Database     string         `json:"database,omitempty"`
	Status       string         `json:"status"`
	Keep         int            `json:"keep"`
	Count        int            `json:"count"`
	OverLimit    int            `json:"overLimit"`
	Latest       *snapshotEntry `json:"latest,omitempty"`
	RemoteExport bool           `json:"remoteExport"`
	PreRestore   bool           `json:"preRestore"`
	Evidence     []string       `json:"evidence,omitempty"`
	ListURL      string         `json:"listUrl"`
	ExportURL    string         `json:"exportUrl"`
}

type operatorAuthHints struct {
	GeneratedAt string             `json:"generatedAt"`
	Principles  []string           `json:"principles"`
	Patterns    []operatorAuthHint `json:"patterns"`
}

type operatorAuthHint struct {
	Name       string   `json:"name"`
	UseWhen    string   `json:"useWhen"`
	Command    string   `json:"command"`
	Evidence   []string `json:"evidence,omitempty"`
	SecretSafe bool     `json:"secretSafe"`
}

type operatorActionCatalog struct {
	GeneratedAt string                     `json:"generatedAt"`
	Actions     []operatorActionDescriptor `json:"actions"`
}

type operatorActionDescriptor struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	BodySchema  string   `json:"bodySchema,omitempty"`
	Risk        string   `json:"risk"`
	MobileReady bool     `json:"mobileReady"`
	Requires    []string `json:"requires,omitempty"`
}

func (h *Handler) OperatorInbox(w http.ResponseWriter, r *http.Request) {
	inbox := operatorInbox{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Actions:     operatorActions(),
	}
	items := make([]operatorInboxItem, 0)

	incidents, err := h.db.ListActiveIncidents(r.Context(), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, inc := range incidents {
		inbox.Summary.OpenIncidents++
		items = append(items, operatorInboxItem{
			ID:             "incident:" + inc.CorrelationKey,
			Kind:           "incident",
			App:            inc.App,
			Severity:       inc.LatestSeverity,
			Status:         "open",
			Title:          inc.LatestTitle,
			CorrelationKey: inc.CorrelationKey,
			OccurredAt:     inc.LastSeen.Format(time.RFC3339),
			Action:         "inspect",
			ActionURL:      "/api/events/correlated?key=" + inc.CorrelationKey,
			Evidence:       []string{fmt.Sprintf("%d event(s)", inc.EventCount), "latest " + inc.LatestType},
		})
	}

	if h.db != nil && h.db.Pool != nil {
		ops, err := h.db.ListOperations(r.Context(), store.OperationFilter{Limit: 50})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, op := range ops {
			recentFailure := op.Status == model.OperationFailed && time.Since(op.UpdatedAt) <= 24*time.Hour
			if !op.Active() && !recentFailure {
				continue
			}
			severity := "info"
			if op.Status == model.OperationFailed {
				severity = "critical"
			} else if op.Active() {
				severity = "warning"
				inbox.Summary.ActiveOperations++
			}
			items = append(items, operatorInboxItem{
				ID:         "operation:" + op.ID,
				Kind:       "operation",
				App:        op.App,
				Severity:   severity,
				Status:     string(op.Status),
				Title:      op.Kind + " " + string(op.Status),
				Body:       op.Message,
				OccurredAt: op.UpdatedAt.Format(time.RFC3339),
				Action:     "inspect",
				ActionURL:  "/api/operations",
				Evidence:   compactEvidence(op.LastError, fmt.Sprintf("attempts=%d/%d", op.Attempts, op.MaxAttempts)),
			})
		}
	}

	deployConfidence, err := h.buildOperatorDeployConfidence(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, app := range deployConfidence.Apps {
		if app.Confidence == "ready" {
			continue
		}
		inbox.Summary.DeployRisks++
		items = append(items, operatorInboxItem{
			ID:        "deploy:" + app.App,
			Kind:      "deploy_confidence",
			App:       app.App,
			Severity:  severityForConfidence(app.Confidence),
			Status:    app.Confidence,
			Title:     app.App + " deploy confidence " + app.Confidence,
			Action:    "preflight",
			ActionURL: app.PreflightURL,
			Evidence:  app.Evidence,
		})
	}

	cronOverview, err := h.buildOperatorCronOverview(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, entry := range cronOverview.Entries {
		if entry.Risk == "" || entry.Risk == "ok" {
			continue
		}
		inbox.Summary.CronRisks++
		items = append(items, operatorInboxItem{
			ID:        "cron:" + entry.App + ":" + entry.Process,
			Kind:      "cron",
			App:       entry.App,
			Process:   entry.Process,
			Severity:  severityForRisk(entry.Risk),
			Status:    entry.Risk,
			Title:     entry.App + " " + entry.Process + " cron " + entry.Risk,
			Action:    "inspect",
			ActionURL: "/api/operator/cron",
			Evidence:  entry.Evidence,
		})
	}

	snapshotReadiness, err := h.buildOperatorSnapshotReadiness(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, app := range snapshotReadiness.Apps {
		if app.Status == "ready" {
			continue
		}
		inbox.Summary.SnapshotRisks++
		items = append(items, operatorInboxItem{
			ID:        "snapshot:" + app.App,
			Kind:      "snapshot",
			App:       app.App,
			Severity:  severityForRisk(app.Status),
			Status:    app.Status,
			Title:     app.App + " snapshot " + app.Status,
			Action:    "inspect",
			ActionURL: app.ListURL,
			Evidence:  app.Evidence,
		})
	}

	for _, status := range h.operatorSecretStatuses() {
		if status.OK {
			continue
		}
		inbox.Summary.SecretRisks++
		items = append(items, operatorInboxItem{
			ID:        "secrets:" + status.App,
			Kind:      "secrets",
			App:       status.App,
			Severity:  "warning",
			Status:    "needs_attention",
			Title:     status.App + " secrets need attention",
			Action:    "inspect",
			ActionURL: "/api/apps/" + status.App + "/secrets/status",
			Evidence:  compactEvidence(strings.Join(status.MissingEncrypted, ", "), strings.Join(status.PlainEnvWarnings, ", ")),
		})
	}

	wakeTargets, err := h.buildOperatorWakeTargets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	inbox.Summary.WakeTargets = len(wakeTargets.Targets)

	sort.Slice(items, func(i, j int) bool {
		return operatorSeverityRank(items[i].Severity) < operatorSeverityRank(items[j].Severity)
	})
	inbox.Items = items
	inbox.Summary.RecommendedActions = len(items)
	if inbox.Items == nil {
		inbox.Items = []operatorInboxItem{}
	}
	writeJSON(w, inbox)
}

func (h *Handler) OperatorCronOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := h.buildOperatorCronOverview(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, overview)
}

func (h *Handler) OperatorWakeTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := h.buildOperatorWakeTargets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, targets)
}

func (h *Handler) OperatorDeployConfidence(w http.ResponseWriter, r *http.Request) {
	confidence, err := h.buildOperatorDeployConfidence(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, confidence)
}

func (h *Handler) OperatorSnapshotReadiness(w http.ResponseWriter, r *http.Request) {
	readiness, err := h.buildOperatorSnapshotReadiness(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, readiness)
}

func (h *Handler) OperatorAuthHints(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, operatorAuthHints{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Principles: []string{
			"prefer in-process secret use over printing tokens",
			"report presence, length, or short fingerprints instead of raw values",
			"use Mini-local state when operating Mini-hosted Norn services",
		},
		Patterns: []operatorAuthHint{
			{
				Name:       "mini-vigil-gateway",
				UseWhen:    "querying the live Vigil gateway from the Mini",
				Command:    "token=$(nomad alloc exec -t=false -task web <alloc> /bin/sh -lc 'printf %s \"$VIGIL_DEVICE_REGISTRATION_TOKEN\"' 2>/dev/null); curl -H \"Authorization: Bearer $token\" http://127.0.0.1:8144/api/events",
				Evidence:   []string{"-t=false avoids pseudo-TTY env corruption", "do not echo token"},
				SecretSafe: true,
			},
			{
				Name:       "mini-vigil-postgres",
				UseWhen:    "inspecting Vigil event state from the Mini host",
				Command:    "db=$(nomad alloc exec -t=false -task web <alloc> /bin/sh -lc 'printf %s \"$DATABASE_URL\"' 2>/dev/null); db=${db/host.docker.internal/127.0.0.1}; psql \"$db\"",
				Evidence:   []string{"Postgres.app is host-side", "translate container host.docker.internal to 127.0.0.1"},
				SecretSafe: true,
			},
		},
	})
}

func (h *Handler) OperatorActions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, operatorActionCatalog{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Actions:     operatorActions(),
	})
}

func (h *Handler) IncidentAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action         string `json:"action"`
		CorrelationKey string `json:"correlationKey,omitempty"`
		DedupeKey      string `json:"dedupeKey,omitempty"`
		App            string `json:"app,omitempty"`
		By             string `json:"by,omitempty"`
		Note           string `json:"note,omitempty"`
		Duration       string `json:"duration,omitempty"`
		Until          string `json:"until,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.By == "" {
		req.By = "operator"
	}
	key := store.IncidentGroupKey{CorrelationKey: req.CorrelationKey, DedupeKey: req.DedupeKey}
	var affected int
	var err error
	switch req.Action {
	case "acknowledge", "ack":
		affected, err = h.db.AcknowledgeIncidentGroup(r.Context(), key, req.By, req.Note)
	case "snooze":
		until, parseErr := incidentSnoozeUntil(req.Duration, req.Until)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, parseErr.Error())
			return
		}
		affected, err = h.db.SnoozeIncidentGroup(r.Context(), key, req.By, req.Note, until)
	case "open":
		affected, err = h.db.OpenIncidentGroup(r.Context(), key)
	case "resolve":
		affected, err = h.db.AcknowledgeIncidentGroup(r.Context(), key, req.By, req.Note)
		if err == nil {
			if _, emitErr := h.emitIncidentResolution(r, req.App, key, req.By, req.Note); emitErr != nil {
				err = emitErr
			}
		}
	default:
		writeError(w, http.StatusBadRequest, "action must be acknowledge, snooze, open, or resolve")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{
		"action":   req.Action,
		"affected": affected,
	})
}

func (h *Handler) emitIncidentResolution(r *http.Request, app string, key store.IncidentGroupKey, by, note string) (*model.BeaconEvent, error) {
	if h.beacon == nil {
		return nil, nil
	}
	if app == "" {
		app = "norn"
	}
	metadata := map[string]interface{}{
		"resolvedBy": by,
		"resolution": emptyIf(note, "operator resolved incident group"),
	}
	dedupeKey := fmt.Sprintf("%s:incident:resolved:%d", app, time.Now().UnixNano())
	if key.CorrelationKey != "" {
		metadata["correlationKey"] = key.CorrelationKey
	} else if key.DedupeKey != "" {
		dedupeKey = key.DedupeKey
		metadata["resolvedDedupeKey"] = key.DedupeKey
	}
	return h.beacon.Emit(r.Context(), model.BeaconEvent{
		App:       app,
		Type:      "incident.resolved",
		Severity:  model.BeaconInfo,
		Title:     app + " incident resolved",
		Body:      metadata["resolution"].(string),
		DedupeKey: dedupeKey,
		Metadata:  metadata,
	})
}

func (h *Handler) buildOperatorCronOverview(r *http.Request) (operatorCronOverview, error) {
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		return operatorCronOverview{}, err
	}
	out := operatorCronOverview{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, spec := range specs {
		processes := make([]string, 0, len(spec.Processes))
		for name, proc := range spec.Processes {
			if strings.TrimSpace(proc.Schedule) != "" {
				processes = append(processes, name)
			}
		}
		sort.Strings(processes)
		for _, process := range processes {
			proc := spec.Processes[process]
			entry := operatorCronEntry{
				App:              spec.App,
				Process:          process,
				Schedule:         proc.Schedule,
				Timezone:         emptyIf(model.ResolveProcessTimezone(spec, proc), "UTC"),
				ParentJobID:      fmt.Sprintf("%s-%s", spec.App, process),
				ManualTriggerURL: fmt.Sprintf("/api/apps/%s/cron/trigger", spec.App),
				PauseURL:         fmt.Sprintf("/api/apps/%s/cron/pause", spec.App),
				ResumeURL:        fmt.Sprintf("/api/apps/%s/cron/resume", spec.App),
				Risk:             "ok",
			}
			if state, err := h.db.GetCronState(r.Context(), spec.App, process); err == nil {
				entry.Paused = state.Paused
				if state.Schedule != "" {
					entry.Schedule = state.Schedule
				}
			}
			loc := loadOperatorLocation(entry.Timezone)
			if h.nomad != nil {
				info, err := h.nomad.PeriodicJobSchedule(entry.ParentJobID)
				if err != nil {
					entry.Risk = "parent_unavailable"
					entry.Evidence = append(entry.Evidence, err.Error())
				} else {
					entry.Status = info.Status
					entry.Paused = entry.Paused || info.Paused
					if info.Schedule != "" {
						entry.Schedule = info.Schedule
					}
					if info.TimeZone != "" {
						entry.Timezone = info.TimeZone
						loc = loadOperatorLocation(entry.Timezone)
					}
					entry.ChildrenPending = info.ChildrenPending
					entry.ChildrenRunning = info.ChildrenRunning
					entry.ChildrenDead = info.ChildrenDead
					if info.Paused || info.Status == "dead" {
						entry.Risk = "paused"
					}
				}
				if runs, err := h.nomad.PeriodicChildren(entry.ParentJobID); err == nil {
					entry.Runs = runs
					if last := latestCronRunTime(runs, loc); !last.IsZero() {
						entry.LastRunAt = last.UTC().Format(time.RFC3339)
						entry.LastRunAtLocal = formatOperatorLocalTime(last, loc)
					}
				}
			}
			if next := nextCronRun(entry.Schedule, loc); !next.IsZero() {
				entry.NextRunAt = next.UTC().Format(time.RFC3339)
				entry.NextRunAtLocal = formatOperatorLocalTime(next, loc)
			}
			if entry.Paused && entry.Risk == "ok" {
				entry.Risk = "paused"
			}
			out.Entries = append(out.Entries, entry)
		}
	}
	if out.Entries == nil {
		out.Entries = []operatorCronEntry{}
	}
	return out, nil
}

func (h *Handler) buildOperatorWakeTargets() (operatorWakeTargets, error) {
	manifest, err := h.buildServiceManifest()
	if err != nil {
		return operatorWakeTargets{}, err
	}
	out := operatorWakeTargets{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, service := range manifest.Services {
		if len(service.Endpoints) == 0 {
			continue
		}
		for _, endpoint := range service.Endpoints {
			target := operatorWakeTarget{
				App:       service.App,
				Process:   service.Process,
				Endpoint:  endpoint.URL,
				Exposure:  service.Reachability.Exposure,
				Status:    service.Status,
				Instances: len(service.Instances),
				Ready:     service.Status == "passing" && len(service.Instances) > 0,
				WakeURL:   "/api/wake-gateway/" + endpoint.URL,
				Evidence:  []string{"wake gateway preserves Host when routed by hostname"},
			}
			if target.Ready {
				target.Evidence = append(target.Evidence, "ready instance available")
			} else {
				target.Evidence = append(target.Evidence, "wake may scale or wait for readiness")
			}
			out.Targets = append(out.Targets, target)
		}
	}
	if out.Targets == nil {
		out.Targets = []operatorWakeTarget{}
	}
	return out, nil
}

func (h *Handler) buildOperatorDeployConfidence(r *http.Request) (operatorDeployConfidence, error) {
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		return operatorDeployConfidence{}, err
	}
	out := operatorDeployConfidence{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, spec := range specs {
		recent, err := h.db.ListDeployments(r.Context(), spec.App, 5)
		if err != nil {
			return out, err
		}
		app := operatorDeployConfidenceApp{
			App:          spec.App,
			Recent:       recent,
			Confidence:   "ready",
			PreflightURL: fmt.Sprintf("/api/apps/%s/preflight", spec.App),
			DeployURL:    fmt.Sprintf("/api/apps/%s/deploy", spec.App),
		}
		if spec.DeployPolicy != nil {
			app.AutoRollback = spec.DeployPolicy.AutoRollback
		}
		for name, proc := range spec.Processes {
			if proc.Canary != nil {
				app.CanaryProcesses = append(app.CanaryProcesses, name)
			}
		}
		sort.Strings(app.CanaryProcesses)
		if len(recent) == 0 {
			app.Confidence = "unknown"
			app.Evidence = append(app.Evidence, "no recorded deployments")
		} else {
			app.LastStatus = string(recent[0].Status)
			switch recent[0].Status {
			case model.StatusFailed:
				app.Confidence = "blocked"
				app.Evidence = append(app.Evidence, "last deployment failed")
			case model.StatusDeployed:
				app.Evidence = append(app.Evidence, "last deployment succeeded")
			default:
				app.Confidence = "pending"
				app.Evidence = append(app.Evidence, "deployment is in progress")
			}
			failures := 0
			for _, deployment := range recent {
				if deployment.Status == model.StatusFailed {
					failures++
				}
				if deployment.SourceDirty {
					app.Confidence = "caution"
					app.Evidence = appendUniqueEvidence(app.Evidence, "recent deployment came from dirty source")
				}
			}
			if failures >= 2 {
				if recent[0].Status == model.StatusFailed {
					app.Confidence = "blocked"
				} else if app.Confidence == "ready" {
					app.Confidence = "caution"
				}
				app.Evidence = appendUniqueEvidence(app.Evidence, fmt.Sprintf("%d recent failures", failures))
			}
		}
		if app.AutoRollback {
			app.Evidence = append(app.Evidence, "autoRollback enabled")
		}
		out.Apps = append(out.Apps, app)
	}
	return out, nil
}

func (h *Handler) buildOperatorSnapshotReadiness(r *http.Request) (operatorSnapshotReadiness, error) {
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		return operatorSnapshotReadiness{}, err
	}
	out := operatorSnapshotReadiness{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, spec := range specs {
		if spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
			continue
		}
		snaps := listSnapshotsForSpec(spec)
		keep := snapshotKeepForSpec(spec, 3)
		app := operatorSnapshotReadinessApp{
			App:       spec.App,
			Database:  spec.Infrastructure.Postgres.Database,
			Status:    "ready",
			Keep:      keep,
			Count:     len(snaps),
			OverLimit: maxInt(0, len(snaps)-keep),
			ListURL:   fmt.Sprintf("/api/apps/%s/snapshots", spec.App),
			ExportURL: fmt.Sprintf("/api/apps/%s/snapshots/export", spec.App),
		}
		if spec.Snapshots != nil {
			app.RemoteExport = spec.Snapshots.ExportBucket != ""
			app.PreRestore = spec.Snapshots.PreRestore
		}
		if len(snaps) > 0 {
			app.Latest = &snaps[0]
			app.Evidence = append(app.Evidence, "latest snapshot "+snaps[0].Timestamp)
		} else {
			app.Status = "missing"
			app.Evidence = append(app.Evidence, "no local snapshots")
		}
		if app.OverLimit > 0 {
			app.Status = "retention_over_limit"
			app.Evidence = append(app.Evidence, fmt.Sprintf("%d snapshot(s) over retention", app.OverLimit))
		}
		if app.RemoteExport {
			app.Evidence = append(app.Evidence, "remote export configured")
		}
		out.Apps = append(out.Apps, app)
	}
	if out.Apps == nil {
		out.Apps = []operatorSnapshotReadinessApp{}
	}
	return out, nil
}

func (h *Handler) operatorSecretStatuses() []SecretStatus {
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		return nil
	}
	statuses := make([]SecretStatus, 0, len(specs))
	for _, spec := range specs {
		statuses = append(statuses, h.secretStatus(spec))
	}
	return statuses
}

func operatorActions() []operatorActionDescriptor {
	return []operatorActionDescriptor{
		{ID: "incident.acknowledge", Label: "Acknowledge Incident", Method: http.MethodPost, Path: "/api/incidents/action", BodySchema: `{"action":"acknowledge","correlationKey":"...","by":"operator","note":"..."}`, Risk: "low", MobileReady: true},
		{ID: "incident.resolve", Label: "Resolve Incident", Method: http.MethodPost, Path: "/api/incidents/action", BodySchema: `{"action":"resolve","correlationKey":"...","app":"...","by":"operator","note":"..."}`, Risk: "low", MobileReady: true},
		{ID: "incident.snooze", Label: "Snooze Incident", Method: http.MethodPost, Path: "/api/incidents/action", BodySchema: `{"action":"snooze","correlationKey":"...","duration":"1h"}`, Risk: "low", MobileReady: true},
		{ID: "cron.trigger", Label: "Trigger Cron", Method: http.MethodPost, Path: "/api/apps/{id}/cron/trigger", BodySchema: `{"process":"..."}`, Risk: "medium", MobileReady: true},
		{ID: "cron.pause", Label: "Pause Cron", Method: http.MethodPost, Path: "/api/apps/{id}/cron/pause", BodySchema: `{"process":"..."}`, Risk: "medium", MobileReady: true},
		{ID: "deploy.preflight", Label: "Run Preflight", Method: http.MethodPost, Path: "/api/apps/{id}/preflight", BodySchema: `{"ref":"HEAD"}`, Risk: "low", MobileReady: true},
		{ID: "deploy.start", Label: "Deploy", Method: http.MethodPost, Path: "/api/apps/{id}/deploy", BodySchema: `{"ref":"HEAD"}`, Risk: "high", MobileReady: true, Requires: []string{"preflight recommended"}},
		{ID: "snapshot.export", Label: "Export Snapshot", Method: http.MethodPost, Path: "/api/apps/{id}/snapshots/export", Risk: "medium", MobileReady: true},
		{ID: "snapshot.restore", Label: "Restore Snapshot", Method: http.MethodPost, Path: "/api/apps/{id}/snapshots/{ts}/restore", BodySchema: `{"confirm":"<app>"}`, Risk: "high", MobileReady: true, Requires: []string{"confirmation", "preRestore recommended"}},
	}
}

func incidentSnoozeUntil(durationText, untilText string) (time.Time, error) {
	if untilText != "" {
		until, err := time.Parse(time.RFC3339, untilText)
		if err != nil {
			return time.Time{}, fmt.Errorf("until must be RFC3339")
		}
		return until, nil
	}
	if durationText == "" {
		durationText = "1h"
	}
	duration, err := time.ParseDuration(durationText)
	if err != nil || duration <= 0 {
		return time.Time{}, fmt.Errorf("duration must be a positive Go duration such as 1h or 30m")
	}
	return time.Now().UTC().Add(duration), nil
}

func latestCronRunTime(runs []nomad.CronRun, loc *time.Location) time.Time {
	var latest time.Time
	for _, run := range runs {
		t, err := time.Parse(time.RFC3339, run.StartedAt)
		if err != nil {
			continue
		}
		t = t.In(loc)
		if t.After(latest) {
			latest = t
		}
	}
	return latest
}

func nextCronRun(schedule string, loc *time.Location) time.Time {
	var expr *cronexpr.Expression
	func() {
		defer func() { recover() }()
		expr = cronexpr.MustParse(schedule)
	}()
	if expr == nil {
		return time.Time{}
	}
	return expr.Next(time.Now().In(loc))
}

func loadOperatorLocation(timezone string) *time.Location {
	if timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func formatOperatorLocalTime(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("Mon Jan 2, 2006 3:04 PM MST")
}

func severityForRisk(risk string) string {
	switch risk {
	case "blocked", "parent_unavailable", "missing", "retention_over_limit":
		return "critical"
	case "paused", "pending", "unknown", "caution":
		return "warning"
	default:
		return "info"
	}
}

func severityForConfidence(confidence string) string {
	switch confidence {
	case "blocked":
		return "critical"
	case "pending", "unknown", "caution":
		return "warning"
	default:
		return "info"
	}
}

func operatorSeverityRank(severity string) int {
	switch severity {
	case "critical":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}

func compactEvidence(values ...string) []string {
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func appendUniqueEvidence(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
