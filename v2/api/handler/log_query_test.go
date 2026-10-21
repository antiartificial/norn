package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/logcollect"
)

func logQueryRequest(h *Handler, app, query string, principal *AccessPrincipal) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/apps/"+app+"/logs/query?"+query, nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", app)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	if principal != nil {
		req = WithAccessPrincipal(req, principal)
	}
	rec := httptest.NewRecorder()
	h.GetAppLogQuery(rec, req)
	return rec
}

type logQueryResponse struct {
	App       string          `json:"app"`
	Source    string          `json:"source"`
	Entries   []logQueryEntry `json:"entries"`
	Truncated bool            `json:"truncated"`
}

func decodeLogQuery(t *testing.T, rec *httptest.ResponseRecorder) logQueryResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var out logQueryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

var logQueryReader = &AccessPrincipal{Subject: "reader", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}}

func TestAppLogQueryAuthAndAvailability(t *testing.T) {
	h := New(nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil)
	if rec := logQueryRequest(h, "web", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated = %d", rec.Code)
	}
	if rec := logQueryRequest(h, "web", "", &AccessPrincipal{Subject: "x", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIWrite}}); rec.Code != http.StatusForbidden {
		t.Errorf("missing scope = %d", rec.Code)
	}
	if rec := logQueryRequest(h, "web", "", &AccessPrincipal{Subject: "ci", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}, App: "other"}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "app_logs_forbidden") {
		t.Errorf("other app's credential = %d %s", rec.Code, rec.Body)
	}
	if rec := logQueryRequest(h, "web", "", logQueryReader); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "log_query_unavailable") {
		t.Errorf("unconfigured = %d %s", rec.Code, rec.Body)
	}
}

func TestAppLogQueryValidation(t *testing.T) {
	h := New(nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil)
	spool, err := logcollect.OpenSpool(filepath.Join(t.TempDir(), "spool"), logcollect.Limits{TotalBytes: 1 << 20, StreamBytes: 1 << 19, SegmentBytes: 1 << 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	h.SetLogSpool(spool)
	now := time.Now().UTC()
	for _, query := range []string{
		"limit=0", "limit=5001", "limit=x", "stream=other", "direction=sideways", "since=yesterday", "until=tomorrow",
		"since=" + now.Format(time.RFC3339) + "&until=" + now.Add(-time.Minute).Format(time.RFC3339),
		"since=" + now.Add(-8*24*time.Hour).Format(time.RFC3339),
		"alloc=" + url.QueryEscape(`a"} |= "`), "task=" + url.QueryEscape("a b"), "taskGroup=" + url.QueryEscape("{x}"),
		"contains=" + strings.Repeat("x", 257),
	} {
		if rec := logQueryRequest(h, "web", query, logQueryReader); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", query, rec.Code)
		}
	}
	if rec := logQueryRequest(h, "web", "limit=5000&direction=forward&stream=stderr&contains="+url.QueryEscape("a\"b"), logQueryReader); rec.Code != http.StatusOK {
		t.Errorf("valid query = %d %s", rec.Code, rec.Body)
	}
}

func TestAppLogQueryFromSpool(t *testing.T) {
	h := New(nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil)
	spool, err := logcollect.OpenSpool(filepath.Join(t.TempDir(), "spool"), logcollect.Limits{TotalBytes: 1 << 20, StreamBytes: 1 << 19, SegmentBytes: 1 << 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	h.SetLogSpool(spool)
	web := logcollect.Labels{App: "web", JobID: "web", NodeID: "node-1", NodeName: "mini-a", AllocID: "alloc-1", TaskGroup: "web", Task: "web", Stream: "stdout"}
	worker := logcollect.Labels{App: "web", JobID: "web-worker", NodeID: "node-1", NodeName: "mini-a", AllocID: "alloc-2", TaskGroup: "worker", Task: "worker", Stream: "stderr"}
	other := logcollect.Labels{App: "other", JobID: "other", AllocID: "alloc-9", TaskGroup: "web", Task: "web", Stream: "stdout"}
	for _, labels := range []logcollect.Labels{web, worker, other} {
		if _, err := spool.Stream(labels); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	const trace = "4bf92f3577b34da6a3ce929d0e0e4736"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(spool.Append(web, logcollect.Record{Time: base, Offset: 0, Data: []byte("one\ntwo trace_id=" + trace + "\n")}, logcollect.Record{Time: base.Add(time.Minute), Offset: 100, Data: []byte("three\n")}))
	must(spool.Append(worker, logcollect.Record{Time: base.Add(30 * time.Second), Data: []byte("job failed\n")}, logcollect.Record{Time: base.Add(2 * time.Minute), Offset: 50, Gap: "output lost"}))
	must(spool.Append(other, logcollect.Record{Time: base, Data: []byte("secret of another app\n")}))

	lines := func(out logQueryResponse) string {
		var parts []string
		for _, entry := range out.Entries {
			parts = append(parts, entry.Line+entry.Gap)
		}
		return strings.Join(parts, "|")
	}
	out := decodeLogQuery(t, logQueryRequest(h, "web", "", logQueryReader))
	if out.Source != "spool" || out.App != "web" || out.Truncated {
		t.Fatalf("response = %+v", out)
	}
	if got := lines(out); got != "output lost|three|job failed|two trace_id="+trace+"|one" {
		t.Errorf("backward default = %s", got)
	}
	if out.Entries[0].Gap != "output lost" || out.Entries[0].Line != "" || out.Entries[3].TraceID != trace || out.Entries[3].Task != "web" || out.Entries[3].NodeName != "mini-a" {
		t.Errorf("entry fields = %+v", out.Entries)
	}
	if got := lines(decodeLogQuery(t, logQueryRequest(h, "web", "direction=forward&limit=2", logQueryReader))); got != "one|two trace_id="+trace {
		t.Errorf("forward limit = %s", got)
	}
	if out := decodeLogQuery(t, logQueryRequest(h, "web", "direction=forward&limit=2", logQueryReader)); !out.Truncated {
		t.Error("limit hit must report truncated")
	}
	for query, want := range map[string]string{
		"contains=job":                          "job failed",
		"stream=stderr&direction=forward":       "job failed|output lost",
		"alloc=alloc-1&direction=forward":       "one|two trace_id=" + trace + "|three",
		"taskGroup=worker&direction=forward":    "job failed|output lost",
		"task=web&contains=t&direction=forward": "two trace_id=" + trace + "|three",
		"since=" + url.QueryEscape(base.Add(45*time.Second).Format(time.RFC3339Nano)) + "&until=" + url.QueryEscape(base.Add(90*time.Second).Format(time.RFC3339Nano)): "three",
	} {
		if got := lines(decodeLogQuery(t, logQueryRequest(h, "web", query, logQueryReader))); got != want {
			t.Errorf("%s = %q, want %q", query, got, want)
		}
	}
	// An app-bound credential reads its own app only.
	if rec := logQueryRequest(h, "web", "", &AccessPrincipal{Subject: "ci", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}, App: "web"}); rec.Code != http.StatusOK {
		t.Errorf("own app = %d", rec.Code)
	}
}

func TestAppLogQueryFromLoki(t *testing.T) {
	var gotQuery url.Values
	var gotTenant string
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotTenant = r.URL.Query(), r.Header.Get("X-Scope-OrgID")
		if fail {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"app":"web","job":"web","task_group":"web","task":"web","stream":"stdout","node":"mini-a"},"values":[
			["1792576800000000002","trace_id=4bf92f3577b34da6a3ce929d0e0e4736 hi",{"structuredMetadata":{"alloc_id":"alloc-1","node_id":"node-1"}}],
			["1792576800000000001","lost",{"structuredMetadata":{"alloc_id":"alloc-1","node_id":"node-1","norn_gap":"true"}}],
			["1792576800000000000","plain",{"structuredMetadata":{"alloc_id":"alloc-1","node_id":"node-1"}}]]}]}}`)
	}))
	defer srv.Close()
	h := New(nil, nil, nil, nil, &config.Config{LokiURL: srv.URL, LokiTenant: "t1"}, nil, nil, nil, nil, nil, nil)
	query := "alloc=alloc-1&task=web&stream=stdout&contains=" + url.QueryEscape(`x"} |= "`) + "&limit=2"
	out := decodeLogQuery(t, logQueryRequest(h, "web", query, logQueryReader))
	if out.Source != "loki" || len(out.Entries) != 2 || !out.Truncated {
		t.Fatalf("response = %+v", out)
	}
	if want := `{source="norn",app="web",task="web",stream="stdout"} |= "x\"} |= \"" | alloc_id="alloc-1"`; gotQuery.Get("query") != want {
		t.Errorf("logql = %s", gotQuery.Get("query"))
	}
	if gotQuery.Get("direction") != "backward" || gotQuery.Get("limit") != "3" || gotTenant != "t1" {
		t.Errorf("loki request = %v tenant %q", gotQuery, gotTenant)
	}
	first, second := out.Entries[0], out.Entries[1]
	if first.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || first.AllocID != "alloc-1" || first.NodeID != "node-1" || first.NodeName != "mini-a" || first.JobID != "web" || first.Stream != "stdout" || first.Time.UnixNano() != 1792576800000000002 {
		t.Errorf("first = %+v", first)
	}
	if second.Gap != "lost" || second.Line != "" {
		t.Errorf("gap entry = %+v", second)
	}
	// A Loki failure is a 502, not a silent spool answer.
	fail = true
	spool, err := logcollect.OpenSpool(filepath.Join(t.TempDir(), "spool"), logcollect.Limits{TotalBytes: 1 << 20, StreamBytes: 1 << 19, SegmentBytes: 1 << 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	h.SetLogSpool(spool)
	if rec := logQueryRequest(h, "web", "", logQueryReader); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "log_backend_failed") {
		t.Errorf("loki down = %d %s", rec.Code, rec.Body)
	}
}
