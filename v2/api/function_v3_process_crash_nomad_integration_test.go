package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
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
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// TestClaimedFunctionV3WorkerProcessCrashNomadPostgres kills an OS worker
// after Nomad accepted the one-shot function job and the write has been
// recorded durably, but before that worker can obtain its terminal allocation
// evidence. Two separate replacement worker processes use the same proxy.
// It rejects a second register before forwarding it, proving recovery neither
// creates nor reaches Nomad with a duplicate external effect.
//
// This is opt-in and uses only a disposable loopback Nomad agent and
// PostgreSQL database.
func TestClaimedFunctionV3WorkerProcessCrashNomadPostgres(t *testing.T) {
	if os.Getenv("NORN_FUNCTION_V3_CRASH_WORKER") == "1" {
		runFunctionV3CrashWorker(t)
		return
	}
	address, image, databaseURL := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_FUNCTION_IMAGE"), os.Getenv("NORN_TEST_DATABASE_URL")
	if address == "" || image == "" || databaseURL == "" || os.Getenv("NORN_TEST_NOMAD_DOCKER") != "1" {
		t.Skip("set disposable NORN_TEST_NOMAD_ADDR, NORN_TEST_FUNCTION_IMAGE, NORN_TEST_DATABASE_URL, and NORN_TEST_NOMAD_DOCKER=1")
	}
	target, err := url.Parse(address)
	if err != nil || target.Scheme != "http" || net.ParseIP(target.Hostname()) == nil || !net.ParseIP(target.Hostname()).IsLoopback() || !model.IsContentAddressedImage(image) {
		t.Fatal("qualification requires loopback Nomad and a content-addressed image")
	}

	db, schema := functionV3CrashDB(t, databaseURL)
	realNomad, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	app, appsDir, spec, digest := functionV3CrashApp(t, realNomad, image)
	deployment := &model.Deployment{ID: uuid.NewString(), App: app, SagaID: uuid.NewString(), Status: model.StatusDeployed, ImageTag: image, SpecDigest: digest, Environment: "staging", CommitSHA: strings.Repeat("a", 40), SourceKind: "git_clone", SourceRef: strings.Repeat("a", 40), StartedAt: time.Now().UTC().Add(-time.Second)}
	if err := db.InsertDeployment(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for {
		if err := realNomad.VerifyRunningAppImage(ctx, spec, image); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("service image never became provable: %v", err)
		}
		time.Sleep(time.Second)
	}

	keyMaterial := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, 32))
	cfg := functionV3CrashConfig(appsDir, keyMaterial)
	pipe := &pipeline.Pipeline{DB: db, Nomad: realNomad, AppsDir: appsDir}
	h := handler.New(db, realNomad, nil, nil, cfg, pipe, nil, nil, nil, nil, nil)
	if err := h.OperationStoreError(); err != nil || h.OperationStore() == nil {
		t.Fatalf("operation acceptance=%v store=%T", err, h.OperationStore())
	}
	pipe.SetOperationStore(h.OperationStore())
	admission, _, err := configureFunctionV3(cfg, db, pipe, realNomad, nil, h.OperationStore())
	if err != nil || admission == nil {
		t.Fatalf("configure function v3 admission=%v err=%v", admission, err)
	}
	accepted := functionV3CrashAccept(t, h, admission, app)

	blocked := make(chan struct{}, 1)
	var held, releaseTerminalRead, registrations atomic.Bool
	var registrationCalls atomic.Int32
	proxy := functionV3CrashProxy(t, target, &held, &releaseTerminalRead, blocked, &registrations, &registrationCalls)
	defer proxy.Close()
	crashed := functionV3CrashWorkerCommand(t, databaseURL, schema, proxy.URL, appsDir, keyMaterial, false)
	if err := crashed.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = crashed.Process.Kill(); _, _ = crashed.Process.Wait() })
	select {
	case <-blocked:
	case <-time.After(30 * time.Second):
		t.Fatal("worker did not reach post-register terminal-observation boundary")
	}
	var jobID string
	if err := db.Pool.QueryRow(context.Background(), `SELECT target FROM function_invocation_effect_attempts WHERE operation_id=$1 AND stage='job'`, accepted.ID).Scan(&jobID); err != nil || jobID == "" {
		t.Fatalf("durable job attempt before kill job=%q err=%v", jobID, err)
	}
	job, _, err := realNomad.API().Jobs().Info(jobID, nil)
	if err != nil || job == nil || job.Version == nil || *job.Version != 0 {
		t.Fatalf("live one-shot job before kill job=%v err=%v", job, err)
	}
	if err := crashed.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL worker: %v", err)
	}
	if err := crashed.Wait(); err == nil {
		t.Fatal("worker unexpectedly exited cleanly instead of being killed")
	}
	if got := registrationCalls.Load(); got != 1 {
		t.Fatalf("Nomad register calls before recovery=%d want 1", got)
	}
	if _, err := db.Pool.Exec(context.Background(), `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, accepted.ID); err != nil {
		t.Fatal(err)
	}
	releaseTerminalRead.Store(true)
	successors := []*exec.Cmd{
		functionV3CrashWorkerCommand(t, databaseURL, schema, proxy.URL, appsDir, keyMaterial, true),
		functionV3CrashWorkerCommand(t, databaseURL, schema, proxy.URL, appsDir, keyMaterial, true),
	}
	for index, successor := range successors {
		if err := successor.Start(); err != nil {
			t.Fatalf("start successor %d: %v", index+1, err)
		}
		t.Cleanup(func() { _ = successor.Process.Kill(); _, _ = successor.Process.Wait() })
	}
	final := functionV3CrashWaitTerminal(t, db, accepted.ID, 30*time.Second)
	if final.Status != model.OperationSucceeded {
		t.Fatalf("recovered operation=%+v", final)
	}
	for index, successor := range successors {
		if err := successor.Process.Kill(); err != nil {
			t.Fatalf("stop successor %d: %v", index+1, err)
		}
		functionV3CrashStop(t, successor, 10*time.Second, "successor "+string(rune('1'+index)))
	}
	if got := registrationCalls.Load(); got != 1 {
		t.Fatalf("recovery attempted duplicate Nomad register calls=%d", got)
	}
	job, _, err = realNomad.API().Jobs().Info(jobID, nil)
	if err != nil || job == nil || job.Version == nil || *job.Version != 0 {
		t.Fatalf("live one-shot job after recovery job=%v err=%v", job, err)
	}
	versions, _, _, err := realNomad.API().Jobs().Versions(jobID, false, nil)
	if err != nil || len(versions) != 1 || versions[0] == nil || versions[0].Version == nil || *versions[0].Version != 0 {
		t.Fatalf("one-shot job history after recovery versions=%+v err=%v", versions, err)
	}
	_, _, _ = realNomad.API().Jobs().Deregister(jobID, true, nil)
}

func runFunctionV3CrashWorker(t *testing.T) {
	t.Helper()
	db, err := openFunctionV3CrashDB(os.Getenv("NORN_TEST_DATABASE_URL"), os.Getenv("NORN_FUNCTION_V3_CRASH_SCHEMA"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client, err := nomad.NewClient(os.Getenv("NORN_TEST_NOMAD_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := functionV3CrashConfig(os.Getenv("NORN_FUNCTION_V3_CRASH_APPS_DIR"), os.Getenv("NORN_FUNCTION_V3_CRASH_KEY"))
	pipe := &pipeline.Pipeline{DB: db, Nomad: client, AppsDir: cfg.AppsDir}
	h := handler.New(db, client, nil, nil, cfg, pipe, nil, nil, nil, nil, nil)
	if err := h.OperationStoreError(); err != nil {
		t.Fatal(err)
	}
	pipe.SetOperationStore(h.OperationStore())
	_, claimed, err := configureFunctionV3(cfg, db, pipe, client, nil, h.OperationStore())
	if err != nil || claimed == nil {
		t.Fatalf("configure function worker=%v err=%v", claimed, err)
	}
	if os.Getenv("NORN_FUNCTION_V3_CRASH_RECOVER") == "1" {
		claimed.Run(context.Background(), 100*time.Millisecond)
		return
	}
	if err := claimed.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func functionV3CrashConfig(appsDir, keyMaterial string) *config.Config {
	return &config.Config{Profile: "production", Environment: "staging", AppsDir: appsDir, AuditSigningKey: strings.Repeat("a", 32), PrivateInvocationEnabled: true, FunctionV3PreviewEnabled: true, PrivateInvocationCurrentKeyID: "function-crash", PrivateInvocationKeys: `{"function-crash":"` + keyMaterial + `"}`}
}

func functionV3CrashDB(t *testing.T, databaseURL string) (*store.DB, string) {
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
	schema := "function_v3_crash_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	db, err := openFunctionV3CrashDB(databaseURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db, schema
}

func openFunctionV3CrashDB(databaseURL, schema string) (*store.DB, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if poolConfig.ConnConfig.RuntimeParams == nil {
		poolConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	if poolConfig.MaxConns < 4 {
		poolConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, err
	}
	return &store.DB{Pool: pool}, nil
}

func functionV3CrashApp(t *testing.T, client *nomad.Client, image string) (string, string, *model.InfraSpec, string) {
	t.Helper()
	app := "norn-fncrash-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	appsDir, appDir := t.TempDir(), ""
	appDir = filepath.Join(appsDir, app)
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte("name: "+app+"\ndeploy: true\nprocesses:\n  web:\n    command: sleep 120\n  resize:\n    command: sleep 5; printf '%s' \\\"$NORN_REQUEST_BODY\\\" | sha256sum\n    function:\n      timeout: 30s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := model.LoadInfraSpec(filepath.Join(appDir, "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	service := nomad.TranslateForRegion(spec, image, nil, spec.ResolvedRegions()[0])
	service.TaskGroups[0].Tasks[0].Config["force_pull"] = false
	if _, _, err := client.API().Jobs().Register(service, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _, _ = client.API().Jobs().Deregister(app, true, nil) })
	return app, appsDir, spec, digest
}

func functionV3CrashAccept(t *testing.T, h *handler.Handler, admission http.HandlerFunc, app string) model.Operation {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/"+app+"/invoke", strings.NewReader(`{"process":"resize","body":"function-crash-private","method":"POST","path":"/private/function"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "function-v3-process-crash")
	route := chi.NewRouteContext()
	route.URLParams.Add("id", app)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	req = handler.WithAccessPrincipal(req, &handler.AccessPrincipal{Subject: "operator", TokenID: "function-crash-token", DeviceID: "function-crash-device", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeAPIWrite}})
	rec := httptest.NewRecorder()
	h.MutationAuditMiddleware(http.HandlerFunc(admission)).ServeHTTP(rec, req)
	var accepted model.Operation
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &accepted) != nil || accepted.ID == "" {
		t.Fatalf("function acceptance status=%d body=%s operation=%+v", rec.Code, rec.Body.String(), accepted)
	}
	return accepted
}

func functionV3CrashProxy(t *testing.T, target *url.URL, held, release *atomic.Bool, blocked chan<- struct{}, registrations *atomic.Bool, registrationCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	upstream := httputil.NewSingleHostReverseProxy(target)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/jobs" {
			call := registrationCalls.Add(1)
			if !registrations.CompareAndSwap(false, true) || call != 1 {
				http.Error(w, "duplicate function register rejected", http.StatusConflict)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/v1/job/norn-fn-") && strings.HasSuffix(r.URL.Path, "/allocations") && held.CompareAndSwap(false, true) && !release.Load() {
			select {
			case blocked <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		upstream.ServeHTTP(w, r)
	}))
}

func functionV3CrashWorkerCommand(t *testing.T, databaseURL, schema, address, appsDir, keyMaterial string, recover bool) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestClaimedFunctionV3WorkerProcessCrashNomadPostgres$")
	cmd.Env = append(os.Environ(), "NORN_FUNCTION_V3_CRASH_WORKER=1", "NORN_TEST_DATABASE_URL="+databaseURL, "NORN_FUNCTION_V3_CRASH_SCHEMA="+schema, "NORN_TEST_NOMAD_ADDR="+address, "NORN_FUNCTION_V3_CRASH_APPS_DIR="+appsDir, "NORN_FUNCTION_V3_CRASH_KEY="+keyMaterial)
	if recover {
		cmd.Env = append(cmd.Env, "NORN_FUNCTION_V3_CRASH_RECOVER=1")
	}
	return cmd
}

func functionV3CrashWait(t *testing.T, command *exec.Cmd, timeout time.Duration, label string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s failed: %v", label, err)
		}
	case <-time.After(timeout):
		_ = command.Process.Kill()
		<-done
		t.Fatalf("%s did not exit", label)
	}
}

func functionV3CrashStop(t *testing.T, command *exec.Cmd, timeout time.Duration, label string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("%s did not stop", label)
	}
}

func functionV3CrashWaitTerminal(t *testing.T, db *store.DB, operationID string, timeout time.Duration) *model.Operation {
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
	t.Fatalf("function operation did not terminalize operation=%+v err=%v", op, err)
	return nil
}
