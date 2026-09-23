package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/logcollect"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// Collected logs are read back with their full labels under the same scope
// and app binding as other history. Diagnostic log handling is independent
// of the evidence reserve: while audited mutations are refused, logs are
// still collected and read, and dropping logs frees no evidence reserve.
func TestAppLogHistoryIsAuthorizedLabelledAndIndependentOfEvidenceReserve(t *testing.T) {
	db := privateControlDB(t)
	ctx := context.Background()
	h := New(db, nil, nil, nil, &config.Config{AppsDir: t.TempDir()}, nil, nil, nil, saga.NewPostgresStore(db.Pool), nil, nil)
	read := func(app, query string, principal *AccessPrincipal) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/apps/"+app+"/logs/history?"+query, nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", app)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		if principal != nil {
			req = WithAccessPrincipal(req, principal)
		}
		rec := httptest.NewRecorder()
		h.GetAppLogHistory(rec, req)
		return rec
	}
	reader := &AccessPrincipal{Subject: "reader", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}}
	if rec := read("shop", "", reader); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("without collection = %d", rec.Code)
	}
	spool, err := logcollect.OpenSpool(filepath.Join(t.TempDir(), "spool"), logcollect.Limits{TotalBytes: 4 << 10, StreamBytes: 2 << 10, SegmentBytes: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	h.SetLogSpool(spool)

	// Exhaust the evidence reserve first: collection must not care.
	if err := db.SetEvidenceReservePolicy(ctx, store.EvidenceReservePolicy{Enabled: true, MaxPending: 1, MaxPendingAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordArchiveCapacity(ctx, true, "archive quota reached"); err != nil {
		t.Fatal(err)
	}
	web := logcollect.Labels{App: "shop", JobID: "shop", NodeID: "node-1", NodeName: "mini-a", AllocID: "alloc-1", TaskGroup: "web", Task: "web", Stream: "stdout"}
	other := logcollect.Labels{App: "other", JobID: "other", NodeID: "node-1", NodeName: "mini-a", AllocID: "alloc-9", TaskGroup: "web", Task: "web", Stream: "stderr"}
	for _, labels := range []logcollect.Labels{web, other} {
		if _, err := spool.Stream(labels); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now().UTC()
	var offset int64
	for index := range 40 {
		line := []byte(strings.Repeat("x", 90) + "\n")
		if err := spool.Append(web, logcollect.Record{Time: start.Add(time.Duration(index) * time.Millisecond), Offset: offset, Data: line}); err != nil {
			t.Fatal(err)
		}
		offset += int64(len(line))
	}
	if err := spool.Append(other, logcollect.Record{Time: start, Data: []byte("other app\n")}); err != nil {
		t.Fatal(err)
	}
	if spool.Counters().DroppedBytes == 0 {
		t.Fatal("expected bounded spool to drop the oldest diagnostics")
	}
	reserve, err := db.EvidenceReserve(ctx)
	if err != nil || !reserve.Exhausted || !reserve.ArchiveExhausted {
		t.Fatalf("dropping diagnostics changed the evidence reserve: %+v, %v", reserve, err)
	}

	rec := read("shop", "stream=stdout&limit=5", reader)
	var body struct {
		Entries []struct {
			NodeName, AllocID, TaskGroup, Task, Stream, Data string
			Offset                                           int64
		} `json:"entries"`
		Truncated bool                `json:"truncated"`
		Loss      logcollect.Counters `json:"loss"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Entries) != 5 || !body.Truncated || body.Loss.DroppedBytes == 0 {
		t.Fatalf("history = %d %s", rec.Code, rec.Body.String())
	}
	for _, entry := range body.Entries {
		if entry.NodeName != "mini-a" || entry.AllocID != "alloc-1" || entry.TaskGroup != "web" || entry.Task != "web" || entry.Stream != "stdout" || strings.Contains(entry.Data, "other app") {
			t.Fatalf("entry = %+v", entry)
		}
	}
	if rec := read("shop", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", rec.Code)
	}
	if rec := read("shop", "", &AccessPrincipal{Subject: "events", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeEventsRead}}); rec.Code != http.StatusForbidden {
		t.Fatalf("without api:read = %d", rec.Code)
	}
	if rec := read("shop", "", &AccessPrincipal{Subject: "ci", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}, App: "other"}); rec.Code != http.StatusForbidden {
		t.Fatalf("other app's credential = %d", rec.Code)
	}
	for _, bad := range []string{"limit=0", "limit=99999", "since=yesterday", "stream=stdin"} {
		if rec := read("shop", bad, reader); rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q = %d", bad, rec.Code)
		}
	}
}
