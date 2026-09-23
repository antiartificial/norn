package logcollect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/nomad"
)

// The collector runs over the real Nomad client against a fake Nomad HTTP
// API: allocation labels come from the allocation's own node, group and
// task states; frames carry log file names and offsets. This checks the
// adapter plumbing, not behaviour of a real Nomad agent.
func TestCollectorOverNomadClientAgainstFakeHTTPAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/job/shop/allocations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: "alloc-9", JobID: "shop", NodeID: "node-7", NodeName: "fleet-7", TaskGroup: "api",
				ClientStatus: "complete", TaskStates: map[string]*nomadapi.TaskState{"api": {}, "log-shipper": {}}}})
		case "/v1/client/fs/logs/alloc-9":
			task, stream := r.URL.Query().Get("task"), r.URL.Query().Get("type")
			for index, text := range []string{task + " " + stream + " a\n", task + " " + stream + " b\n"} {
				_ = json.NewEncoder(w).Encode(&nomadapi.StreamFrame{File: "alloc/logs/" + task + "." + stream + "." + string(rune('0'+index)), Offset: 0, Data: []byte(text)})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := nomad.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	spool := openTestSpool(t, filepath.Join(t.TempDir(), "spool"), testLimits())
	defer spool.Close()
	collector := &Collector{Source: NomadSource{Client: client}, Spool: spool, Jobs: func() []Job { return []Job{{App: "shop", JobID: "shop"}} }}
	if err := collector.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	collector.Wait()
	for _, task := range []string{"api", "log-shipper"} {
		for _, stream := range []string{"stdout", "stderr"} {
			text, gaps := streamText(t, spool, "alloc-9", task, stream)
			if text != task+" "+stream+" a\n"+task+" "+stream+" b\n" || len(gaps) != 0 {
				t.Fatalf("%s/%s = %q gaps %v", task, stream, text, gaps)
			}
		}
	}
	result, err := spool.Read(Query{App: "shop", ScanBytes: 1 << 20})
	if err != nil || len(result.Entries) != 8 {
		t.Fatalf("entries = %d, %v", len(result.Entries), err)
	}
	for _, entry := range result.Entries {
		if entry.NodeName != "fleet-7" || entry.NodeID != "node-7" || entry.TaskGroup != "api" || entry.AllocID != "alloc-9" {
			t.Fatalf("labels = %+v", entry.Labels)
		}
	}
}
