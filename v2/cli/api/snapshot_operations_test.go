package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSnapshotMutationsQueueOperationsWithStableKeys(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Idempotency-Key") != "operator-retry" || r.Method != http.MethodPost {
			t.Fatalf("request %d missing durable key: %s %q", requests, r.Method, r.Header.Get("Idempotency-Key"))
		}
		if r.URL.Query().Get("database") != "primary" || r.URL.Query().Get("confirm") != "true" {
			t.Fatalf("request %d query=%v", requests, r.URL.Query())
		}
		switch requests {
		case 1:
			if r.URL.Path != "/api/apps/shop/snapshots/20260924T120000/restore" {
				t.Fatalf("restore path=%q", r.URL.Path)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(Operation{ID: "restore-1", Kind: "app.snapshot-restore", Status: "queued"})
		case 2:
			if r.URL.Path != "/api/apps/shop/snapshots/retention" || r.URL.Query().Get("keep") != "3" {
				t.Fatalf("prune path=%q query=%v", r.URL.Path, r.URL.Query())
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(Operation{ID: "prune-1", Kind: "app.snapshot-prune", Status: "queued"})
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	client := New(server.URL)
	restore, err := client.QueueSnapshotRestore("shop", "20260924T120000", "operator-retry", "primary")
	if err != nil || restore.ID != "restore-1" || restore.Status != "queued" {
		t.Fatalf("restore=%+v err=%v", restore, err)
	}
	prune, err := client.QueueSnapshotPrune("shop", 3, "operator-retry", "primary")
	if err != nil || prune.ID != "prune-1" || prune.Status != "queued" {
		t.Fatalf("prune=%+v err=%v", prune, err)
	}
}
