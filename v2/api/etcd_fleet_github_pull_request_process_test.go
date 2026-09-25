package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
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
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// TestEtcdFleetGitHubPullRequestProcess starts the normal etcd router with a
// deliberately poisoned PostgreSQL DSN. The fake GitHub App accepts exactly
// one pull-request write, drops that response, then proves the next API call
// reconciles the deterministic branch without another mutation. The returned
// operation is the shape decoded by the Fleet CLI client.
func TestEtcdFleetGitHubPullRequestProcess(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/norn-test/fleet-github-pr-process/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()); _ = client.Close() })

	const secret = "fleet-github-pr-process-managed-token-000"
	const auditKey = "fleet-github-pr-process-audit-signing-key-000"
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

	document := []byte("apiVersion: norn.dev/fleet/v1\nkind: Cluster\nmetadata:\n  repository: acme/norn-fleet\n  environment: staging\n  workflowURL: https://github.com/acme/norn-fleet/actions\ncluster:\n  name: staging-nyc3\n  provider: digitalocean\n  region: nyc3\nnodePools:\n  control:\n    size: s-2vcpu-4gb\n    min: 2\n    desired: 3\n    max: 5\n    replacement:\n      strategy: blueGreen\n      requireCapacityHeadroom: true\n      requireReadiness: true\n      drainTimeout: 15m\n")
	github := newProcessFleetGitHubPullRequest(t, document)
	defer github.Close()
	configPath := filepath.Join(t.TempDir(), "cluster.yaml")
	if err := os.WriteFile(configPath, document, 0o600); err != nil {
		t.Fatal(err)
	}
	keyPath := writeProcessFleetGitHubPullRequestKey(t)

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
		"NORN_DATABASE_URL=postgres://poisoned.invalid:1/never-open",
		"NORN_CONTROL_BACKEND=etcd", "NORN_ETCD_ENDPOINTS="+endpoints, "NORN_ETCD_PREFIX="+prefix, "NORN_CONTROL_AUTHORITY="+authority,
		"NORN_PROFILE=development", "NORN_ENVIRONMENT=staging", "NORN_QUALIFICATION_SIGNING_KEY="+testEd25519Private('p'), "NORN_API_TOKEN="+secret, "NORN_AUDIT_SIGNING_KEY="+auditKey, "NORN_AUDIT_RETENTION_DAYS=365", "NORN_REQUIRE_EXPLICIT_AUTH=true",
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

	planResponse := processJSONRequest(t, http.MethodPost, base+"/api/v1/fleet/node-pools/control/plan", operatorToken, "pr-process-plan", map[string]interface{}{"desired": 4, "reason": "process GitHub PR qualification"})
	if planResponse.StatusCode != http.StatusCreated {
		t.Fatalf("plan status=%d body=%s logs=%s", planResponse.StatusCode, planResponse.Body, output.String())
	}
	var plan struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(planResponse.Body, &plan); err != nil || plan.ID == "" {
		t.Fatalf("plan=%s err=%v", planResponse.Body, err)
	}
	url := base + "/api/v1/fleet/plans/" + plan.ID + "/github/pull-request"
	first := processJSONRequest(t, http.MethodPost, url, operatorToken, "", nil)
	if first.StatusCode != http.StatusBadGateway || !bytes.Contains(first.Body, []byte("fleet_github_pull_request_unproven")) || github.FleetWrites() != 3 {
		t.Fatalf("lost PR response status=%d body=%s fleet writes=%d logs=%s", first.StatusCode, first.Body, github.FleetWrites(), output.String())
	}
	recovered := processJSONRequest(t, http.MethodPost, url, operatorToken, "", nil)
	if recovered.StatusCode != http.StatusOK || github.FleetWrites() != 3 {
		t.Fatalf("recovery status=%d body=%s fleet writes=%d", recovered.StatusCode, recovered.Body, github.FleetWrites())
	}
	var operation model.Operation
	if err := json.Unmarshal(recovered.Body, &operation); err != nil || operation.ID == "" || operation.Kind != "fleet.github.pull-request" || operation.Status != model.OperationSucceeded || operation.Payload["url"] != "https://github.com/acme/norn-fleet/pull/42" || operation.Payload["pullRequestNumber"] != float64(42) {
		t.Fatalf("CLI operation response=%s err=%v", recovered.Body, err)
	}
	replay := processJSONRequest(t, http.MethodPost, url, operatorToken, "", nil)
	if replay.StatusCode != http.StatusOK || github.FleetWrites() != 3 {
		t.Fatalf("replay status=%d body=%s fleet writes=%d", replay.StatusCode, replay.Body, github.FleetWrites())
	}
}

// TestEtcdFleetGitHubPullRequestTwoAPIsProcess puts two independently built
// normal etcd API processes behind the same signed reservation.  The fake
// GitHub API holds both pre-write reconciliations until each process has
// proved no write exists, which is the narrow window where an unfenced router
// could ask GitHub to mutate twice.  GitHub's deterministic branch is the
// final external idempotency boundary; the processes must subsequently retain
// one signed completion and replay it without another mutation.
func TestEtcdFleetGitHubPullRequestTwoAPIsProcess(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/norn-test/fleet-github-pr-two-api-process/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()); _ = client.Close() })

	const secret = "fleet-github-pr-two-api-managed-token-000"
	const auditKey = "fleet-github-pr-two-api-audit-signing-key-000"
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

	document := []byte("apiVersion: norn.dev/fleet/v1\nkind: Cluster\nmetadata:\n  repository: acme/norn-fleet\n  environment: staging\n  workflowURL: https://github.com/acme/norn-fleet/actions\ncluster:\n  name: staging-nyc3\n  provider: digitalocean\n  region: nyc3\nnodePools:\n  control:\n    size: s-2vcpu-4gb\n    min: 2\n    desired: 3\n    max: 5\n    replacement:\n      strategy: blueGreen\n      requireCapacityHeadroom: true\n      requireReadiness: true\n      drainTimeout: 15m\n")
	github := newProcessFleetGitHubPullRequest(t, document)
	github.dropPullResponse = false
	github.ArmNoWriteRace(2)
	defer github.Close()
	configPath := filepath.Join(t.TempDir(), "cluster.yaml")
	if err := os.WriteFile(configPath, document, 0o600); err != nil {
		t.Fatal(err)
	}
	keyPath := writeProcessFleetGitHubPullRequestKey(t)
	binary := filepath.Join(t.TempDir(), "norn-api")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build norn-api: %v\n%s", err, output)
	}

	start := func() (string, *exec.Cmd, *lockedBuffer) {
		listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
		if listenErr != nil {
			t.Fatal(listenErr)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		command := exec.Command(binary)
		var output lockedBuffer
		command.Stdout, command.Stderr = &output, &output
		command.Env = append(withoutEnvironment(os.Environ(), "NORN_DATABASE_URL"),
			"NORN_DATABASE_URL=postgres://poisoned.invalid:1/never-open",
			"NORN_CONTROL_BACKEND=etcd", "NORN_ETCD_ENDPOINTS="+endpoints, "NORN_ETCD_PREFIX="+prefix, "NORN_CONTROL_AUTHORITY="+authority,
			"NORN_PROFILE=development", "NORN_ENVIRONMENT=staging", "NORN_QUALIFICATION_SIGNING_KEY="+testEd25519Private('q'), "NORN_API_TOKEN="+secret, "NORN_AUDIT_SIGNING_KEY="+auditKey, "NORN_AUDIT_RETENTION_DAYS=365", "NORN_REQUIRE_EXPLICIT_AUTH=true",
			"NORN_NOMAD_ADDR=https://nomad.example.test:4646", "NORN_CONSUL_ADDR=https://consul.example.test:8501", "NORN_REGISTRY_URL=registry.example.test/norn", "NORN_LEGACY_TOKEN_SIGNING_UNTIL=2020-01-01T00:00:00Z",
			"NORN_BIND_ADDR=127.0.0.1", fmt.Sprintf("NORN_PORT=%d", port), "NORN_FLEET_CONFIG="+configPath, "NORN_UI_DIR=",
			"NORN_FLEET_GITHUB_APP_ID=1234", "NORN_FLEET_GITHUB_INSTALLATION_ID=1", "NORN_FLEET_GITHUB_PRIVATE_KEY_FILE="+keyPath, "NORN_FLEET_GITHUB_REPOSITORY=acme/norn-fleet", "NORN_FLEET_GITHUB_CONFIG_PATH=environments/staging/nyc3/cluster.yaml", "NORN_FLEET_GITHUB_API_BASE_URL="+github.URL,
			"NORN_GITHUB_ACTIONS_OIDC_AUDIENCE=norn-fleet-staging", "NORN_GITHUB_ACTIONS_ALLOWED_REPOSITORIES=acme/norn-fleet@101@202", "NORN_GITHUB_ACTIONS_ALLOWED_WORKFLOW_REFS=acme/norn-fleet/.github/workflows/apply.yml@"+strings.Repeat("a", 40), "NORN_GITHUB_ACTIONS_ALLOWED_REFS=refs/heads/main", "NORN_GITHUB_ACTIONS_ALLOWED_EVENTS=workflow_dispatch", "NORN_GITHUB_ACTIONS_ALLOWED_APPS=fleet", "NORN_GITHUB_ACTIONS_ALLOWED_ENVIRONMENTS=staging", "NORN_GITHUB_ACTIONS_DEFAULT_BRANCH=main",
		)
		if startErr := command.Start(); startErr != nil {
			t.Fatal(startErr)
		}
		t.Cleanup(func() { _ = command.Process.Signal(os.Interrupt); _ = command.Wait() })
		base := fmt.Sprintf("http://127.0.0.1:%d", port)
		waitForSourceHealth(t, base, &output)
		return base, command, &output
	}
	firstBase, _, firstOutput := start()
	secondBase, _, secondOutput := start()

	planResponse := processJSONRequest(t, http.MethodPost, firstBase+"/api/v1/fleet/node-pools/control/plan", operatorToken, "pr-two-api-plan", map[string]interface{}{"desired": 4, "reason": "two API GitHub PR race qualification"})
	if planResponse.StatusCode != http.StatusCreated {
		t.Fatalf("plan status=%d body=%s logs=%s", planResponse.StatusCode, planResponse.Body, firstOutput.String())
	}
	var plan struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(planResponse.Body, &plan); err != nil || plan.ID == "" {
		t.Fatalf("plan=%s err=%v", planResponse.Body, err)
	}
	path := "/api/v1/fleet/plans/" + plan.ID + "/github/pull-request"
	type response struct {
		status int
		body   []byte
	}
	responses := make(chan response, 2)
	for _, base := range []string{firstBase, secondBase} {
		go func(base string) {
			reply := processJSONRequest(t, http.MethodPost, base+path, operatorToken, "", nil)
			responses <- response{status: reply.StatusCode, body: reply.Body}
		}(base)
	}
	first, second := <-responses, <-responses
	statuses := map[int]int{}
	statuses[first.status]++
	statuses[second.status]++
	if statuses[http.StatusCreated]+statuses[http.StatusOK]+statuses[http.StatusBadGateway] != 2 || statuses[http.StatusCreated] < 1 {
		branchWrites, fileWrites, pullWrites := github.MutationCounts()
		t.Fatalf("race statuses=%d,%d bodies=%s / %s mutations=%d/%d/%d logs=%s / %s", first.status, second.status, first.body, second.body, branchWrites, fileWrites, pullWrites, firstOutput.String(), secondOutput.String())
	}
	branchWrites, fileWrites, pullWrites := github.MutationCounts()
	if branchWrites != 1 || fileWrites != 1 || pullWrites != 1 || github.FleetWrites() != 3 {
		t.Fatalf("race mutated GitHub more than once: branch=%d file=%d pull=%d total=%d", branchWrites, fileWrites, pullWrites, github.FleetWrites())
	}
	replay := processJSONRequest(t, http.MethodPost, firstBase+path, operatorToken, "", nil)
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("replay status=%d body=%s logs=%s", replay.StatusCode, replay.Body, firstOutput.String())
	}
	var operation model.Operation
	if err := json.Unmarshal(replay.Body, &operation); err != nil || operation.ID == "" || operation.Kind != "fleet.github.pull-request" || operation.Status != model.OperationSucceeded || operation.Payload["url"] != "https://github.com/acme/norn-fleet/pull/42" {
		t.Fatalf("replay operation=%s err=%v", replay.Body, err)
	}
	signer, err := store.NewHMACAcceptanceSigner(auditKey)
	if err != nil {
		t.Fatal(err)
	}
	verificationStore, err := etcdstore.NewV3OperationStoreWithPolicy(client, prefix, authority, signer, store.AcceptancePolicy{ReplayTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := verificationStore.GetFleetGitHubPullRequestReservation(context.Background(), plan.ID)
	if err != nil || reservation.OperationID != operation.ID {
		t.Fatalf("durable reservation=%+v err=%v operation=%s", reservation, err, operation.ID)
	}
	if err := verificationStore.VerifyFleetGitHubPullRequestReservation(context.Background(), reservation); err != nil {
		t.Fatalf("signed reservation verification: %v", err)
	}
	if err := verificationStore.VerifyFleetGitHubPullRequestCompletion(context.Background(), reservation, pullRequestFromEtcdOperation(&operation)); err != nil {
		t.Fatalf("signed completion verification: %v", err)
	}
	if branchWrites, fileWrites, pullWrites = github.MutationCounts(); branchWrites != 1 || fileWrites != 1 || pullWrites != 1 {
		t.Fatalf("replay mutated GitHub: branch=%d file=%d pull=%d", branchWrites, fileWrites, pullWrites)
	}
}

type processFleetGitHubPullRequest struct {
	*httptest.Server
	mu               sync.Mutex
	main             []byte
	branch           []byte
	branchName       string
	prCreated        bool
	fleetWrites      int
	branchWrites     int
	fileWrites       int
	pullWrites       int
	dropPullResponse bool
	raceRemain       int
	raceRelease      chan struct{}
	title, body      string
}

func newProcessFleetGitHubPullRequest(t *testing.T, main []byte) *processFleetGitHubPullRequest {
	t.Helper()
	fake := &processFleetGitHubPullRequest{main: append([]byte(nil), main...), dropPullResponse: true}
	fake.Server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	return fake
}

// FleetWrites excludes GitHub App token exchange and counts the protected
// mutation sequence: branch creation, fleet configuration update, and PR.
func (f *processFleetGitHubPullRequest) FleetWrites() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fleetWrites
}

// ArmNoWriteRace holds exactly count initial pull-request listing calls until
// all have arrived. Those calls are the router's verified-no-write
// reconciliation step, before either API process is allowed to begin GitHub
// mutation.
func (f *processFleetGitHubPullRequest) ArmNoWriteRace(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raceRemain = count
	f.raceRelease = make(chan struct{})
}

func (f *processFleetGitHubPullRequest) awaitNoWriteRace() {
	f.mu.Lock()
	if f.raceRemain <= 0 || f.raceRelease == nil {
		f.mu.Unlock()
		return
	}
	f.raceRemain--
	release := f.raceRelease
	if f.raceRemain == 0 {
		close(release)
	}
	f.mu.Unlock()
	<-release
}

func (f *processFleetGitHubPullRequest) MutationCounts() (branch, file, pull int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.branchWrites, f.fileWrites, f.pullWrites
}

func (f *processFleetGitHubPullRequest) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/repos/acme/norn-fleet/pulls" {
		f.awaitNoWriteRace()
	}
	write := func(value interface{}) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	content := func(raw []byte) {
		write(map[string]interface{}{"content": base64.StdEncoding.EncodeToString(raw), "encoding": "base64", "sha": "content-sha", "size": len(raw)})
	}
	const mainSHA = "1111111111111111111111111111111111111111"
	branchSHA := "2222222222222222222222222222222222222222"
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/1/access_tokens":
		write(map[string]string{"token": "installation-token", "expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/norn-fleet/contents/environments/staging/nyc3/cluster.yaml":
		if r.URL.Query().Get("ref") == "main" {
			content(f.main)
			return
		}
		if r.URL.Query().Get("ref") == f.branchName && len(f.branch) > 0 {
			content(f.branch)
			return
		}
		http.NotFound(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/norn-fleet/git/ref/heads/main":
		write(map[string]interface{}{"object": map[string]string{"sha": mainSHA}})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/acme/norn-fleet/git/ref/heads/norn/plan-"):
		if f.branchName == "" {
			http.NotFound(w, r)
			return
		}
		write(map[string]interface{}{"object": map[string]string{"sha": branchSHA}})
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/norn-fleet/git/refs":
		var value struct {
			Ref string `json:"ref"`
		}
		_ = json.NewDecoder(r.Body).Decode(&value)
		branch := strings.TrimPrefix(value.Ref, "refs/heads/")
		if f.branchName != "" {
			http.Error(w, "reference already exists", http.StatusUnprocessableEntity)
			return
		}
		f.branchName = branch
		f.fleetWrites++
		f.branchWrites++
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut && r.URL.Path == "/repos/acme/norn-fleet/contents/environments/staging/nyc3/cluster.yaml":
		var value struct {
			Branch  string `json:"branch"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(value.Content)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.branchName, f.branch = value.Branch, decoded
		f.fleetWrites++
		f.fileWrites++
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/norn-fleet/pulls":
		if !f.prCreated {
			write([]interface{}{})
			return
		}
		write([]map[string]interface{}{{"number": 42, "html_url": "https://github.com/acme/norn-fleet/pull/42", "state": "open", "head": map[string]string{"ref": f.branchName, "sha": branchSHA}, "base": map[string]string{"ref": "main"}, "title": f.title, "body": f.body}})
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/norn-fleet/pulls":
		var value struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if f.prCreated {
			http.Error(w, "duplicate pull request", http.StatusConflict)
			return
		}
		f.prCreated, f.fleetWrites, f.title, f.body = true, f.fleetWrites+1, value.Title, value.Body
		f.pullWrites++
		if !f.dropPullResponse {
			write(map[string]interface{}{"number": 42, "html_url": "https://github.com/acme/norn-fleet/pull/42", "state": "open"})
			return
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	default:
		http.NotFound(w, r)
	}
}

func writeProcessFleetGitHubPullRequestKey(t *testing.T) string {
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
