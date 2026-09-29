package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
)

func TestSnapshotCommandQueuesMutationsAndPrintsRetryKey(t *testing.T) {
	priorClient, priorYes, priorExecute := client, snapshotRestoreYes, snapshotRetentionExecute
	priorWait, priorKey, priorDatabase := snapshotWait, snapshotIdempotencyKey, snapshotDatabase
	t.Cleanup(func() {
		client, snapshotRestoreYes, snapshotRetentionExecute = priorClient, priorYes, priorExecute
		snapshotWait, snapshotIdempotencyKey, snapshotDatabase = priorWait, priorKey, priorDatabase
	})
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Idempotency-Key") != "restore-retry" || r.URL.Query().Get("database") != "primary" {
			t.Fatalf("request %d key=%q query=%v", requests, r.Header.Get("Idempotency-Key"), r.URL.Query())
		}
		if requests == 1 && !strings.Contains(r.URL.Path, "/restore") {
			t.Fatalf("restore path=%q", r.URL.Path)
		}
		if requests == 2 && !strings.Contains(r.URL.Path, "/retention") {
			t.Fatalf("prune path=%q", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"operation-1","kind":"app.snapshot-restore","status":"queued"}`))
	}))
	defer server.Close()
	client = api.New(server.URL)
	snapshotRestoreYes, snapshotWait = true, false
	snapshotIdempotencyKey, snapshotDatabase = "restore-retry", "primary"
	var stderr bytes.Buffer
	snapshotsCmd.SetErr(&stderr)
	if err := snapshotsCmd.RunE(snapshotsCmd, []string{"shop", "restore", "20260924T120000"}); err != nil {
		t.Fatal(err)
	}
	snapshotRetentionExecute = true
	if err := snapshotsCmd.RunE(snapshotsCmd, []string{"shop", "retention"}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || strings.Count(stderr.String(), "idempotency key: restore-retry") != 2 {
		t.Fatalf("requests=%d stderr=%q", requests, stderr.String())
	}
}

func TestSnapshotWaitSurfacesStatusLookupFailure(t *testing.T) {
	priorClient, priorWait, priorTimeout := client, snapshotWait, snapshotTimeout
	t.Cleanup(func() { client, snapshotWait, snapshotTimeout = priorClient, priorWait, priorTimeout })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "operation read forbidden", http.StatusForbidden)
	}))
	defer server.Close()
	client = api.New(server.URL)
	snapshotWait = true
	snapshotTimeout = 5 * time.Second
	command := &cobra.Command{}
	command.SetContext(context.Background())
	err := handleSnapshotOperation(command, &api.Operation{ID: "restore-1", Kind: "app.snapshot-restore", Status: "queued"})
	if err == nil || !strings.Contains(err.Error(), "status lookup failed") || !strings.Contains(err.Error(), "restore-1") {
		t.Fatalf("error=%v", err)
	}
}
