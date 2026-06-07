package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
)

type contextDBOpsSummary struct {
	GeneratedAt    string                       `json:"generatedAt"`
	App            *model.AppStatus             `json:"app,omitempty"`
	Services       []model.ServiceManifestEntry `json:"services"`
	WebURL         string                       `json:"webUrl,omitempty"`
	WorkerURL      string                       `json:"workerUrl,omitempty"`
	Worker         *contextDBWorkerStatus       `json:"worker,omitempty"`
	ProviderGate   contextDBProviderGate        `json:"providerGate"`
	Queue          contextDBReviewQueueResult   `json:"queue"`
	WorkerRuns     []contextDBWorkerRun         `json:"workerRuns"`
	FeedbackEvents []contextDBFeedbackEvent     `json:"feedbackEvents"`
	Rollbacks      []contextDBFeedbackRollback  `json:"rollbacks"`
	Snapshots      []snapshotEntry              `json:"snapshots"`
	Deployments    []model.Deployment           `json:"deployments"`
	Secrets        *SecretStatus                `json:"secrets,omitempty"`
	AccessEvents   []AccessEvent                `json:"accessEvents"`
	SagaEvents     any                          `json:"sagaEvents,omitempty"`
	Warnings       []string                     `json:"warnings,omitempty"`
}

type contextDBProviderGate struct {
	Ready               bool   `json:"ready"`
	Reason              string `json:"reason,omitempty"`
	ProviderBacked      int    `json:"providerBacked"`
	MutationEnabled     int    `json:"mutationEnabled"`
	MissingProviderKeys int    `json:"missingProviderKeys"`
	Warnings            int    `json:"warnings"`
	Errors              int    `json:"errors"`
}

type contextDBReviewQueueResult struct {
	Items []contextDBReviewItem `json:"items"`
	Total int                   `json:"total"`
	Error string                `json:"error,omitempty"`
}

type contextDBWorkerStatus struct {
	Status string                `json:"status"`
	Worker string                `json:"worker"`
	DryRun bool                  `json:"dry_run"`
	Policy contextDBPolicyReport `json:"policy"`
}

type contextDBPolicyReport struct {
	GeneratedAt string                     `json:"generated_at"`
	DryRun      bool                       `json:"dry_run"`
	Namespaces  []contextDBNamespacePolicy `json:"namespaces"`
	Totals      contextDBPolicyTotals      `json:"totals"`
}

type contextDBNamespacePolicy struct {
	Namespace             string   `json:"namespace"`
	Mode                  string   `json:"mode"`
	PolicyPreset          string   `json:"policy_preset"`
	DryRun                bool     `json:"dry_run"`
	Evaluator             string   `json:"evaluator"`
	Provider              string   `json:"provider"`
	ProviderKeyRequired   bool     `json:"provider_key_required"`
	ProviderKeyConfigured bool     `json:"provider_key_configured"`
	AllowedActions        []string `json:"allowed_actions"`
	MutationAllowed       bool     `json:"mutation_allowed"`
	Warnings              []string `json:"warnings"`
	OK                    bool     `json:"ok"`
	Error                 string   `json:"error"`
}

type contextDBPolicyTotals struct {
	Namespaces          int `json:"namespaces"`
	MutationEnabled     int `json:"mutation_enabled"`
	ProviderBacked      int `json:"provider_backed"`
	MissingProviderKeys int `json:"missing_provider_keys"`
	Warnings            int `json:"warnings"`
	Errors              int `json:"errors"`
}

type contextDBReviewItem struct {
	ReviewID string `json:"review_id"`
	NodeID   string `json:"node_id"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Owner    string `json:"owner"`
	Reason   string `json:"reason"`
}

type contextDBWorkerRun struct {
	CycleID     string                       `json:"cycle_id"`
	Namespace   string                       `json:"namespace"`
	Mode        string                       `json:"mode"`
	Evaluator   string                       `json:"evaluator"`
	DryRun      bool                         `json:"dry_run"`
	Scanned     int                          `json:"scanned"`
	Applied     int                          `json:"applied"`
	Skipped     int                          `json:"skipped"`
	Errors      int                          `json:"errors"`
	GeneratedAt string                       `json:"generated_at"`
	Decisions   []contextDBWorkerRunDecision `json:"decisions,omitempty"`
}

type contextDBWorkerRunDecision struct {
	ReviewID              string `json:"review_id"`
	NodeID                string `json:"node_id"`
	Type                  string `json:"type"`
	Action                string `json:"action"`
	Applied               bool   `json:"applied"`
	Reason                string `json:"reason"`
	FeedbackEventID       string `json:"feedback_event_id,omitempty"`
	ReviewDecisionEventID string `json:"review_decision_event_id,omitempty"`
}

type contextDBFeedbackEvent struct {
	EventID    string  `json:"event_id"`
	Namespace  string  `json:"namespace"`
	NodeID     string  `json:"node_id"`
	Action     string  `json:"action"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
	TxTime     string  `json:"tx_time"`
}

type contextDBFeedbackRollback struct {
	EventID            string  `json:"event_id"`
	RolledBackEventID  string  `json:"rolled_back_event_id"`
	NodeID             string  `json:"node_id"`
	Action             string  `json:"action"`
	PreviousConfidence float64 `json:"previous_confidence"`
	RestoredConfidence float64 `json:"restored_confidence"`
	Reason             string  `json:"reason"`
	Owner              string  `json:"owner"`
	TxTime             string  `json:"tx_time"`
}

func (h *Handler) ContextDBOps(w http.ResponseWriter, r *http.Request) {
	const appID = "contextdb"
	namespace := queryDefault(r, "namespace", "hermes-agent")
	mode := queryDefault(r, "mode", "agent_memory")
	limit := queryIntDefault(r, "limit", 10)

	spec := h.findSpec(appID)
	if spec == nil {
		writeError(w, http.StatusNotFound, "contextdb app not found")
		return
	}

	out := contextDBOpsSummary{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Warnings:    []string{},
	}
	out.Secrets = ptrSecretStatus(h.secretStatus(spec))

	if h.nomad != nil {
		status := model.AppStatus{Spec: spec}
		if jobStatus, err := h.nomad.JobStatus(spec.App); err == nil {
			status.NomadStatus = jobStatus
		}
		if allocs, err := h.nomad.JobAllocations(spec.App); err == nil {
			status.Allocations = enrichAllocations(allocs, h.nomad)
			for _, alloc := range allocs {
				if alloc.ClientStatus == "running" && alloc.DeploymentStatus != nil && alloc.DeploymentStatus.Healthy != nil && *alloc.DeploymentStatus.Healthy {
					status.Healthy = true
					break
				}
			}
		}
		out.App = &status
	}

	if manifest, err := h.buildServiceManifest(); err == nil {
		for _, svc := range manifest.Services {
			if svc.App != appID {
				continue
			}
			out.Services = append(out.Services, svc)
			if svc.Process == "web" {
				out.WebURL = firstReachableServiceURL(svc)
			}
			if svc.Process == "review-worker" {
				out.WorkerURL = firstReachableServiceURL(svc)
			}
		}
	} else {
		out.Warnings = append(out.Warnings, "service manifest: "+err.Error())
	}

	if out.WebURL != "" {
		httpClient := &http.Client{Timeout: 5 * time.Second}
		if err := getContextDBJSON(httpClient, contextDBReviewQueueURL(out.WebURL, namespace, mode, limit), &out.Queue); err != nil {
			out.Queue.Error = err.Error()
		}
		out.Queue.Total = len(out.Queue.Items)
		var runs struct {
			Runs []contextDBWorkerRun `json:"runs"`
		}
		if err := getContextDBJSON(httpClient, contextDBWorkerRunsURL(out.WebURL, namespace, mode), &runs); err != nil {
			out.Warnings = append(out.Warnings, "worker runs: "+err.Error())
		} else {
			out.WorkerRuns = runs.Runs
			sort.Slice(out.WorkerRuns, func(i, j int) bool {
				return out.WorkerRuns[i].GeneratedAt > out.WorkerRuns[j].GeneratedAt
			})
			if len(out.WorkerRuns) > limit {
				out.WorkerRuns = out.WorkerRuns[:limit]
			}
		}
		var events struct {
			Events []contextDBFeedbackEvent `json:"events"`
		}
		if err := getContextDBJSON(httpClient, contextDBFeedbackEventsURL(out.WebURL, namespace, mode), &events); err != nil {
			out.Warnings = append(out.Warnings, "feedback events: "+err.Error())
		} else {
			out.FeedbackEvents = events.Events
			sort.Slice(out.FeedbackEvents, func(i, j int) bool {
				return out.FeedbackEvents[i].TxTime > out.FeedbackEvents[j].TxTime
			})
			if len(out.FeedbackEvents) > limit {
				out.FeedbackEvents = out.FeedbackEvents[:limit]
			}
		}
		var rollbacks struct {
			Receipts []contextDBFeedbackRollback `json:"receipts"`
		}
		if err := getContextDBJSON(httpClient, contextDBFeedbackRollbacksURL(out.WebURL, namespace, mode), &rollbacks); err != nil {
			out.Warnings = append(out.Warnings, "feedback rollbacks: "+err.Error())
		} else {
			out.Rollbacks = rollbacks.Receipts
			sort.Slice(out.Rollbacks, func(i, j int) bool {
				return out.Rollbacks[i].TxTime > out.Rollbacks[j].TxTime
			})
			if len(out.Rollbacks) > limit {
				out.Rollbacks = out.Rollbacks[:limit]
			}
		}
	}

	if out.WorkerURL != "" {
		httpClient := &http.Client{Timeout: 5 * time.Second}
		var status contextDBWorkerStatus
		if err := getContextDBJSON(httpClient, strings.TrimRight(out.WorkerURL, "/")+"/v1/status", &status); err != nil {
			out.Warnings = append(out.Warnings, "worker status: "+err.Error())
		} else {
			out.Worker = &status
			out.ProviderGate = providerGateFromPolicy(status.Policy)
		}
	}

	out.Snapshots = listSnapshotsForSpec(spec)
	if h.db != nil {
		if deployments, err := h.db.ListDeployments(r.Context(), appID, 5); err == nil {
			out.Deployments = deployments
		} else {
			out.Warnings = append(out.Warnings, "deployments: "+err.Error())
		}
	}
	if h.access != nil {
		out.AccessEvents = h.access.Recent(10)
	}
	if h.sagaStore != nil {
		if events, err := h.sagaStore.ListByApp(r.Context(), appID, 10); err == nil {
			out.SagaEvents = events
		} else {
			out.Warnings = append(out.Warnings, "saga: "+err.Error())
		}
	}

	writeJSON(w, out)
}

func (h *Handler) ContextDBRollbackFeedback(w http.ResponseWriter, r *http.Request) {
	namespace := queryDefault(r, "namespace", "hermes-agent")
	mode := queryDefault(r, "mode", "agent_memory")
	eventID := chi.URLParam(r, "eventID")
	if eventID == "" {
		writeError(w, http.StatusBadRequest, "event id is required")
		return
	}
	var req struct {
		Reason string `json:"reason"`
		Owner  string `json:"owner"`
	}
	_ = decodeJSON(r, &req)
	if strings.TrimSpace(req.Owner) == "" {
		req.Owner = "norn"
	}
	manifest, err := h.buildServiceManifest()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	webURL := ""
	for _, svc := range manifest.Services {
		if svc.App == "contextdb" && svc.Process == "web" {
			webURL = firstReachableServiceURL(svc)
			break
		}
	}
	if webURL == "" {
		writeError(w, http.StatusBadGateway, "contextdb web service unavailable")
		return
	}
	payload, _ := json.Marshal(map[string]string{
		"mode":   mode,
		"reason": req.Reason,
		"owner":  req.Owner,
	})
	target := fmt.Sprintf("%s/v1/namespaces/%s/feedback/events/%s/rollback",
		strings.TrimRight(webURL, "/"), url.PathEscape(namespace), url.PathEscape(eventID))
	resp, err := http.Post(target, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeError(w, resp.StatusCode, fmt.Sprintf("contextdb rollback failed: HTTP %d", resp.StatusCode))
		return
	}
	var receipt contextDBFeedbackRollback
	if err := json.NewDecoder(resp.Body).Decode(&receipt); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, receipt)
}

func providerGateFromPolicy(policy contextDBPolicyReport) contextDBProviderGate {
	totals := policy.Totals
	gate := contextDBProviderGate{
		Ready:               true,
		ProviderBacked:      totals.ProviderBacked,
		MutationEnabled:     totals.MutationEnabled,
		MissingProviderKeys: totals.MissingProviderKeys,
		Warnings:            totals.Warnings,
		Errors:              totals.Errors,
	}
	switch {
	case totals.Errors > 0:
		gate.Ready = false
		gate.Reason = "policy has errors"
	case totals.MissingProviderKeys > 0:
		gate.Ready = false
		gate.Reason = "provider key missing"
	case totals.Warnings > 0:
		gate.Ready = false
		gate.Reason = "policy has warnings"
	case totals.ProviderBacked == 0:
		gate.Ready = false
		gate.Reason = "rules evaluator only"
	}
	return gate
}

func firstReachableServiceURL(svc model.ServiceManifestEntry) string {
	for _, endpoint := range svc.Endpoints {
		if strings.TrimSpace(endpoint.URL) != "" {
			return strings.TrimRight(endpoint.URL, "/")
		}
	}
	for _, inst := range svc.Instances {
		if inst.Address != "" && inst.Port > 0 {
			return fmt.Sprintf("http://%s:%d", inst.Address, inst.Port)
		}
	}
	return ""
}

func contextDBReviewQueueURL(base, namespace, mode string, limit int) string {
	values := url.Values{}
	values.Set("mode", mode)
	values.Set("limit", fmt.Sprintf("%d", limit))
	return fmt.Sprintf("%s/v1/namespaces/%s/review/queue?%s", strings.TrimRight(base, "/"), url.PathEscape(namespace), values.Encode())
}

func contextDBWorkerRunsURL(base, namespace, mode string) string {
	values := url.Values{}
	values.Set("mode", mode)
	return fmt.Sprintf("%s/v1/namespaces/%s/review/worker-runs?%s", strings.TrimRight(base, "/"), url.PathEscape(namespace), values.Encode())
}

func contextDBFeedbackEventsURL(base, namespace, mode string) string {
	values := url.Values{}
	values.Set("mode", mode)
	return fmt.Sprintf("%s/v1/namespaces/%s/feedback/events?%s", strings.TrimRight(base, "/"), url.PathEscape(namespace), values.Encode())
}

func contextDBFeedbackRollbacksURL(base, namespace, mode string) string {
	values := url.Values{}
	values.Set("mode", mode)
	return fmt.Sprintf("%s/v1/namespaces/%s/feedback/rollbacks?%s", strings.TrimRight(base, "/"), url.PathEscape(namespace), values.Encode())
}

func getContextDBJSON(client *http.Client, target string, out any) error {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func queryDefault(r *http.Request, key, def string) string {
	if value := strings.TrimSpace(r.URL.Query().Get(key)); value != "" {
		return value
	}
	return def
}

func queryIntDefault(r *http.Request, key string, def int) int {
	var out int
	if _, err := fmt.Sscanf(r.URL.Query().Get(key), "%d", &out); err == nil && out > 0 {
		return out
	}
	return def
}

func ptrSecretStatus(status SecretStatus) *SecretStatus {
	return &status
}
