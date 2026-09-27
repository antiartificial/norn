package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/logcollect"
)

const (
	logHistoryDefaultLimit = 500
	logHistoryMaxLimit     = 5000
	logHistoryScanBytes    = 16 << 20
)

// SetLogSpool enables historical log reads from the collection spool.
func (h *Handler) SetLogSpool(spool *logcollect.Spool) { h.logSpool = spool }

type logHistoryEntry struct {
	Time      time.Time `json:"time"`
	NodeID    string    `json:"nodeId"`
	NodeName  string    `json:"nodeName"`
	JobID     string    `json:"jobId"`
	AllocID   string    `json:"allocId"`
	TaskGroup string    `json:"taskGroup"`
	Task      string    `json:"task"`
	Stream    string    `json:"stream"`
	File      int       `json:"file"`
	Offset    int64     `json:"offset"`
	Data      string    `json:"data,omitempty"`
	Gap       string    `json:"gap,omitempty"`
}

// GetAppLogHistory returns collected output of one app, oldest first, with
// full source labels (node, allocation, task group, task, stream, log file
// and offset). It uses the same scope and app binding as history reads:
// api:read, and an app-bound credential may read only its own app. Reads
// are bounded; dropped diagnostics are reported (gap records and loss
// counters), never hidden.
func (h *Handler) GetAppLogHistory(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireControlScope(w, r, ScopeAPIRead)
	if !ok {
		return
	}
	appID := chi.URLParam(r, "id")
	if principal.App != "" && principal.App != appID {
		WriteControlProblem(w, r, http.StatusForbidden, "app_logs_forbidden", "this credential may read only its own app's logs")
		return
	}
	if h.logSpool == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "log_collection_unavailable", "log collection is not configured on this server")
		return
	}
	values := r.URL.Query()
	query := logcollect.Query{App: appID, Alloc: values.Get("alloc"), Task: values.Get("task"), Stream: values.Get("stream"), Limit: logHistoryDefaultLimit, ScanBytes: logHistoryScanBytes}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > logHistoryMaxLimit {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_log_query", "limit must be between 1 and 5000")
			return
		}
		query.Limit = limit
	}
	if raw := values.Get("since"); raw != "" {
		since, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_log_query", "since must be an RFC 3339 time")
			return
		}
		query.Since = since
	}
	if query.Stream != "" && query.Stream != "stdout" && query.Stream != "stderr" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_log_query", "stream must be stdout or stderr")
		return
	}
	result, err := h.logSpool.Read(query)
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_log_query", "log history could not be read for this query")
		return
	}
	entries := make([]logHistoryEntry, 0, len(result.Entries))
	for _, entry := range result.Entries {
		entries = append(entries, logHistoryEntry{Time: entry.Time, NodeID: entry.NodeID, NodeName: entry.NodeName, JobID: entry.JobID, AllocID: entry.AllocID,
			TaskGroup: entry.TaskGroup, Task: entry.Task, Stream: entry.Stream, File: entry.File, Offset: entry.Offset, Data: string(entry.Data), Gap: entry.Gap})
	}
	writeJSON(w, map[string]interface{}{"app": appID, "entries": entries, "truncated": result.Truncated, "loss": result.Counters})
}
