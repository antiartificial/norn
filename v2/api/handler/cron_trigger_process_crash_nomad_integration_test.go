package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/config"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

// TestCronTriggerWorkerProcessCrashNomadPostgres kills an OS process after its
// durable trigger-effect reservation and before it can force Nomad. The proxy
// is only a transport barrier: the periodic parent is registered with, and the
// first worker read is served by, the supplied live Nomad agent.
//
// A replacement worker receives the reserved effect after claim recovery. It
// must not call Force; after its bounded recovery attempts it leaves a manual
// review receipt and the unresolved reservation. This is intentionally opt-in:
// it needs disposable loopback Nomad and PostgreSQL services.
func TestCronTriggerWorkerProcessCrashNomadPostgres(t *testing.T) {
	if os.Getenv("NORN_CRON_TRIGGER_CRASH_WORKER") == "1" {
		runCronTriggerCrashWorker(t)
		return
	}
	address, databaseURL := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_DATABASE_URL")
	if address == "" || databaseURL == "" {
		t.Skip("set disposable NORN_TEST_NOMAD_ADDR and NORN_TEST_DATABASE_URL")
	}
	target, err := url.Parse(address)
	if err != nil || target.Scheme != "http" || net.ParseIP(target.Hostname()) == nil || !net.ParseIP(target.Hostname()).IsLoopback() {
		t.Fatal("process-crash qualification requires disposable loopback Nomad")
	}

	db, schema := cronTriggerCrashDB(t, databaseURL)
	realNomad, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	app, root, jobID := cronTriggerCrashApp(t, realNomad)
	beforeLaunch := make(chan struct{})
	proxy := cronTriggerCrashProxy(t, target, jobID, beforeLaunch)
	t.Cleanup(proxy.Close)

	// The handler owns signed acceptance. Its pre-admission Nomad read goes to
	// the live agent; only the separate worker process uses the barrier.
	p := &pipeline.Pipeline{DB: db, Nomad: realNomad, AppsDir: root, SagaStore: saga.NewPostgresStore(db.Pool)}
	p.CronTriggerEffects, err = pipeline.NewCronTriggerEffects(db, realNomad)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, realNomad, nil, nil, &config.Config{AppsDir: root, AuditSigningKey: cronTriggerCrashAuditKey}, p, nil, nil, p.SagaStore, nil, nil)
	p.SetOperationStore(h.OperationStore())
	accepted := cronTriggerCrashAccept(t, h, app)

	crashed := cronTriggerCrashWorkerCommand(t, databaseURL, schema, proxy.URL, root)
	if err := crashed.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = crashed.Process.Kill(); _, _ = crashed.Process.Wait() })
	select {
	case <-beforeLaunch:
	case <-time.After(20 * time.Second):
		t.Fatal("worker did not reach post-reservation Nomad boundary")
	}
	var lifecycle string
	if err := db.Pool.QueryRow(context.Background(), `SELECT lifecycle FROM operation_effects WHERE operation_id=$1`, accepted.ID).Scan(&lifecycle); err != nil || lifecycle != "reserved" {
		t.Fatalf("effect before kill lifecycle=%q err=%v", lifecycle, err)
	}
	if err := crashed.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL worker: %v", err)
	}
	if err := crashed.Wait(); err == nil {
		t.Fatal("worker unexpectedly exited cleanly instead of being killed")
	}

	childrenBefore, err := realNomad.PeriodicChildren(jobID)
	if err != nil || len(childrenBefore) != 0 {
		t.Fatalf("Nomad changed before Force boundary children=%+v err=%v", childrenBefore, err)
	}
	// Crash recovery uses elapsed ownership time, not a synthetic in-process
	// claim. Expiring this disposable row makes the restarted worker exercise
	// the production recovery path without waiting through the 90-second lease.
	if _, err := db.Pool.Exec(context.Background(), `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, accepted.ID); err != nil {
		t.Fatal(err)
	}
	restarted := cronTriggerCrashWorkerCommand(t, databaseURL, schema, address, root)
	if err := restarted.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Process.Kill(); _, _ = restarted.Process.Wait() })
	final := cronTriggerCrashWaitForTerminal(t, db, accepted.ID, 25*time.Second)
	if final.Status != model.OperationFailed || final.Metadata["manualRecoveryRequired"] != true || final.Metadata["externalEffectRecoveryPending"] != true || final.Metadata["retryBudgetExhausted"] != true {
		t.Fatalf("restarted receipt=%+v", final)
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT lifecycle FROM operation_effects WHERE operation_id=$1`, accepted.ID).Scan(&lifecycle); err != nil || lifecycle != "reserved" {
		t.Fatalf("effect after recovery lifecycle=%q err=%v", lifecycle, err)
	}
	childrenAfter, err := realNomad.PeriodicChildren(jobID)
	if err != nil || len(childrenAfter) != 0 {
		t.Fatalf("restart issued Nomad Force children=%+v err=%v", childrenAfter, err)
	}
}

const cronTriggerCrashAuditKey = "cccccccccccccccccccccccccccccccc"

func runCronTriggerCrashWorker(t *testing.T) {
	t.Helper()
	db, err := openCronTriggerCrashDB(os.Getenv("NORN_TEST_DATABASE_URL"), os.Getenv("NORN_CRON_TRIGGER_CRASH_SCHEMA"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client, err := nomad.NewClient(os.Getenv("NORN_TEST_NOMAD_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{DB: db, Nomad: client, AppsDir: os.Getenv("NORN_CRON_TRIGGER_CRASH_APPS_DIR"), SagaStore: saga.NewPostgresStore(db.Pool)}
	p.CronTriggerEffects, err = pipeline.NewCronTriggerEffects(db, client)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, client, nil, nil, &config.Config{AppsDir: p.AppsDir, AuditSigningKey: cronTriggerCrashAuditKey}, p, nil, nil, p.SagaStore, nil, nil)
	p.SetOperationStore(h.OperationStore())
	worker.NewOperationWorkerForKinds(db, p, []string{"app.cron-trigger"}).Run(context.Background())
}

func cronTriggerCrashDB(t *testing.T, databaseURL string) (*store.DB, string) {
	t.Helper()
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(context.Background(), adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "cron_trigger_crash_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	db, err := openCronTriggerCrashDB(databaseURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db, schema
}

func openCronTriggerCrashDB(databaseURL, schema string) (*store.DB, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = map[string]string{}
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	if config.MaxConns < 4 {
		config.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, err
	}
	return &store.DB{Pool: pool}, nil
}

func cronTriggerCrashApp(t *testing.T, client *nomad.Client) (string, string, string) {
	t.Helper()
	app := "cron-crash-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	root, appDir := t.TempDir(), ""
	appDir = filepath.Join(root, app)
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
	jobID := app + "-nightly"
	job := nomad.TranslatePeriodic(spec, "nightly", spec.Processes["nightly"], "busybox:1.36", nil)
	if _, _, err := client.API().Jobs().Register(job, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _, _ = client.API().Jobs().Deregister(jobID, true, nil)
		jobs, _, err := client.API().Jobs().List(nil)
		if err != nil {
			return
		}
		for _, job := range jobs {
			if job != nil && strings.HasPrefix(job.ID, jobID+"/periodic-") {
				_, _, _ = client.API().Jobs().Deregister(job.ID, true, nil)
			}
		}
	})
	return app, root, jobID
}

func cronTriggerCrashProxy(t *testing.T, target *url.URL, jobID string, beforeLaunch chan<- struct{}) *httptest.Server {
	t.Helper()
	var reads int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/job/"+jobID {
			reads++
			if reads == 2 {
				close(beforeLaunch)
				<-r.Context().Done()
				return
			}
		}
		upstream := r.Clone(r.Context())
		upstream.URL.Scheme, upstream.URL.Host, upstream.Host, upstream.RequestURI = target.Scheme, target.Host, target.Host, ""
		response, err := http.DefaultTransport.RoundTrip(upstream)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
}

func cronTriggerCrashAccept(t *testing.T, h *Handler, app string) model.Operation {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/"+app+"/cron/trigger", bytes.NewBufferString(`{"process":"nightly"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "cron-trigger-process-crash")
	route := chi.NewRouteContext()
	route.URLParams.Add("id", app)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "cron-crash-token", DeviceID: "cron-crash-device", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
	rec := httptest.NewRecorder()
	h.MutationAuditMiddleware(http.HandlerFunc(h.CronTrigger)).ServeHTTP(rec, req)
	var accepted model.Operation
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &accepted) != nil || accepted.ID == "" {
		t.Fatalf("cron trigger acceptance status=%d body=%s operation=%+v", rec.Code, rec.Body.String(), accepted)
	}
	return accepted
}

func cronTriggerCrashWorkerCommand(t *testing.T, databaseURL, schema, address, appsDir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCronTriggerWorkerProcessCrashNomadPostgres$")
	cmd.Env = append(os.Environ(), "NORN_CRON_TRIGGER_CRASH_WORKER=1", "NORN_CRON_TRIGGER_CRASH_SCHEMA="+schema, "NORN_CRON_TRIGGER_CRASH_APPS_DIR="+appsDir, "NORN_TEST_DATABASE_URL="+databaseURL, "NORN_TEST_NOMAD_ADDR="+address)
	return cmd
}

func cronTriggerCrashWaitForTerminal(t *testing.T, db *store.DB, operationID string, timeout time.Duration) *model.Operation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		op, err := db.GetOperation(context.Background(), operationID)
		if err == nil && !op.Active() {
			return op
		}
		time.Sleep(200 * time.Millisecond)
	}
	op, err := db.GetOperation(context.Background(), operationID)
	t.Fatalf("operation did not terminalize after restarted worker operation=%+v err=%v", op, err)
	return nil
}
