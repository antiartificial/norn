package logcollect

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeLoki struct {
	mu     sync.Mutex
	fail   bool
	code   int // status while failing (default 500)
	pushes []lokiPush
	header http.Header
}

type lokiPush struct {
	Streams []struct {
		Stream map[string]string `json:"stream"`
		Values [][]any           `json:"values"`
	} `json:"streams"`
}

func (f *fakeLoki) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.header = r.Header.Clone()
	if r.URL.Path != "/loki/api/v1/push" || f.fail {
		http.Error(w, "boom", cmp.Or(f.code, http.StatusInternalServerError))
		return
	}
	var push lokiPush
	if err := json.Unmarshal(body, &push); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.pushes = append(f.pushes, push)
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeLoki) lines() [][]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]any
	for _, push := range f.pushes {
		for _, stream := range push.Streams {
			out = append(out, stream.Values...)
		}
	}
	return out
}

var lokiTestLabels = Labels{App: "web", JobID: "web", NodeID: "node-1", NodeName: "mini-a", AllocID: "alloc-1", TaskGroup: "web", Task: "web", Stream: "stdout"}

func newLokiTest(t *testing.T, dir string, fake *fakeLoki, batchLines int) (*Spool, *LokiShipper) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	spool, err := OpenSpool(dir, Limits{TotalBytes: 1 << 20, StreamBytes: 1 << 19, SegmentBytes: 1 << 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { spool.Close() })
	client, err := NewLokiClient(srv.URL, "tenant-a", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Stream(lokiTestLabels); err != nil {
		t.Fatal(err)
	}
	return spool, &LokiShipper{Spool: spool, Client: client, Interval: time.Millisecond, MaxBackoff: time.Millisecond, MaxBatchBytes: 1 << 20, MaxBatchLines: batchLines}
}

func TestLokiShipperLabelsMetadataAndDistinctTimestamps(t *testing.T) {
	fake := &fakeLoki{}
	spool, shipper := newLokiTest(t, t.TempDir()+"/spool", fake, 5000)
	at := time.Date(2026, 10, 21, 10, 0, 0, 100, time.UTC)
	data := []byte("same\nsame\r\nsame\n")
	if err := spool.Append(lokiTestLabels, Record{Time: at, Data: data}, Record{Time: at.Add(time.Second), Offset: int64(len(data)), Gap: "lost output"}); err != nil {
		t.Fatal(err)
	}
	if err := shipper.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	lines := fake.lines()
	if len(lines) != 4 {
		t.Fatalf("lines = %v", lines)
	}
	stamps := map[any]bool{}
	for _, line := range lines[:3] {
		stamps[line[0]] = true
		if line[1] != "same" {
			t.Errorf("line = %q (trailing empty piece and CR must be dropped)", line[1])
		}
	}
	if len(stamps) != 3 || lines[0][0] != "1792576800000000100" || lines[2][0] != "1792576800000000102" {
		t.Errorf("timestamps not distinct and consecutive: %v", lines)
	}
	if md := lines[0][2].(map[string]any); md["alloc_id"] != "alloc-1" || md["node_id"] != "node-1" || md["norn_gap"] != nil {
		t.Errorf("metadata = %v", md)
	}
	if md := lines[3][2].(map[string]any); lines[3][1] != "lost output" || md["norn_gap"] != "true" {
		t.Errorf("gap line = %v", lines[3])
	}
	labels := fake.pushes[0].Streams[0].Stream
	for key, want := range map[string]string{"source": "norn", "app": "web", "job": "web", "task_group": "web", "task": "web", "stream": "stdout", "node": "mini-a"} {
		if labels[key] != want {
			t.Errorf("label %s = %q, want %q", key, labels[key], want)
		}
	}
	if _, leaked := labels["alloc_id"]; leaked || len(labels) != 7 {
		t.Errorf("labels must stay low cardinality: %v", labels)
	}
	if fake.header.Get("X-Scope-OrgID") != "tenant-a" || fake.header.Get("Authorization") != "Bearer secret" {
		t.Errorf("headers = %v", fake.header)
	}
	if stats := shipper.Stats(); stats.ShippedLines != 4 || stats.FailedPushes != 0 || stats.LastSuccess.IsZero() {
		t.Errorf("stats = %+v", stats)
	}
	// Nothing new: no further push.
	if err := shipper.Flush(context.Background()); err != nil || len(fake.pushes) != 1 {
		t.Fatalf("idle flush pushed again: %v %d", err, len(fake.pushes))
	}
}

func TestLokiShipperBatchesAndResumesAfterRestart(t *testing.T) {
	fake := &fakeLoki{}
	dir := t.TempDir() + "/spool"
	spool, shipper := newLokiTest(t, dir, fake, 4)
	at := time.Now().UTC()
	var offset int64
	for i := range 5 {
		data := []byte(strings.Repeat("line\n", 3))
		if err := spool.Append(lokiTestLabels, Record{Time: at.Add(time.Duration(i) * time.Second), Offset: offset, Data: data}); err != nil {
			t.Fatal(err)
		}
		offset += int64(len(data))
	}
	if err := shipper.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.lines()) != 15 {
		t.Fatalf("shipped %d lines, want 15", len(fake.lines()))
	}
	for _, push := range fake.pushes {
		if n := len(push.Streams[0].Values); n > 4 && n != 3 {
			t.Errorf("batch of %d lines exceeds the bound (records are not split)", n)
		}
	}
	if len(fake.pushes) < 5 {
		t.Errorf("pushes = %d, want the 15 lines in several bounded batches", len(fake.pushes))
	}
	// Restart: a new spool and shipper resume from the durable cursor.
	spool.Close()
	reopened, err := OpenSpool(dir, Limits{TotalBytes: 1 << 20, StreamBytes: 1 << 19, SegmentBytes: 1 << 10})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	shipper.Spool = reopened
	if err := shipper.Flush(context.Background()); err != nil || len(fake.lines()) != 15 {
		t.Fatalf("restart re-shipped: %v %d", err, len(fake.lines()))
	}
	if _, err := reopened.Stream(lokiTestLabels); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Append(lokiTestLabels, Record{Time: at.Add(time.Minute), Offset: offset, Data: []byte("fresh\n")}); err != nil {
		t.Fatal(err)
	}
	if err := shipper.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lines := fake.lines(); len(lines) != 16 || lines[15][1] != "fresh" {
		t.Fatalf("lines after restart = %d", len(lines))
	}
}

func TestLokiShipperRetriesWithoutAdvancingCursor(t *testing.T) {
	fake := &fakeLoki{fail: true}
	spool, shipper := newLokiTest(t, t.TempDir()+"/spool", fake, 5000)
	if err := spool.Append(lokiTestLabels, Record{Time: time.Now().UTC(), Data: []byte("a\nb\n")}); err != nil {
		t.Fatal(err)
	}
	if err := shipper.Flush(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	if state := spool.StreamStates()[0]; state.Shipped.Known {
		t.Fatalf("cursor advanced after failed push: %+v", state.Shipped)
	}
	stats := shipper.Stats()
	if stats.FailedPushes != 1 || stats.LastError == "" || stats.ShippedLines != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	fake.mu.Lock()
	fake.fail = false
	fake.mu.Unlock()
	if err := shipper.Flush(context.Background()); err != nil || len(fake.lines()) != 2 {
		t.Fatalf("retry: %v %d", err, len(fake.lines()))
	}
	if state := spool.StreamStates()[0]; !state.Idle() {
		t.Fatalf("cursor not caught up: %+v", state)
	}
}

func TestLokiShipperDropsRejectedBatchesButRetriesAuthFailures(t *testing.T) {
	fake := &fakeLoki{fail: true, code: http.StatusUnauthorized}
	spool, shipper := newLokiTest(t, t.TempDir()+"/spool", fake, 5000)
	if err := spool.Append(lokiTestLabels, Record{Time: time.Now().UTC(), Data: []byte("a\nb\n")}); err != nil {
		t.Fatal(err)
	}
	if err := shipper.Flush(context.Background()); err == nil {
		t.Fatal("expected auth failure")
	}
	if state := spool.StreamStates()[0]; state.Shipped.Known {
		t.Fatalf("cursor advanced after auth failure: %+v", state.Shipped)
	}
	fake.mu.Lock()
	fake.code = http.StatusBadRequest
	fake.mu.Unlock()
	if err := shipper.Flush(context.Background()); err != nil {
		t.Fatalf("rejected batch should be dropped, got %v", err)
	}
	if state := spool.StreamStates()[0]; !state.Idle() {
		t.Fatalf("cursor stalled on a rejected batch: %+v", state)
	}
	if stats := shipper.Stats(); stats.DroppedLines != 2 || stats.ShippedLines != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestLokiCursorDoesNotRepeatGapRecord(t *testing.T) {
	fake := &fakeLoki{}
	spool, shipper := newLokiTest(t, t.TempDir()+"/spool", fake, 1)
	if err := spool.Append(lokiTestLabels, Record{Time: time.Now().UTC(), Offset: 10, Gap: "gap"}, Record{Time: time.Now().UTC(), Offset: 10, Data: []byte("x\n")}); err != nil {
		t.Fatal(err)
	}
	if err := shipper.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lines := fake.lines(); len(lines) != 2 || lines[0][1] != "gap" || lines[1][1] != "x" {
		t.Fatalf("lines = %v", lines)
	}
}

func TestBuildLogQLEscapesEveryValue(t *testing.T) {
	got := BuildLogQL(LogQLFilter{App: "web", TaskGroup: "g", Task: "t", Stream: "stderr", Alloc: "a1", Contains: "boom"})
	want := `{source="norn",app="web",task_group="g",task="t",stream="stderr"} |= "boom" | alloc_id="a1"`
	if got != want {
		t.Fatalf("logql = %s", got)
	}
	if got := BuildLogQL(LogQLFilter{App: "web"}); got != `{source="norn",app="web"}` {
		t.Fatalf("logql = %s", got)
	}
	for _, evil := range []string{`"} |= "`, `" or 1`, "\\", "line\nbreak", "`}|~`.*", `\"} | json | label_format x="`} {
		got := BuildLogQL(LogQLFilter{App: "web", Contains: evil, Alloc: evil, Task: evil})
		// Strip every quoted literal; only structure may remain.
		rest := quotedLiteral.ReplaceAllString(got, `""`)
		if rest != `{source="",app="",task=""} |= "" | alloc_id=""` {
			t.Errorf("injection %q escaped the literal: %s (rest %s)", evil, got, rest)
		}
	}
}

var quotedLiteral = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

func TestLokiQueryRangeReadsCategorizedAndMergedMetadata(t *testing.T) {
	var gotQuery, gotFlags string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotFlags = r.URL.Query().Get("query"), r.Header.Get("X-Loki-Response-Encoding-Flags")
		if r.URL.Query().Get("direction") != "backward" || r.URL.Query().Get("limit") != "3" || r.URL.Query().Get("start") != "1000" || r.URL.Query().Get("end") != "2000" {
			t.Errorf("query = %v", r.URL.Query())
		}
		io.WriteString(w, `{"status":"success","data":{"resultType":"streams","result":[
			{"stream":{"task":"web"},"values":[["1500","new",{"structuredMetadata":{"alloc_id":"a1"}}]]},
			{"stream":{"task":"web","alloc_id":"a2"},"values":[["1600","newest"],["1100","old"]]}]}}`)
	}))
	defer srv.Close()
	client, _ := NewLokiClient(srv.URL, "", "")
	entries, err := client.QueryRange(context.Background(), `{x="y"}`, time.Unix(0, 1000), time.Unix(0, 2000), 3, true)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != `{x="y"}` || gotFlags != "categorize-labels" {
		t.Errorf("query %q flags %q", gotQuery, gotFlags)
	}
	if len(entries) != 3 || entries[0].Line != "newest" || entries[1].Line != "new" || entries[2].Line != "old" {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Field("alloc_id") != "a2" || entries[1].Field("alloc_id") != "a1" || entries[1].Field("task") != "web" {
		t.Errorf("fields = %+v", entries)
	}
}

func TestParseLokiOrigin(t *testing.T) {
	for _, ok := range []string{"http://loki:3100", "https://loki.example.com/", "http://127.0.0.1:3100"} {
		if _, err := ParseLokiOrigin(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "loki:3100", "ftp://loki", "http://u:p@loki", "http://loki/path", "http://loki?x=1", "http://loki#f", "http://"} {
		if _, err := ParseLokiOrigin(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
