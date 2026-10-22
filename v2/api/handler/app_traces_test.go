package handler

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
)

const testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

func tracesRequest(h *Handler, route func(http.ResponseWriter, *http.Request), app, traceID, query string, principal *AccessPrincipal) *httptest.ResponseRecorder {
	target := "/api/v1/apps/" + app + "/traces"
	if traceID != "" {
		target += "/" + traceID
	}
	req := httptest.NewRequest(http.MethodGet, target+"?"+query, nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", app)
	rc.URLParams.Add("traceId", traceID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	if principal != nil {
		req = WithAccessPrincipal(req, principal)
	}
	rec := httptest.NewRecorder()
	route(rec, req)
	return rec
}

func tempoTestTrace() string {
	spanB64 := func(h string) string { b, _ := hex.DecodeString(h); return base64.StdEncoding.EncodeToString(b) }
	traceB64 := spanB64(testTraceID)
	return `{"trace":{"resourceSpans":[
	 {"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"web"}},{"key":"norn.app","value":{"stringValue":"web"}},{"key":"norn.process","value":{"stringValue":"web"}},{"key":"nomad.alloc_id","value":{"stringValue":"alloc-1"}}]},
	  "scopeSpans":[{"spans":[
	   {"traceId":"` + traceB64 + `","spanId":"` + spanB64("00f067aa0ba902b7") + `","name":"GET /","kind":"SPAN_KIND_SERVER","startTimeUnixNano":"1760000000000000000","endTimeUnixNano":"1760000000012500000","attributes":[{"key":"http.route","value":{"stringValue":"/"}},{"key":"http.status_code","value":{"intValue":"500"}}],"status":{"code":"STATUS_CODE_ERROR","message":"boom"}},
	   {"traceId":"` + testTraceID + `","spanId":"b7ad6b7169203331","parentSpanId":"` + spanB64("00f067aa0ba902b7") + `","name":"query","kind":3,"startTimeUnixNano":1760000000001000000,"endTimeUnixNano":"1760000000003000000","status":{}}]}]},
	 {"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"billing"}},{"key":"norn.app","value":{"stringValue":"billing"}}]},
	  "scopeSpans":[{"spans":[{"traceId":"` + testTraceID + `","spanId":"1111111111111111","parentSpanId":"b7ad6b7169203331","name":"charge","kind":"SPAN_KIND_SERVER","startTimeUnixNano":"1760000000002000000","endTimeUnixNano":"1760000000002500000","status":{"code":1}}]}]}
	]}}`
}

func TestAppTraceNormalizesAndFiltersForAppBoundPrincipals(t *testing.T) {
	tempo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/traces/"+testTraceID {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(tempoTestTrace()))
	}))
	defer tempo.Close()
	h := New(nil, nil, nil, nil, &config.Config{TempoURL: tempo.URL}, nil, nil, nil, nil, nil, nil)

	var full struct {
		TraceID      string      `json:"traceId"`
		App          string      `json:"app"`
		OmittedSpans int         `json:"omittedSpans"`
		Spans        []traceSpan `json:"spans"`
	}
	unscoped := &AccessPrincipal{Subject: "ops", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}}
	rec := tracesRequest(h, h.GetAppTrace, "web", testTraceID, "", unscoped)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if full.OmittedSpans != 0 || len(full.Spans) != 3 || full.TraceID != testTraceID {
		t.Fatalf("unscoped = %+v", full)
	}
	root := full.Spans[0]
	if root.SpanID != "00f067aa0ba902b7" || root.ParentSpanID != "" || root.Kind != "server" || root.Status != "error" || root.StatusMsg != "boom" ||
		root.DurationMs != 12.5 || root.Process != "web" || root.AllocID != "alloc-1" || root.Attributes["http.status_code"] != float64(500) {
		t.Fatalf("root = %+v", root)
	}
	if child := full.Spans[1]; child.ParentSpanID != "00f067aa0ba902b7" || child.Kind != "client" || child.Status != "unset" {
		t.Fatalf("child = %+v", child)
	}

	bound := &AccessPrincipal{Subject: "web", App: "web", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}}
	rec = tracesRequest(h, h.GetAppTrace, "web", testTraceID, "", bound)
	full.Spans = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || full.OmittedSpans != 1 || len(full.Spans) != 2 {
		t.Fatalf("bound = %d %+v", rec.Code, full)
	}
	if rec := tracesRequest(h, h.GetAppTrace, "billing", testTraceID, "", bound); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "app_traces_forbidden") {
		t.Fatalf("cross-app = %d %s", rec.Code, rec.Body)
	}
	readOnly := &AccessPrincipal{Subject: "x", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{"api:other"}}
	if rec := tracesRequest(h, h.GetAppTrace, "web", testTraceID, "", readOnly); rec.Code != http.StatusForbidden {
		t.Fatalf("scope = %d", rec.Code)
	}
}

func TestAppTraceErrorMapping(t *testing.T) {
	unscoped := &AccessPrincipal{Subject: "ops", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}}
	h := New(nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil)
	if rec := tracesRequest(h, h.GetAppTrace, "web", testTraceID, "", unscoped); rec.Code != 503 || !strings.Contains(rec.Body.String(), "tracing_unavailable") {
		t.Fatalf("unconfigured = %d %s", rec.Code, rec.Body)
	}
	fallback := 0
	tempo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "deadbeefdeadbeefdeadbeefdeadbeef") || strings.HasSuffix(r.URL.Path, "ffffffffffffffffffffffffffffffff"):
			if strings.HasPrefix(r.URL.Path, "/api/traces/") {
				fallback++
			}
			http.NotFound(w, r)
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer tempo.Close()
	h = New(nil, nil, nil, nil, &config.Config{TempoURL: tempo.URL}, nil, nil, nil, nil, nil, nil)
	if rec := tracesRequest(h, h.GetAppTrace, "web", "deadbeefdeadbeefdeadbeefdeadbeef", "", unscoped); rec.Code != 404 || !strings.Contains(rec.Body.String(), "trace_not_found") || fallback != 1 {
		t.Fatalf("missing = %d %s fallback=%d", rec.Code, rec.Body, fallback)
	}
	if rec := tracesRequest(h, h.GetAppTrace, "web", testTraceID, "", unscoped); rec.Code != 502 || !strings.Contains(rec.Body.String(), "trace_backend_failed") {
		t.Fatalf("backend = %d %s", rec.Code, rec.Body)
	}
	for _, id := range []string{"short", strings.ToUpper(testTraceID), strings.Repeat("g", 32)} {
		if rec := tracesRequest(h, h.GetAppTrace, "web", id, "", unscoped); rec.Code != 400 {
			t.Fatalf("id %q = %d", id, rec.Code)
		}
	}
}

func TestSearchAppTracesBuildsTraceQLAndNormalizes(t *testing.T) {
	var got url.Values
	tempo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			http.NotFound(w, r)
			return
		}
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"traces":[{"traceID":"` + testTraceID + `","rootServiceName":"web","rootTraceName":"GET /","startTimeUnixNano":"1760000000000000000","durationMs":12,"spanSets":[{"matched":3}],"spanSet":{"matched":3}}]}`))
	}))
	defer tempo.Close()
	h := New(nil, nil, nil, nil, &config.Config{TempoURL: tempo.URL}, nil, nil, nil, nil, nil, nil)
	bound := &AccessPrincipal{Subject: "web", App: "web", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}}
	rec := tracesRequest(h, h.SearchAppTraces, "web", "", "since=2026-10-21T10:00:00Z&until=2026-10-21T11:00:00Z&limit=5&minDurationMs=50", bound)
	if rec.Code != 200 {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	if got.Get("q") != `{ resource.norn.app = "web" && duration > 50ms }` || got.Get("limit") != "5" || got.Get("start") != "1792576800" {
		t.Fatalf("tempo query = %v", got)
	}
	var out struct {
		App    string         `json:"app"`
		Traces []traceSummary `json:"traces"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Traces) != 1 || out.Traces[0] != (traceSummary{TraceID: testTraceID, RootName: "GET /", RootService: "web", Start: "2025-10-09T08:53:20Z", DurationMs: 12, SpanCount: 3}) {
		t.Fatalf("traces = %+v", out.Traces)
	}
	for _, q := range []string{"limit=0", "limit=101", "minDurationMs=-1", "since=nope", "since=2026-10-21T11:00:00Z&until=2026-10-21T10:00:00Z"} {
		if rec := tracesRequest(h, h.SearchAppTraces, "web", "", q, bound); rec.Code != 400 {
			t.Fatalf("%s = %d", q, rec.Code)
		}
	}
	if rec := tracesRequest(h, h.SearchAppTraces, "billing", "", "", bound); rec.Code != 403 {
		t.Fatalf("cross-app = %d", rec.Code)
	}
}
