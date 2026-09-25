package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

// TestEtcdFleetGitHubDispatchProcess is an opt-in normal-router qualification.
// It starts norn-api with a disposable etcd prefix and drives the public
// dispatch endpoint against a local GitHub App API emulator. The emulator
// commits one dispatch then drops its HTTP response, proving that the router
// preserves the server-generated nonce, recovers the reviewed run, and keeps
// the destructive intent immutable across retries.
func TestEtcdFleetGitHubDispatchProcess(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}

	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/norn-test/fleet-github-dispatch-process/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()); _ = client.Close() })

	const secret = "fleet-github-dispatch-process-managed-token-000"
	const auditKey = "fleet-github-dispatch-process-audit-signing-key-000"
	authority := uuid.NewString()
	bootstrap := filepath.Join(t.TempDir(), "operator-token")
	if err := bootstrapEtcdManagedCredential(context.Background(), client, prefix, secret, etcdBootstrapRequest{Output: bootstrap, Subject: "operator", Scopes: []string{handler.ScopeAPIRead, handler.ScopeAPIWrite}, TTL: time.Hour}, publishBootstrapTokenFile); err != nil {
		t.Fatal(err)
	}
	tokenBytes, err := os.ReadFile(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	operatorToken := strings.TrimSpace(string(tokenBytes))

	github := newProcessFleetGitHub(t)
	defer github.Close()
	keyPath := writeProcessFleetGitHubKey(t)
	configPath := filepath.Join(t.TempDir(), "cluster.yaml")
	document := "apiVersion: norn.dev/fleet/v1\nkind: Cluster\nmetadata:\n  repository: acme/norn-fleet\n  environment: staging\n  workflowURL: https://github.com/acme/norn-fleet/actions\ncluster:\n  name: staging-nyc3\n  provider: digitalocean\n  region: nyc3\nnodePools:\n  control:\n    size: s-2vcpu-4gb\n    min: 2\n    desired: 3\n    max: 5\n    replacement:\n      strategy: blueGreen\n      requireCapacityHeadroom: true\n      requireReadiness: true\n      drainTimeout: 15m\n"
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	binary := filepath.Join(t.TempDir(), "norn-api")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build norn-api: %v\n%s", err, output)
	}
	command := exec.Command(binary)
	var output lockedBuffer
	command.Stdout, command.Stderr = &output, &output
	command.Env = append(withoutEnvironment(os.Environ(), "NORN_DATABASE_URL"),
		"NORN_CONTROL_BACKEND=etcd", "NORN_ETCD_ENDPOINTS="+endpoints, "NORN_ETCD_PREFIX="+prefix, "NORN_CONTROL_AUTHORITY="+authority,
		"NORN_PROFILE=development", "NORN_ENVIRONMENT=staging", "NORN_QUALIFICATION_SIGNING_KEY="+testEd25519Private('g'), "NORN_API_TOKEN="+secret, "NORN_AUDIT_SIGNING_KEY="+auditKey, "NORN_AUDIT_RETENTION_DAYS=365", "NORN_REQUIRE_EXPLICIT_AUTH=true",
		"NORN_NOMAD_ADDR=https://nomad.example.test:4646", "NORN_CONSUL_ADDR=https://consul.example.test:8501", "NORN_REGISTRY_URL=registry.example.test/norn", "NORN_LEGACY_TOKEN_SIGNING_UNTIL=2020-01-01T00:00:00Z",
		"NORN_BIND_ADDR=127.0.0.1", fmt.Sprintf("NORN_PORT=%d", port), "NORN_FLEET_CONFIG="+configPath, "NORN_UI_DIR=",
		"NORN_FLEET_GITHUB_APP_ID=1234", "NORN_FLEET_GITHUB_INSTALLATION_ID=1", "NORN_FLEET_GITHUB_PRIVATE_KEY_FILE="+keyPath, "NORN_FLEET_GITHUB_REPOSITORY=acme/norn-fleet", "NORN_FLEET_GITHUB_CONFIG_PATH=environments/staging/nyc3/cluster.yaml", "NORN_FLEET_GITHUB_API_BASE_URL="+github.URL,
		"NORN_GITHUB_ACTIONS_OIDC_AUDIENCE=norn-fleet-staging", "NORN_GITHUB_ACTIONS_ALLOWED_REPOSITORIES=acme/norn-fleet@101@202", "NORN_GITHUB_ACTIONS_ALLOWED_WORKFLOW_REFS=acme/norn-fleet/.github/workflows/apply.yml@"+strings.Repeat("a", 40), "NORN_GITHUB_ACTIONS_ALLOWED_REFS=refs/heads/main", "NORN_GITHUB_ACTIONS_ALLOWED_EVENTS=workflow_dispatch", "NORN_GITHUB_ACTIONS_ALLOWED_APPS=fleet", "NORN_GITHUB_ACTIONS_ALLOWED_ENVIRONMENTS=staging", "NORN_GITHUB_ACTIONS_DEFAULT_BRANCH=main",
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Signal(os.Interrupt); _ = command.Wait() })
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForSourceHealth(t, base, &output)

	planResponse := processJSONRequest(t, http.MethodPost, base+"/api/v1/fleet/node-pools/control/plan", operatorToken, "signed-contraction", map[string]interface{}{"desired": 2, "reason": "process protected dispatch qualification"})
	if planResponse.StatusCode != http.StatusCreated || !bytes.Contains(planResponse.Body, []byte(`"signature"`)) || !bytes.Contains(planResponse.Body, []byte(`"digest"`)) {
		t.Fatalf("signed plan status=%d body=%s logs=%s", planResponse.StatusCode, planResponse.Body, output.String())
	}
	var plan struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(planResponse.Body, &plan); err != nil || plan.ID == "" {
		t.Fatalf("plan response=%s err=%v", planResponse.Body, err)
	}
	dispatchURL := base + "/api/v1/fleet/plans/" + plan.ID + "/github/dispatch"

	refused := processJSONRequest(t, http.MethodPost, dispatchURL, operatorToken, "", map[string]bool{"allowDestructive": false})
	if refused.StatusCode != http.StatusConflict || !bytes.Contains(refused.Body, []byte("fleet_github_destructive_ack_required")) || github.Dispatches() != 0 {
		t.Fatalf("unacknowledged contraction status=%d body=%s dispatches=%d", refused.StatusCode, refused.Body, github.Dispatches())
	}
	first := processJSONRequest(t, http.MethodPost, dispatchURL, operatorToken, "", map[string]bool{"allowDestructive": true})
	if first.StatusCode != http.StatusBadGateway || !bytes.Contains(first.Body, []byte("fleet_github_dispatch_unproven")) || github.Dispatches() != 1 {
		t.Fatalf("lost response status=%d body=%s dispatches=%d", first.StatusCode, first.Body, github.Dispatches())
	}
	if nonce := github.Nonce(); nonce == "" || bytes.Contains(first.Body, []byte(nonce)) {
		t.Fatalf("first dispatch leaked or omitted nonce: body=%s nonce=%q", first.Body, nonce)
	}
	changed := processJSONRequest(t, http.MethodPost, dispatchURL, operatorToken, "", map[string]bool{"allowDestructive": false})
	if changed.StatusCode != http.StatusConflict || !bytes.Contains(changed.Body, []byte("fleet_github_destructive_ack_required")) || github.Dispatches() != 1 {
		t.Fatalf("changed intent status=%d body=%s dispatches=%d", changed.StatusCode, changed.Body, github.Dispatches())
	}
	recovered := processJSONRequest(t, http.MethodPost, dispatchURL, operatorToken, "", map[string]bool{"allowDestructive": true})
	if recovered.StatusCode != http.StatusCreated || bytes.Contains(recovered.Body, []byte(github.Nonce())) || github.Dispatches() != 1 {
		t.Fatalf("recovery status=%d body=%s dispatches=%d", recovered.StatusCode, recovered.Body, github.Dispatches())
	}
	var dispatched struct {
		RunID            int64  `json:"runId"`
		PlanSHA256       string `json:"planSha256"`
		ApprovedHeadSHA  string `json:"approvedHeadSha"`
		AllowDestructive bool   `json:"allowDestructive"`
	}
	if err := json.Unmarshal(recovered.Body, &dispatched); err != nil || dispatched.RunID != 93 || dispatched.PlanSHA256 != github.planSHA || dispatched.ApprovedHeadSHA != github.headSHA || !dispatched.AllowDestructive {
		t.Fatalf("recovered dispatch=%s err=%v", recovered.Body, err)
	}
	replay := processJSONRequest(t, http.MethodPost, dispatchURL, operatorToken, "", map[string]bool{"allowDestructive": true})
	if replay.StatusCode != http.StatusOK || github.Dispatches() != 1 || bytes.Contains(replay.Body, []byte(github.Nonce())) {
		t.Fatalf("bound replay status=%d body=%s dispatches=%d", replay.StatusCode, replay.Body, github.Dispatches())
	}

	signer, err := store.NewHMACAcceptanceSigner(auditKey)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := operations.GetFleetRunnerDispatchBinding(context.Background(), plan.ID)
	if err != nil || bound.RunID != 93 || bound.PlanSHA256 != github.planSHA || bound.ApprovedHeadSHA != github.headSHA {
		t.Fatalf("durable dispatch binding=%+v err=%v", bound, err)
	}
}

type processFleetGitHub struct {
	*httptest.Server
	mu         sync.Mutex
	dispatches int
	nonce      string
	planID     string
	planSHA    string
	headSHA    string
}

func newProcessFleetGitHub(t *testing.T) *processFleetGitHub {
	t.Helper()
	fake := &processFleetGitHub{planSHA: strings.Repeat("a", 64), headSHA: strings.Repeat("b", 40)}
	fake.Server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	return fake
}

func (f *processFleetGitHub) Dispatches() int { f.mu.Lock(); defer f.mu.Unlock(); return f.dispatches }
func (f *processFleetGitHub) Nonce() string   { f.mu.Lock(); defer f.mu.Unlock(); return f.nonce }

func (f *processFleetGitHub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	write := func(value interface{}) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/1/access_tokens":
		write(map[string]string{"token": "installation-token", "expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
	case r.Method == http.MethodGet && r.URL.Path == "/app":
		write(map[string]string{"slug": "norn"})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/norn-fleet/pulls":
		head := strings.TrimPrefix(r.URL.Query().Get("head"), "acme:")
		write([]map[string]interface{}{{"number": 42, "html_url": "https://github.com/acme/norn-fleet/pull/42", "state": "closed", "merged_at": "2026-09-25T00:00:00Z", "head": map[string]string{"ref": head, "sha": f.headSHA}, "base": map[string]string{"ref": "main"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/norn-fleet/pulls/42":
		write(map[string]string{"merge_commit_sha": f.headSHA, "merged_at": "2026-09-25T00:00:00Z"})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/norn-fleet/actions/workflows/plan.yml/runs":
		write(map[string]interface{}{"workflow_runs": []map[string]interface{}{{"id": 91, "head_sha": f.headSHA, "status": "completed", "conclusion": "success"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/norn-fleet/actions/runs/91/artifacts":
		write(map[string]interface{}{"artifacts": []map[string]interface{}{{"id": 8, "name": "fleet-plan-staging-nyc3-91", "expired": false}}})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/norn-fleet/actions/artifacts/8/zip":
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(processFleetGitHubArtifact(f.planSHA))
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/norn-fleet/actions/workflows/apply.yml/runs":
		f.mu.Lock()
		dispatched := f.dispatches > 0
		f.mu.Unlock()
		runs := []map[string]int64{}
		if dispatched {
			runs = append(runs, map[string]int64{"id": 93})
		}
		write(map[string]interface{}{"workflow_runs": runs})
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/norn-fleet/actions/workflows/apply.yml/dispatches":
		var request struct {
			Inputs map[string]string `json:"inputs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.dispatches++
		f.nonce = request.Inputs["dispatch_nonce"]
		f.planID = request.Inputs["norn_plan_id"]
		first := f.dispatches == 1
		f.mu.Unlock()
		if !first {
			http.Error(w, "duplicate dispatch", http.StatusConflict)
			return
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/norn-fleet/actions/runs/93":
		f.mu.Lock()
		nonce, planID := f.nonce, f.planID
		f.mu.Unlock()
		write(map[string]interface{}{"id": 93, "html_url": "https://github.com/acme/norn-fleet/actions/runs/93", "event": "workflow_dispatch", "head_sha": f.headSHA, "head_branch": "main", "path": ".github/workflows/apply.yml", "name": "apply", "display_title": "Apply staging/nyc3 Norn plan " + planID + " nonce " + nonce, "inputs": map[string]string{"fleet_environment": "staging/nyc3", "plan_run_id": "91", "plan_sha256": f.planSHA, "norn_plan_id": planID, "allow_destructive": "true", "dispatch_nonce": nonce}, "actor": map[string]string{"login": "norn[bot]", "type": "Bot"}})
	default:
		http.NotFound(w, r)
	}
}

func processFleetGitHubArtifact(planSHA string) []byte {
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	file, _ := writer.Create("fleet-plan.sha256")
	_, _ = file.Write([]byte(planSHA + "\n"))
	_ = writer.Close()
	return output.Bytes()
}

func writeProcessFleetGitHubKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	path := filepath.Join(t.TempDir(), "fleet-github-app.pem")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
