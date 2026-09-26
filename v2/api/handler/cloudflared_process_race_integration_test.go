package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/cloudflared"
	"norn/v2/api/config"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

const cloudflaredProcessAuditKey = "cloudflared-process-race-signing-key-0001"

// Two OS processes construct independent API handlers and claimed workers
// against one disposable PostgreSQL schema and one disposable host config.
// A PATH-scoped fake launchctl records restart calls; no live service changes.
func TestCloudflaredTwoAPIProcessRacePostgres(t *testing.T) {
	if os.Getenv("NORN_CLOUDFLARED_RACE_CHILD") == "1" {
		runCloudflaredRaceChild(t)
		return
	}
	if os.Getenv("NORN_TEST_DATABASE_URL") == "" {
		t.Skip("set NORN_TEST_DATABASE_URL to disposable PostgreSQL")
	}
	db := acceptanceIntegrationDB(t)
	var schema string
	if err := db.Pool.QueryRow(context.Background(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	apps := filepath.Join(root, "apps")
	if err := os.MkdirAll(filepath.Join(apps, "demo"), 0700); err != nil {
		t.Fatal(err)
	}
	spec := "name: demo\ndeploy: true\nprocesses: {}\nendpoints:\n  - url: https://demo.example.com\n"
	if err := os.WriteFile(filepath.Join(apps, "demo", "infraspec.yaml"), []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yml")
	if err := os.WriteFile(configPath, []byte("tunnel: disposable\ningress:\n  - hostname: demo.example.com\n    service: http://127.0.0.1:8080\n  - service: http_status:404\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	restarts := filepath.Join(root, "restarts.log")
	stub := "#!/bin/sh\ncase \"$1\" in\n  kickstart) printf 'restart\\n' >> \"$NORN_CLOUDFLARED_RESTART_LOG\" ;;\n  print) printf 'state = running\\n' ;;\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	first := startCloudflaredRaceChild(t, schema, apps, configPath, restarts, bin)
	second := startCloudflaredRaceChild(t, schema, apps, configPath, restarts, bin)
	if first.cmd.Process.Pid == second.cmd.Process.Pid {
		t.Fatal("API processes did not have distinct PIDs")
	}
	urls := []string{first.url, second.url}
	var accepted [2]model.Operation
	var codes [2]int
	var bodies [2]string
	var wg sync.WaitGroup
	for i := range urls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, urls[i]+"/api/apps/demo/teardown", strings.NewReader("{}"))
			if err != nil {
				bodies[i] = err.Error()
				return
			}
			req.Header.Set("Idempotency-Key", "two-api-teardown-key")
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				bodies[i] = err.Error()
				return
			}
			defer response.Body.Close()
			codes[i] = response.StatusCode
			data, _ := io.ReadAll(response.Body)
			bodies[i] = string(data)
			_ = json.Unmarshal(data, &accepted[i])
		}(i)
	}
	wg.Wait()
	for i := range accepted {
		if (codes[i] != http.StatusAccepted && codes[i] != http.StatusOK) || accepted[i].ID == "" || accepted[i].Kind != "app.cloudflared-mutate" {
			t.Fatalf("API %d status=%d body=%s stderr=%s", i, codes[i], bodies[i], []string{first.stderr.String(), second.stderr.String()}[i])
		}
	}
	if accepted[0].ID != accepted[1].ID {
		t.Fatalf("two API processes accepted different work: %s %s", accepted[0].ID, accepted[1].ID)
	}
	terminal := cronTriggerCrashWaitForTerminal(t, db, accepted[0].ID, 20*time.Second)
	if terminal.Status != model.OperationSucceeded {
		t.Fatalf("operation=%+v API stderr=%s | %s", terminal, first.stderr.String(), second.stderr.String())
	}
	var operations, effects int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE kind='app.cloudflared-mutate'`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_effects WHERE operation_id=$1`, accepted[0].ID).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || effects != 1 {
		t.Fatalf("operations=%d effects=%d", operations, effects)
	}
	log, err := os.ReadFile(restarts)
	if err != nil {
		t.Fatal(err)
	}
	if string(log) != "restart\n" {
		t.Fatalf("launchctl calls=%q", log)
	}
	priorConfigPath := cloudflared.ConfigPath()
	cloudflared.SetConfigPath(configPath)
	t.Cleanup(func() { cloudflared.SetConfigPath(priorConfigPath) })
	cfg, err := cloudflared.ReadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Ingress) != 1 || cfg.Ingress[0].Hostname != "" {
		t.Fatalf("post-effect ingress=%+v", cfg.Ingress)
	}
	signer, err := store.NewHMACAcceptanceSigner(cloudflaredProcessAuditKey)
	if err != nil {
		t.Fatal(err)
	}
	acceptance, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptance.VerifyAcceptedOperation(context.Background(), accepted[0].ID); err != nil {
		t.Fatalf("signed acceptance: %v", err)
	}
	// The same two child worker loops must refuse a signed operation belonging
	// to a different host. This observes the actual queued defer in PostgreSQL.
	_, before, err := cloudflared.ReadConfigSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wrong := cloudflared.Mutation{Action: "enable", App: "demo", Host: "different-host", ConfigPath: configPath, Hostnames: []string{"https://demo.example.com"}, Service: "http://127.0.0.1:8080", BeforeDigest: before}
	cfg, err = cloudflared.ReadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	wrong.AfterDigest, err = cloudflared.ConfigDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes, err := json.Marshal(wrong)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatal(err)
	}
	authority, err := acceptance.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	queue := &pipeline.Pipeline{DB: db, OperationStore: acceptance}
	now := time.Now().UTC()
	wrongOperation := model.Operation{ID: uuid.NewString(), Kind: "app.cloudflared-mutate", App: "demo", SagaID: uuid.NewString(), Ref: "enable", Status: model.OperationQueued, StartedAt: now, MaxAttempts: 3, Payload: payload}
	wrongAccepted, err := queue.QueueOperation(context.Background(), wrongOperation, pipeline.EnqueueRequest{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/test", Subject: "operator"}, Key: "different-host", Audit: store.AcceptanceAuditContext{Source: "two-process-qualification"}, Semantics: map[string]interface{}{"action": "enable", "app": "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	var wrongStored *model.Operation
	for time.Now().Before(deadline) {
		wrongStored, err = db.GetOperation(context.Background(), wrongAccepted.Operation.ID)
		if err == nil && wrongStored.Status == model.OperationQueued && strings.Contains(fmt.Sprint(wrongStored.Metadata["effectRecoveryReason"]), "another host") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if wrongStored == nil || wrongStored.Status != model.OperationQueued || !strings.Contains(fmt.Sprint(wrongStored.Metadata["effectRecoveryReason"]), "another host") {
		t.Fatalf("wrong-host worker did not defer: op=%+v err=%v", wrongStored, err)
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_effects WHERE operation_id=$1`, wrongAccepted.Operation.ID).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatalf("wrong-host worker reserved %d effects", effects)
	}
	log, err = os.ReadFile(restarts)
	if err != nil {
		t.Fatal(err)
	}
	if string(log) != "restart\n" {
		t.Fatalf("wrong-host worker restarted service: %q", log)
	}
}

type cloudflaredRaceProcess struct {
	url    string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *cloudflaredProcessLog
}

type cloudflaredProcessLog struct {
	mu   sync.Mutex
	data strings.Builder
}

func (l *cloudflaredProcessLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.data.Write(p)
}
func (l *cloudflaredProcessLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.data.String()
}

func startCloudflaredRaceChild(t *testing.T, schema, apps, configPath, restarts, bin string) *cloudflaredRaceProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCloudflaredTwoAPIProcessRacePostgres$")
	cmd.Env = append(os.Environ(), "NORN_CLOUDFLARED_RACE_CHILD=1", "NORN_CLOUDFLARED_RACE_SCHEMA="+schema, "NORN_CLOUDFLARED_RACE_APPS="+apps, "NORN_CLOUDFLARED_CONFIG="+configPath, "NORN_CLOUDFLARED_RESTART_LOG="+restarts, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &cloudflaredProcessLog{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan string, 1)
	go func() {
		line, e := bufio.NewReader(stdout).ReadString('\n')
		if e != nil {
			ready <- ""
			return
		}
		ready <- strings.TrimSpace(line)
	}()
	select {
	case address := <-ready:
		if !strings.HasPrefix(address, "http://127.0.0.1:") {
			t.Fatalf("child readiness=%q stderr=%s", address, stderr.String())
		}
		return &cloudflaredRaceProcess{url: address, cmd: cmd, stdin: stdin, stderr: stderr}
	case <-time.After(10 * time.Second):
		t.Fatalf("child startup timeout stderr=%s", stderr.String())
	}
	return nil
}

func runCloudflaredRaceChild(t *testing.T) {
	db, err := openCronTriggerCrashDB(os.Getenv("NORN_TEST_DATABASE_URL"), os.Getenv("NORN_CLOUDFLARED_RACE_SCHEMA"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cloudflared.SetConfigPath(os.Getenv("NORN_CLOUDFLARED_CONFIG"))
	p := &pipeline.Pipeline{DB: db}
	p.CloudflaredEffects, err = pipeline.NewCloudflaredEffects(db)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, nil, nil, nil, &config.Config{AppsDir: os.Getenv("NORN_CLOUDFLARED_RACE_APPS"), AuditSigningKey: cloudflaredProcessAuditKey}, p, nil, nil, nil, nil, nil)
	p.SetOperationStore(h.OperationStore())
	router := chi.NewRouter()
	router.Post("/api/apps/{id}/teardown", func(w http.ResponseWriter, r *http.Request) {
		r = WithAccessPrincipal(r, &AccessPrincipal{Subject: "operator", TokenID: "token-1", DeviceID: "device-1", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
		h.MutationAuditMiddleware(http.HandlerFunc(h.Teardown)).ServeHTTP(w, r)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.NewOperationWorkerForKinds(db, p, []string{"app.cloudflared-mutate"}).Run(ctx)
	fmt.Fprintln(os.Stdout, server.URL)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
