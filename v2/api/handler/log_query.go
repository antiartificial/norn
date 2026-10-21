package handler

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/logcollect"
)

const (
	logQueryDefaultLimit = 500
	logQueryMaxLimit     = 5000
	logQueryDefaultSpan  = time.Hour
	logQueryMaxSpan      = 7 * 24 * time.Hour
	logQueryMaxContains  = 256
	logQueryScanBytes    = 16 << 20
)

var logQueryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)

type logQueryEntry struct {
	Time      time.Time `json:"time"`
	NodeID    string    `json:"nodeId"`
	NodeName  string    `json:"nodeName"`
	JobID     string    `json:"jobId"`
	AllocID   string    `json:"allocId"`
	TaskGroup string    `json:"taskGroup"`
	Task      string    `json:"task"`
	Stream    string    `json:"stream"`
	Line      string    `json:"line"`
	Gap       string    `json:"gap,omitempty"`
	TraceID   string    `json:"traceId,omitempty"`
}

// logQuery is a validated GET /apps/{id}/logs/query request.
type logQuery struct {
	app, alloc, taskGroup, task, stream, contains string
	since, until                                  time.Time
	limit                                         int
	backward                                      bool
}

func parseLogQuery(r *http.Request, app string, now time.Time) (logQuery, string) {
	values := r.URL.Query()
	q := logQuery{app: app, alloc: values.Get("alloc"), taskGroup: values.Get("taskGroup"), task: values.Get("task"), stream: values.Get("stream"),
		contains: values.Get("contains"), until: now, limit: logQueryDefaultLimit, backward: true}
	q.since = now.Add(-logQueryDefaultSpan)
	for name, value := range map[string]string{"alloc": q.alloc, "taskGroup": q.taskGroup, "task": q.task} {
		if value != "" && !logQueryName.MatchString(value) {
			return q, name + " is not a valid identifier"
		}
	}
	if q.stream != "" && q.stream != "stdout" && q.stream != "stderr" {
		return q, "stream must be stdout or stderr"
	}
	if len(q.contains) > logQueryMaxContains || !utf8.ValidString(q.contains) {
		return q, "contains must be valid UTF-8 of at most 256 bytes"
	}
	var err error
	if raw := values.Get("since"); raw != "" {
		if q.since, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			return q, "since must be an RFC 3339 time"
		}
	}
	if raw := values.Get("until"); raw != "" {
		if q.until, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			return q, "until must be an RFC 3339 time"
		}
	}
	if !q.until.After(q.since) || q.until.Sub(q.since) > logQueryMaxSpan {
		return q, "until must be after since, and the window at most 7 days"
	}
	if raw := values.Get("limit"); raw != "" {
		if q.limit, err = strconv.Atoi(raw); err != nil || q.limit <= 0 || q.limit > logQueryMaxLimit {
			return q, "limit must be between 1 and 5000"
		}
	}
	switch values.Get("direction") {
	case "", "backward":
	case "forward":
		q.backward = false
	default:
		return q, "direction must be backward or forward"
	}
	return q, ""
}

// GetAppLogQuery returns an app's log lines in a time window, filtered by
// allocation, task group, task, stream and substring. It reads Loki when
// NORN_LOKI_URL is set (errors are reported, never answered from the spool)
// and the collection spool otherwise. Scope and app binding match log
// history: api:read, and an app-bound credential may read only its own app.
func (h *Handler) GetAppLogQuery(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireControlScope(w, r, ScopeAPIRead)
	if !ok {
		return
	}
	appID := chi.URLParam(r, "id")
	if principal.App != "" && principal.App != appID {
		WriteControlProblem(w, r, http.StatusForbidden, "app_logs_forbidden", "this credential may read only its own app's logs")
		return
	}
	lokiURL := ""
	if h.cfg != nil {
		lokiURL = h.cfg.LokiURL
	}
	if lokiURL == "" && h.logSpool == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "log_query_unavailable", "neither NORN_LOKI_URL nor NORN_LOG_SPOOL_DIR is configured on this server")
		return
	}
	q, problem := parseLogQuery(r, appID, time.Now().UTC())
	if problem != "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_log_query", problem)
		return
	}
	var entries []logQueryEntry
	var truncated bool
	source := "spool"
	if lokiURL != "" {
		source = "loki"
		var err error
		if entries, truncated, err = h.queryLoki(r.Context(), q); err != nil {
			WriteControlProblem(w, r, http.StatusBadGateway, "log_backend_failed", "Loki query failed; the spool is not used as a fallback while NORN_LOKI_URL is set")
			return
		}
	} else {
		var err error
		if entries, truncated, err = h.querySpool(q); err != nil {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_log_query", "log history could not be read for this query")
			return
		}
	}
	if entries == nil {
		entries = []logQueryEntry{}
	}
	writeJSON(w, map[string]interface{}{"app": appID, "source": source, "entries": entries, "truncated": truncated})
}

func (h *Handler) queryLoki(ctx context.Context, q logQuery) ([]logQueryEntry, bool, error) {
	client, err := logcollect.NewLokiClient(h.cfg.LokiURL, h.cfg.LokiTenant, h.cfg.LokiToken)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	logql := logcollect.BuildLogQL(logcollect.LogQLFilter{App: q.app, TaskGroup: q.taskGroup, Task: q.task, Stream: q.stream, Alloc: q.alloc, Contains: q.contains})
	// One extra line tells whether more matched than the limit.
	fetch := min(q.limit+1, logQueryMaxLimit)
	found, err := client.QueryRange(ctx, logql, q.since, q.until, fetch, q.backward)
	if err != nil {
		return nil, false, err
	}
	truncated := len(found) > q.limit || len(found) == logQueryMaxLimit
	if len(found) > q.limit {
		found = found[:q.limit]
	}
	entries := make([]logQueryEntry, 0, len(found))
	for _, entry := range found {
		item := logQueryEntry{Time: entry.Time, NodeID: entry.Field("node_id"), NodeName: entry.Field("node"), JobID: entry.Field("job"), AllocID: entry.Field("alloc_id"),
			TaskGroup: entry.Field("task_group"), Task: entry.Field("task"), Stream: entry.Field("stream"), Line: entry.Line}
		if entry.Field("norn_gap") == "true" {
			item.Gap, item.Line = entry.Line, ""
		} else {
			item.TraceID = logcollect.ExtractTraceID(entry.Line)
		}
		entries = append(entries, item)
	}
	return entries, truncated, nil
}

func (h *Handler) querySpool(q logQuery) ([]logQueryEntry, bool, error) {
	result, err := h.logSpool.Read(logcollect.Query{App: q.app, Alloc: q.alloc, TaskGroup: q.taskGroup, Task: q.task, Stream: q.stream,
		Since: q.since.Add(-time.Second), Until: q.until, Newest: q.backward, ScanBytes: logQueryScanBytes})
	if err != nil {
		return nil, false, err
	}
	var entries []logQueryEntry
	for _, record := range result.Entries {
		base := logQueryEntry{NodeID: record.NodeID, NodeName: record.NodeName, JobID: record.JobID, AllocID: record.AllocID, TaskGroup: record.TaskGroup, Task: record.Task, Stream: record.Stream}
		if record.Gap != "" {
			if q.contains == "" && !record.Time.Before(q.since) && record.Time.Before(q.until) {
				base.Time, base.Gap = record.Time, record.Gap
				entries = append(entries, base)
			}
			continue
		}
		// Same line splitting and per-line nanosecond as the Loki shipper.
		pieces := bytes.Split(record.Data, []byte("\n"))
		if len(pieces) > 0 && len(pieces[len(pieces)-1]) == 0 {
			pieces = pieces[:len(pieces)-1]
		}
		for i, piece := range pieces {
			base.Time = record.Time.Add(time.Duration(i))
			base.Line = string(bytes.TrimSuffix(piece, []byte("\r")))
			if base.Time.Before(q.since) || !base.Time.Before(q.until) || !strings.Contains(base.Line, q.contains) {
				continue
			}
			base.TraceID = logcollect.ExtractTraceID(base.Line)
			entries = append(entries, base)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if q.backward {
			return entries[i].Time.After(entries[j].Time)
		}
		return entries[i].Time.Before(entries[j].Time)
	})
	truncated := result.Truncated
	if len(entries) > q.limit {
		entries, truncated = entries[:q.limit], true
	}
	return entries, truncated, nil
}
