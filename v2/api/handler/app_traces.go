package handler

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	traceDefaultLimit   = 20
	traceMaxLimit       = 100
	traceMaxBodyBytes   = 16 << 20
	traceAppResourceKey = "norn.app"
)

var traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type traceSummary struct {
	TraceID     string  `json:"traceId"`
	RootName    string  `json:"rootName"`
	RootService string  `json:"rootService"`
	Start       string  `json:"start"`
	DurationMs  float64 `json:"durationMs"`
	SpanCount   int     `json:"spanCount"`
}

type traceSpan struct {
	SpanID       string                 `json:"spanId"`
	ParentSpanID string                 `json:"parentSpanId"`
	Name         string                 `json:"name"`
	Service      string                 `json:"service"`
	App          string                 `json:"app"`
	Process      string                 `json:"process"`
	AllocID      string                 `json:"allocId"`
	Kind         string                 `json:"kind"`
	Start        string                 `json:"start"`
	End          string                 `json:"end"`
	DurationMs   float64                `json:"durationMs"`
	Status       string                 `json:"status"`
	StatusMsg    string                 `json:"statusMessage"`
	Attributes   map[string]interface{} `json:"attributes"`
}

// SearchAppTraces lists recent traces that contain spans of one app. It uses
// the same scope and app binding as log history.
func (h *Handler) SearchAppTraces(w http.ResponseWriter, r *http.Request) {
	appID, tempo, ok := h.traceRequest(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	now := time.Now().UTC()
	since, until, limit, minDuration := now.Add(-time.Hour), now, traceDefaultLimit, 0
	var err error
	if raw := values.Get("since"); raw != "" {
		if since, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_trace_query", "since must be an RFC 3339 time")
			return
		}
	}
	if raw := values.Get("until"); raw != "" {
		if until, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_trace_query", "until must be an RFC 3339 time")
			return
		}
	}
	if !until.After(since) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_trace_query", "until must be after since")
		return
	}
	if raw := values.Get("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 || limit > traceMaxLimit {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_trace_query", "limit must be between 1 and 100")
			return
		}
	}
	if raw := values.Get("minDurationMs"); raw != "" {
		if minDuration, err = strconv.Atoi(raw); err != nil || minDuration < 0 || minDuration > 86400000 {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_trace_query", "minDurationMs must be a non-negative integer of milliseconds")
			return
		}
	}
	// The query is built only from the validated app ID and integers.
	query := fmt.Sprintf(`{ resource.%s = %s`, traceAppResourceKey, strconv.Quote(appID))
	if minDuration > 0 {
		query += fmt.Sprintf(" && duration > %dms", minDuration)
	}
	query += " }"
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var body struct {
		Traces []struct {
			TraceID           string         `json:"traceID"`
			RootServiceName   string         `json:"rootServiceName"`
			RootTraceName     string         `json:"rootTraceName"`
			StartTimeUnixNano flexString     `json:"startTimeUnixNano"`
			DurationMs        float64        `json:"durationMs"`
			SpanSet           *tempoSpanSet  `json:"spanSet"`
			SpanSets          []tempoSpanSet `json:"spanSets"`
		} `json:"traces"`
	}
	endpoint := url.Values{"q": {query}, "start": {strconv.FormatInt(since.Unix(), 10)}, "end": {strconv.FormatInt(until.Unix()+1, 10)}, "limit": {strconv.Itoa(limit)}}
	if _, err := tempoGet(ctx, tempo, "/api/search", endpoint.Encode(), &body); err != nil {
		WriteControlProblem(w, r, http.StatusBadGateway, "trace_backend_failed", "trace search failed")
		return
	}
	traces := make([]traceSummary, 0, len(body.Traces))
	for _, t := range body.Traces {
		id := normalizeOTLPID(t.TraceID, 16)
		if id == "" {
			continue
		}
		// spanCount is the number of this app's spans Tempo matched (spanSets
		// carries all sets; spanSet repeats the first).
		count := 0
		if len(t.SpanSets) > 0 {
			for _, set := range t.SpanSets {
				count += set.Matched
			}
		} else if t.SpanSet != nil {
			count = t.SpanSet.Matched
		}
		traces = append(traces, traceSummary{TraceID: id, RootName: t.RootTraceName, RootService: t.RootServiceName,
			Start: nanoTime(string(t.StartTimeUnixNano)), DurationMs: t.DurationMs, SpanCount: count})
	}
	writeJSON(w, map[string]interface{}{"app": appID, "traces": traces})
}

// GetAppTrace returns every span of one trace. An app-bound credential sees
// only spans whose resource norn.app is its app; the rest are counted.
func (h *Handler) GetAppTrace(w http.ResponseWriter, r *http.Request) {
	appID, tempo, ok := h.traceRequest(w, r)
	if !ok {
		return
	}
	traceID := chi.URLParam(r, "traceId")
	if !traceIDPattern.MatchString(traceID) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_trace_id", "traceId must be 32 lowercase hex characters")
		return
	}
	principal, _ := AccessPrincipalFromRequest(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var doc struct {
		Trace *otlpTrace `json:"trace"`
		otlpTrace
	}
	status, err := tempoGet(ctx, tempo, "/api/v2/traces/"+traceID, "", &doc)
	if status == http.StatusNotFound {
		// Older Tempo has no v2 endpoint; its 404 is indistinguishable from a
		// missing trace, so ask the v1 endpoint once.
		doc.Trace = nil
		doc.otlpTrace = otlpTrace{}
		status, err = tempoGet(ctx, tempo, "/api/traces/"+traceID, "", &doc)
	}
	if status == http.StatusNotFound {
		WriteControlProblem(w, r, http.StatusNotFound, "trace_not_found", "trace was not found")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadGateway, "trace_backend_failed", "trace lookup failed")
		return
	}
	trace := doc.otlpTrace
	if doc.Trace != nil {
		trace = *doc.Trace
	}
	spans, omitted := trace.flatten(principal.App)
	if len(spans) == 0 && omitted == 0 {
		WriteControlProblem(w, r, http.StatusNotFound, "trace_not_found", "trace was not found")
		return
	}
	writeJSON(w, map[string]interface{}{"traceId": traceID, "app": appID, "omittedSpans": omitted, "spans": spans})
}

// traceRequest applies the shared auth and configuration checks and returns
// the validated Tempo origin.
func (h *Handler) traceRequest(w http.ResponseWriter, r *http.Request) (string, *url.URL, bool) {
	principal, ok := requireControlScope(w, r, ScopeAPIRead)
	if !ok {
		return "", nil, false
	}
	appID := chi.URLParam(r, "id")
	if principal.App != "" && principal.App != appID {
		WriteControlProblem(w, r, http.StatusForbidden, "app_traces_forbidden", "this credential may read only its own app's traces")
		return "", nil, false
	}
	raw := ""
	if h.cfg != nil {
		raw = h.cfg.TempoURL
	}
	if raw == "" {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "tracing_unavailable", "tracing is not configured on this server (set NORN_TEMPO_URL)")
		return "", nil, false
	}
	origin, err := validHistoryOrigin(raw)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "tracing_unavailable", "NORN_TEMPO_URL is not a valid origin")
		return "", nil, false
	}
	return appID, origin, true
}

// tempoGet performs one bounded GET and decodes JSON on 200. It returns the
// HTTP status so callers can distinguish 404.
func tempoGet(ctx context.Context, origin *url.URL, path, rawQuery string, out interface{}) (int, error) {
	u := *origin
	u.Path = path
	u.RawQuery = rawQuery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return resp.StatusCode, fmt.Errorf("tempo returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, traceMaxBodyBytes+1))
	if err != nil || len(body) > traceMaxBodyBytes {
		return resp.StatusCode, errors.New("tempo response unreadable or too large")
	}
	return resp.StatusCode, json.Unmarshal(body, out)
}

type tempoSpanSet struct {
	Matched int `json:"matched"`
}

// flexString decodes a JSON string or number (unix-nano timestamps are strings
// in proto3 JSON but some encoders emit numbers).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexString(n.String())
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*f = flexString(s)
	return nil
}

type otlpValue struct {
	String *string     `json:"stringValue"`
	Int    *flexString `json:"intValue"`
	Double *float64    `json:"doubleValue"`
	Bool   *bool       `json:"boolValue"`
	Array  *struct {
		Values []otlpValue `json:"values"`
	} `json:"arrayValue"`
	Bytes *string `json:"bytesValue"`
}

func (v otlpValue) value() interface{} {
	switch {
	case v.String != nil:
		return *v.String
	case v.Int != nil:
		if n, err := strconv.ParseInt(string(*v.Int), 10, 64); err == nil {
			return n
		}
		return string(*v.Int)
	case v.Double != nil:
		return *v.Double
	case v.Bool != nil:
		return *v.Bool
	case v.Array != nil:
		out := make([]interface{}, 0, len(v.Array.Values))
		for _, item := range v.Array.Values {
			out = append(out, item.value())
		}
		return out
	case v.Bytes != nil:
		return *v.Bytes
	}
	return nil
}

type otlpAttr struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpSpan struct {
	TraceID      string     `json:"traceId"`
	SpanID       string     `json:"spanId"`
	ParentSpanID string     `json:"parentSpanId"`
	Name         string     `json:"name"`
	Kind         flexString `json:"kind"`
	Start        flexString `json:"startTimeUnixNano"`
	End          flexString `json:"endTimeUnixNano"`
	Attributes   []otlpAttr `json:"attributes"`
	Status       struct {
		Code    flexString `json:"code"`
		Message string     `json:"message"`
	} `json:"status"`
}

type otlpScopeSpans struct {
	Spans []otlpSpan `json:"spans"`
}

type otlpResourceSpans struct {
	Resource struct {
		Attributes []otlpAttr `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
	// Older Tempo releases emit the pre-1.0 OTLP name.
	LibrarySpans []otlpScopeSpans `json:"instrumentationLibrarySpans"`
}

type otlpTrace struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
	Batches       []otlpResourceSpans `json:"batches"`
}

// flatten converts OTLP JSON into sorted spans. When onlyApp is set, spans
// from other apps (or with no norn.app) are counted but not returned.
func (t otlpTrace) flatten(onlyApp string) ([]traceSpan, int) {
	spans, starts, omitted := []traceSpan{}, map[string]int64{}, 0
	groups := append(append([]otlpResourceSpans{}, t.ResourceSpans...), t.Batches...)
	for _, group := range groups {
		resource := map[string]interface{}{}
		for _, attr := range group.Resource.Attributes {
			resource[attr.Key] = attr.Value.value()
		}
		str := func(key string) string { s, _ := resource[key].(string); return s }
		scopes := append(append([]otlpScopeSpans{}, group.ScopeSpans...), group.LibrarySpans...)
		for _, scope := range scopes {
			for _, s := range scope.Spans {
				if onlyApp != "" && str(traceAppResourceKey) != onlyApp {
					omitted++
					continue
				}
				span := traceSpan{
					SpanID: normalizeOTLPID(s.SpanID, 8), ParentSpanID: normalizeOTLPID(s.ParentSpanID, 8), Name: s.Name,
					Service: str("service.name"), App: str(traceAppResourceKey), Process: str("norn.process"), AllocID: str("nomad.alloc_id"),
					Kind: spanKind(string(s.Kind)), Start: nanoTime(string(s.Start)), End: nanoTime(string(s.End)),
					Status: spanStatus(string(s.Status.Code)), StatusMsg: s.Status.Message, Attributes: map[string]interface{}{},
				}
				start, _ := strconv.ParseInt(string(s.Start), 10, 64)
				end, _ := strconv.ParseInt(string(s.End), 10, 64)
				if end > start {
					span.DurationMs = float64(end-start) / 1e6
				}
				for _, attr := range s.Attributes {
					span.Attributes[attr.Key] = attr.Value.value()
				}
				starts[span.SpanID] = start
				spans = append(spans, span)
			}
		}
	}
	sort.SliceStable(spans, func(i, j int) bool { return starts[spans[i].SpanID] < starts[spans[j].SpanID] })
	return spans, omitted
}

// normalizeOTLPID returns lowercase hex for an OTLP id that may be hex or
// base64 (Tempo has emitted both). Anything else yields "". Empty stays empty
// so a root span has no parent.
func normalizeOTLPID(id string, size int) string {
	switch len(id) {
	case 0:
		return ""
	case size * 2:
		if b, err := hex.DecodeString(id); err == nil {
			return hex.EncodeToString(b)
		}
	}
	if b, err := base64.StdEncoding.DecodeString(id); err == nil && len(b) == size {
		return hex.EncodeToString(b)
	}
	// Tempo trims leading zero bytes of trace ids in some versions.
	if b, err := hex.DecodeString(id); err == nil && len(b) < size {
		return strings.Repeat("00", size-len(b)) + hex.EncodeToString(b)
	}
	return ""
}

func nanoTime(raw string) string {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return ""
	}
	return time.Unix(0, n).UTC().Format(time.RFC3339Nano)
}

func spanKind(raw string) string {
	switch strings.TrimPrefix(raw, "SPAN_KIND_") {
	case "1", "INTERNAL":
		return "internal"
	case "2", "SERVER":
		return "server"
	case "3", "CLIENT":
		return "client"
	case "4", "PRODUCER":
		return "producer"
	case "5", "CONSUMER":
		return "consumer"
	}
	return "unspecified"
}

func spanStatus(raw string) string {
	switch strings.TrimPrefix(raw, "STATUS_CODE_") {
	case "1", "OK":
		return "ok"
	case "2", "ERROR":
		return "error"
	}
	return "unset"
}
