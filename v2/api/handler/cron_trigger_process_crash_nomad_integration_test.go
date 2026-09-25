package handler

import (
	"bytes"
	"compress/gzip"
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
	"sync/atomic"
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
// Two replacement workers race the recovered receipt after the process crash.
// The winner receives the reserved effect after claim recovery and must not
// call Force; its bounded recovery leaves a manual-review receipt and the
// unresolved reservation. The other replica cannot claim that same receipt.
// This is intentionally opt-in: it needs disposable loopback Nomad and
// PostgreSQL services.
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
	// Start two independent API-worker processes at the recovered receipt. The
	// signed acceptance admits only this one mutable app operation, so exactly
	// one process may reclaim it. The winner must see the first process's
	// unresolved effect and leave it for review; neither replica may Force the
	// live Nomad parent a second time.
	restarted := []*exec.Cmd{
		cronTriggerCrashWorkerCommand(t, databaseURL, schema, address, root),
		cronTriggerCrashWorkerCommand(t, databaseURL, schema, address, root),
	}
	for index, command := range restarted {
		if err := command.Start(); err != nil {
			t.Fatalf("start recovered worker %d: %v", index+1, err)
		}
		t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })
	}
	final := cronTriggerCrashWaitForTerminal(t, db, accepted.ID, 25*time.Second)
	if final.Status != model.OperationFailed || final.Attempts != final.MaxAttempts || final.Metadata["manualRecoveryRequired"] != true || final.Metadata["externalEffectRecoveryPending"] != true || final.Metadata["retryBudgetExhausted"] != true {
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

// TestCronTriggerWorkerProcessCrashAfterNomadForceNomadPostgres kills an OS
// process only after live Nomad has accepted PeriodicForce and returned its
// exact evaluation, while the normal worker still has no response to persist.
// Recovery must keep the reservation ambiguous and must never issue Force a
// second time. This is opt-in because it requires disposable loopback Nomad
// and PostgreSQL services.
func TestCronTriggerWorkerProcessCrashAfterNomadForceNomadPostgres(t *testing.T) {
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
	forced := make(chan string, 1)
	var forceCalls atomic.Int32
	proxy := cronTriggerCrashAfterForceProxy(t, target, jobID, forced, &forceCalls)
	t.Cleanup(proxy.Close)

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
	var evalID string
	select {
	case evalID = <-forced:
	case <-time.After(20 * time.Second):
		t.Fatal("worker did not reach post-Force acknowledgement boundary")
	}
	if _, err := realNomad.PeriodicForceEvaluation(context.Background(), evalID, jobID); err != nil {
		t.Fatalf("live Nomad did not retain exact forced evaluation %q: %v", evalID, err)
	}
	var lifecycle, runtimeID string
	if err := db.Pool.QueryRow(context.Background(), `SELECT lifecycle,COALESCE(runtime_instance_id,'') FROM operation_effects WHERE operation_id=$1`, accepted.ID).Scan(&lifecycle, &runtimeID); err != nil || lifecycle != "reserved" || runtimeID != "" {
		t.Fatalf("effect before kill lifecycle=%q runtime=%q err=%v", lifecycle, runtimeID, err)
	}
	if err := crashed.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL worker: %v", err)
	}
	if err := crashed.Wait(); err == nil {
		t.Fatal("worker unexpectedly exited cleanly instead of being killed")
	}
	if got := forceCalls.Load(); got != 1 {
		t.Fatalf("live Nomad Force calls before recovery=%d want 1", got)
	}

	// Expire only the disposable worker lease so the successor follows the
	// ordinary claim-recovery path rather than an in-process simulation.
	if _, err := db.Pool.Exec(context.Background(), `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, accepted.ID); err != nil {
		t.Fatal(err)
	}
	restarted := cronTriggerCrashWorkerCommand(t, databaseURL, schema, proxy.URL, root)
	if err := restarted.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Process.Kill(); _, _ = restarted.Process.Wait() })
	final := cronTriggerCrashWaitForTerminal(t, db, accepted.ID, 25*time.Second)
	if final.Status != model.OperationFailed || final.Metadata["manualRecoveryRequired"] != true || final.Metadata["externalEffectRecoveryPending"] != true || final.Metadata["retryBudgetExhausted"] != true {
		t.Fatalf("restarted receipt=%+v", final)
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT lifecycle,COALESCE(runtime_instance_id,'') FROM operation_effects WHERE operation_id=$1`, accepted.ID).Scan(&lifecycle, &runtimeID); err != nil || lifecycle != "reserved" || runtimeID != "" {
		t.Fatalf("effect after recovery lifecycle=%q runtime=%q err=%v", lifecycle, runtimeID, err)
	}
	if got := forceCalls.Load(); got != 1 {
		t.Fatalf("recovery issued a second Nomad Force: calls=%d", got)
	}
}

// TestCronTriggerWorkerProcessCrashAfterNomadAcknowledgementNomadPostgres
// kills a distinct normal worker after MarkLaunched has committed the exact
// Nomad evaluation identity, but before it can verify that evaluation and
// write the completed effect/operation receipt. The proxy holds only that
// post-acknowledgement evaluation read, which cannot start until MarkLaunched
// has returned. A replacement worker must use the durable acknowledgement to
// complete successfully without forcing the periodic parent a second time.
func TestCronTriggerWorkerProcessCrashAfterNomadAcknowledgementNomadPostgres(t *testing.T) {
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
	acknowledged := make(chan string, 1)
	var forceCalls atomic.Int32
	var releaseEvaluationRead atomic.Bool
	proxy := cronTriggerCrashAfterLaunchProxy(t, target, jobID, acknowledged, &forceCalls, &releaseEvaluationRead)
	t.Cleanup(proxy.Close)

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
	var evalID string
	select {
	case evalID = <-acknowledged:
	case <-time.After(20 * time.Second):
		t.Fatal("worker did not reach post-acknowledgement evaluation boundary")
	}
	if _, err := realNomad.PeriodicForceEvaluation(context.Background(), evalID, jobID); err != nil {
		t.Fatalf("live Nomad did not retain acknowledged evaluation %q: %v", evalID, err)
	}
	var lifecycle, runtimeID string
	if err := db.Pool.QueryRow(context.Background(), `SELECT lifecycle,COALESCE(runtime_instance_id,'') FROM operation_effects WHERE operation_id=$1`, accepted.ID).Scan(&lifecycle, &runtimeID); err != nil || lifecycle != "launched" || runtimeID != evalID {
		t.Fatalf("effect before kill lifecycle=%q runtime=%q eval=%q err=%v", lifecycle, runtimeID, evalID, err)
	}
	if err := crashed.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL worker: %v", err)
	}
	if err := crashed.Wait(); err == nil {
		t.Fatal("worker unexpectedly exited cleanly instead of being killed")
	}
	if got := forceCalls.Load(); got != 1 {
		t.Fatalf("live Nomad Force calls before recovery=%d want 1", got)
	}

	// Keep the successor on the same proxy. Releasing its evaluation read lets
	// it complete while the proxy still rejects any duplicate Force before it
	// can reach Nomad.
	releaseEvaluationRead.Store(true)
	if _, err := db.Pool.Exec(context.Background(), `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, accepted.ID); err != nil {
		t.Fatal(err)
	}
	restarted := cronTriggerCrashWorkerCommand(t, databaseURL, schema, proxy.URL, root)
	if err := restarted.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Process.Kill(); _, _ = restarted.Process.Wait() })
	final := cronTriggerCrashWaitForTerminal(t, db, accepted.ID, 25*time.Second)
	if final.Status != model.OperationSucceeded || final.Metadata["evalId"] != evalID {
		t.Fatalf("restarted receipt=%+v want eval=%q", final, evalID)
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT lifecycle,COALESCE(runtime_instance_id,'') FROM operation_effects WHERE operation_id=$1`, accepted.ID).Scan(&lifecycle, &runtimeID); err != nil || lifecycle != "completed" || runtimeID != evalID {
		t.Fatalf("effect after recovery lifecycle=%q runtime=%q eval=%q err=%v", lifecycle, runtimeID, evalID, err)
	}
	if got := forceCalls.Load(); got != 1 {
		t.Fatalf("recovery issued a second Nomad Force: calls=%d", got)
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

// cronTriggerCrashAfterForceProxy drains a successful live Nomad response,
// records its EvalID, then withholds it from the worker. Later Force attempts
// are rejected and counted, so the recovered process cannot hide a retry.
func cronTriggerCrashAfterForceProxy(t *testing.T, target *url.URL, jobID string, forced chan<- string, forceCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		force := r.Method == http.MethodPut && r.URL.Path == "/v1/job/"+jobID+"/periodic/force"
		if force && forceCalls.Add(1) != 1 {
			http.Error(w, "second Force rejected by crash qualification", http.StatusConflict)
			return
		}
		upstream := r.Clone(r.Context())
		upstream.URL.Scheme, upstream.URL.Host, upstream.Host, upstream.RequestURI = target.Scheme, target.Host, target.Host, ""
		response, err := http.DefaultTransport.RoundTrip(upstream)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		if force {
			evalID, err := cronTriggerCrashEvalID(response)
			if err != nil || evalID == "" {
				t.Errorf("read successful Nomad Force response: eval=%q err=%v", evalID, err)
				return
			}
			forced <- evalID
			<-r.Context().Done()
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
}

// cronTriggerCrashAfterLaunchProxy forwards the Force response, then blocks
// the immediately following evaluation request. That request is issued only
// after MarkLaunched has committed, making it a deterministic post-acknowledge
// and pre-completion process-crash boundary.
func cronTriggerCrashAfterLaunchProxy(t *testing.T, target *url.URL, jobID string, acknowledged chan<- string, forceCalls *atomic.Int32, releaseEvaluationRead *atomic.Bool) *httptest.Server {
	t.Helper()
	var evalID atomic.Value
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		force := r.Method == http.MethodPut && r.URL.Path == "/v1/job/"+jobID+"/periodic/force"
		if force && forceCalls.Add(1) != 1 {
			http.Error(w, "second Force rejected by crash qualification", http.StatusConflict)
			return
		}
		if r.Method == http.MethodGet && !releaseEvaluationRead.Load() && strings.HasPrefix(r.URL.Path, "/v1/evaluation/") {
			if value, ok := evalID.Load().(string); ok && r.URL.Path == "/v1/evaluation/"+value {
				acknowledged <- value
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
		if force {
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Errorf("read successful Nomad Force response: %v", err)
				return
			}
			copied := &http.Response{Header: response.Header, Body: io.NopCloser(bytes.NewReader(body))}
			id, err := cronTriggerCrashEvalID(copied)
			if err != nil || id == "" {
				t.Errorf("read successful Nomad Force response: eval=%q err=%v", id, err)
				return
			}
			evalID.Store(id)
			for key, values := range response.Header {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(body)
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
}

func TestCronTriggerCrashAfterForceProxyRejectsReplayBeforeNomad(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	var forceCalls atomic.Int32
	forceCalls.Store(1)
	proxy := cronTriggerCrashAfterForceProxy(t, target, "test-parent", make(chan string, 1), &forceCalls)
	defer proxy.Close()
	req, err := http.NewRequest(http.MethodPut, proxy.URL+"/v1/job/test-parent/periodic/force", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || forceCalls.Load() != 2 || upstreamCalls.Load() != 0 {
		t.Fatalf("replay status=%d calls=%d upstream=%d", response.StatusCode, forceCalls.Load(), upstreamCalls.Load())
	}
}

func cronTriggerCrashEvalID(response *http.Response) (string, error) {
	var body io.Reader = response.Body
	if response.Header.Get("Content-Encoding") == "gzip" {
		compressed, err := gzip.NewReader(response.Body)
		if err != nil {
			return "", err
		}
		defer compressed.Close()
		body = compressed
	}
	var result struct {
		EvalID string `json:"EvalID"`
	}
	if err := json.NewDecoder(body).Decode(&result); err != nil {
		return "", err
	}
	return result.EvalID, nil
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
