package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
)

func TestCronTriggerHTTPWorkerNomadPostgres(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	if address == "" || os.Getenv("NORN_TEST_DATABASE_URL") == "" {
		t.Skip("set disposable NORN_TEST_NOMAD_ADDR and NORN_TEST_DATABASE_URL")
	}
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "http" || net.ParseIP(endpoint.Hostname()) == nil || !net.ParseIP(endpoint.Hostname()).IsLoopback() {
		t.Fatal("cron trigger qualification requires disposable loopback Nomad")
	}
	db := acceptanceIntegrationDB(t)
	client, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	app := "cron-trigger-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	root := t.TempDir()
	appDir := filepath.Join(root, app)
	if err := os.MkdirAll(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte("name: "+app+"\ndeploy: true\nprocesses:\n  nightly:\n    command: /bin/true\n    schedule: '0 0 * * *'\n    timezone: UTC\n"), 0600); err != nil {
		t.Fatal(err)
	}
	spec, err := model.LoadInfraSpec(filepath.Join(appDir, "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	job := nomad.TranslatePeriodic(spec, "nightly", spec.Processes["nightly"], "busybox:1.36", nil)
	jobID := app + "-nightly"
	t.Cleanup(func() { _, _, _ = client.API().Jobs().Deregister(jobID, true, nil) })
	if _, _, err := client.API().Jobs().Register(job, nil); err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{DB: db, Nomad: client, AppsDir: root, SagaStore: saga.NewPostgresStore(db.Pool)}
	p.CronTriggerEffects, err = pipeline.NewCronTriggerEffects(db, client)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, client, nil, nil, &config.Config{AppsDir: root, AuditSigningKey: strings.Repeat("c", 32)}, p, nil, nil, p.SagaStore, nil, nil)
	p.SetOperationStore(h.OperationStore())
	serve := func(key string) (int, model.Operation, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/"+app+"/cron/trigger", bytes.NewBufferString(`{"process":"nightly"}`))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		route := chi.NewRouteContext()
		route.URLParams.Add("id", app)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "cron-trigger-token", DeviceID: "cron-trigger-device", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.CronTrigger)).ServeHTTP(rec, req)
		var op model.Operation
		if rec.Code == http.StatusAccepted || rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &op); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, op, rec.Body.String()
	}
	if code, _, body := serve(""); code != http.StatusBadRequest {
		t.Fatalf("missing key status=%d body=%s", code, body)
	}
	code, accepted, body := serve("same-key")
	if code != http.StatusAccepted || accepted.ID == "" || accepted.Kind != "app.cron-trigger" {
		t.Fatalf("accept status=%d op=%+v body=%s", code, accepted, body)
	}
	if code, replay, body := serve("same-key"); code != http.StatusOK || replay.ID != accepted.ID {
		t.Fatalf("replay status=%d op=%+v body=%s", code, replay, body)
	}
	runs, err := client.PeriodicChildren(jobID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("dispatch before worker: runs=%+v err=%v", runs, err)
	}
	claimed, claim, err := db.ClaimNextOperation(context.Background(), "cron-trigger-worker", time.Minute, []string{"app.cron-trigger"})
	if err != nil || claimed == nil || claimed.ID != accepted.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	result, err := p.ExecuteOperation(context.Background(), claimed, claim)
	if err != nil || result == nil || result.Status != model.OperationSucceeded || result.Metadata["evalId"] == "" {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	if err := db.FinishClaimedOperation(context.Background(), claim, result.Status, result.Message, result.Metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PeriodicForceEvaluation(context.Background(), result.Metadata["evalId"].(string), jobID); err != nil {
		t.Fatalf("Nomad evaluation proof: %v", err)
	}
}
