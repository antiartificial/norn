package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHistoryRangeBounds(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		age  time.Duration
		step int
	}{{time.Hour, 15}, {12 * time.Hour, 61}, {24 * time.Hour, 121}, {48 * time.Hour, 300}} {
		q := url.Values{"start": {now.Add(-tc.age).Format(time.RFC3339)}, "end": {now.Format(time.RFC3339)}, "step": {"1"}}
		got, err := parseHistoryRange(q, now)
		if err != nil || got.step != tc.step {
			t.Fatalf("%v: step %d err %v", tc.age, got.step, err)
		}
	}
	for _, q := range []url.Values{{}, {"start": {now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)}, "end": {now.Format(time.RFC3339)}}, {"start": {now.Format(time.RFC3339)}, "end": {now.Format(time.RFC3339)}}} {
		if _, err := parseHistoryRange(q, now); err == nil {
			t.Fatalf("accepted %v", q)
		}
	}
}
func TestHostHistoryRequiresPrincipal(t *testing.T) {
	r := httptest.NewRecorder()
	(&Handler{}).HostMetricsHistory(r, httptest.NewRequest("GET", "/api/v1/host/metrics/history", nil))
	if r.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", r.Code)
	}
}
func TestHistoryOrigins(t *testing.T) {
	for _, raw := range []string{"http://user:pass@localhost", "file:///tmp/a", "http://localhost/?q=x", "http://localhost/path"} {
		if _, err := validHistoryOrigin(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestHistoryMappingAndFixedQueries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	v := historyRange{now.Add(-time.Hour), now, 60, true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("forwarded credentials")
		}
		q := r.URL.Query().Get("query")
		if !strings.Contains(q, `host="mini"`) {
			t.Errorf("missing host selector: %s", q)
		}
		value := "25"
		metric := map[string]string{}
		if strings.Contains(q, "memory_used") {
			value = "700"
		}
		if strings.Contains(q, "memory_total") && !strings.Contains(q, "norn_container") {
			value = "1000"
		}
		if strings.Contains(q, "norn_container") {
			metric = map[string]string{"norn_app": "demo", "norn_process": "web"}
			if !strings.Contains(q, `instance=~"127\\.0\\.0\\.1:[0-9]+"`) {
				t.Error("missing exporter selector")
			}
			value = "10"
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{"metric": metric, "values": [][]any{{now.Unix(), value}}}}}})
	}))
	defer server.Close()
	origin, _ := url.Parse(server.URL)
	out, err := fetchHostHistory(context.Background(), origin, "mini", "127.0.0.1:9000", v)
	if err != nil || len(out.Host) != 1 || len(out.Services) != 1 {
		t.Fatalf("out %+v err %v", out, err)
	}
	if out.Host[0].CPUPercent != 25 || out.Host[0].MemoryUsedBytes != 700 || out.Services[0].App != "demo" {
		t.Fatalf("bad mapping %+v", out)
	}
}
func TestHistoryRejectsUpstreamErrorsAndRedirects(t *testing.T) {
	for _, status := range []int{302, 500} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://example.com")
			w.WriteHeader(status)
		}))
		u, _ := url.Parse(s.URL)
		_, err := historyQuery(context.Background(), u, "fixed", historyRange{time.Now().Add(-time.Hour), time.Now(), 15, false})
		s.Close()
		if err == nil {
			t.Fatalf("accepted %d", status)
		}
	}
}
func TestHistoryCancellationAndInvalidValues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	u, _ := url.Parse("http://127.0.0.1:1")
	if _, err := historyQuery(ctx, u, "fixed", historyRange{}); err == nil {
		t.Fatal("ignored cancellation")
	}
	var m historyMatrix
	json.Unmarshal([]byte(`{"values":[[1,"NaN"],[2,"+Inf"],[3,"-1"],[4,"5"]]}`), &m)
	got := historyValues(m)
	if len(got) != 1 || got[4] != 5 {
		t.Fatalf("values %v", got)
	}
}

func TestHistoryLiveReadOnly(t *testing.T) {
	raw := os.Getenv("NORN_TEST_HISTORY_ORIGIN")
	if raw == "" {
		t.Skip("explicit read-only monitoring origin required")
	}
	origin, err := validHistoryOrigin(raw)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, age := range []time.Duration{time.Hour, 24 * time.Hour} {
		v, err := parseHistoryRange(url.Values{"start": {now.Add(-age).Format(time.RFC3339)}, "end": {now.Format(time.RFC3339)}, "includeServices": {"true"}}, now)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		out, err := fetchHostHistory(ctx, origin, os.Getenv("NORN_TEST_HISTORY_HOST"), os.Getenv("NORN_TEST_HISTORY_EXPORTER"), v)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Host) == 0 || len(out.Services) == 0 {
			t.Fatalf("missing data: host %d services %d", len(out.Host), len(out.Services))
		}
		t.Logf("range %s step %d host %d services %d first %s last %s", age, out.StepSeconds, len(out.Host), len(out.Services), out.Host[0].ObservedAt, out.Host[len(out.Host)-1].ObservedAt)
	}
}

func TestHostHistoryRejectsWrongScope(t *testing.T) {
	r := WithAccessPrincipal(httptest.NewRequest("GET", "/api/v1/host/metrics/history", nil), &AccessPrincipal{Scopes: []string{ScopeFleetOperate}})
	w := httptest.NewRecorder()
	(&Handler{}).HostMetricsHistory(w, r)
	if w.Code != 403 {
		t.Fatalf("status %d", w.Code)
	}
}
func TestHistoryRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(strings.Repeat("x", (4<<20)+1))) }))
	defer server.Close()
	origin, _ := url.Parse(server.URL)
	if _, err := historyQuery(context.Background(), origin, "fixed", historyRange{}); err == nil {
		t.Fatal("accepted oversized response")
	}
}
func TestHistoryBoundaryTolerance(t *testing.T) {
	now := time.Now().UTC()
	v, err := parseHistoryRange(url.Values{"start": {now.Add(-historyRetention - 20*time.Second).Format(time.RFC3339)}, "end": {now.Format(time.RFC3339)}}, now)
	if err != nil || v.start.Before(now.Add(-historyRetention)) {
		t.Fatalf("boundary %+v %v", v, err)
	}
}
