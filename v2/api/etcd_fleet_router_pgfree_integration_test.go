package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/githubapp"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

// TestEtcdFleetRouterPGFreeGitHubConfiguredProcess starts the ordinary binary,
// rather than calling a handler directly. It proves the GitHub-configured
// normal router remains PostgreSQL-free for both absent and poisoned DSNs.
func TestEtcdFleetRouterPGFreeGitHubConfiguredProcess(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	for _, databaseURL := range []string{"", "postgres://poisoned.invalid:1/never-open"} {
		t.Run(map[bool]string{true: "poisoned PostgreSQL DSN", false: "absent PostgreSQL DSN"}[databaseURL != ""], func(t *testing.T) {
			testEtcdFleetRouterPGFreeGitHubConfiguredProcess(t, endpoints, databaseURL)
		})
	}
}

func testEtcdFleetRouterPGFreeGitHubConfiguredProcess(t *testing.T, endpoints, databaseURL string) {
	t.Helper()
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/norn-test/fleet-router-pgfree/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()); _ = client.Close() })
	const secret = "fleet-router-pgfree-managed-token-secret-000"
	const auditKey = "fleet-router-pgfree-audit-signing-key-000"
	authority := uuid.NewString()
	bootstrap := filepath.Join(t.TempDir(), "initial-token")
	if err := bootstrapEtcdManagedCredential(context.Background(), client, prefix, secret, etcdBootstrapRequest{Output: bootstrap, Subject: "operator", Scopes: []string{handler.ScopeAPIRead, handler.ScopeAPIWrite}, TTL: time.Hour}, publishBootstrapTokenFile); err != nil {
		t.Fatal(err)
	}
	operatorBytes, err := os.ReadFile(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	operatorToken := strings.TrimSpace(string(operatorBytes))
	identities := etcdstore.NewAuthStore(client, prefix)
	ci := &handler.CIIdentity{Provider: "github-actions", Repository: "acme/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202", RunID: "7", RunAttempt: "1", WorkflowRef: "acme/norn-fleet/.github/workflows/apply.yml@" + strings.Repeat("a", 40), WorkflowSHA: strings.Repeat("a", 40), Ref: "refs/heads/main", RefType: "branch", EventName: "workflow_dispatch", Environment: "staging", SHA: strings.Repeat("c", 40), RefProtected: true, Intent: "apply"}
	runnerToken, err := signedProcessManagedCIToken(secret, identities, ci)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "cluster.yaml")
	if err := os.WriteFile(configPath, []byte("apiVersion: norn.dev/fleet/v1\nkind: Cluster\nmetadata:\n  repository: acme/norn-fleet\n  workflowURL: https://github.com/acme/norn-fleet/actions\ncluster:\n  name: test\n  provider: digitalocean\n  region: nyc3\nnodePools:\n  control:\n    size: s-2vcpu-4gb\n    min: 3\n    desired: 3\n    max: 5\n    replacement:\n      strategy: blueGreen\n      requireCapacityHeadroom: true\n      requireReadiness: true\n      drainTimeout: 15m\n"), 0o600); err != nil {
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
		t.Fatalf("build: %v\n%s", err, output)
	}
	command := exec.Command(binary)
	var output lockedBuffer
	command.Stdout, command.Stderr = &output, &output
	environment := withoutEnvironment(os.Environ(), "NORN_DATABASE_URL")
	command.Env = append(environment,
		"NORN_CONTROL_BACKEND=etcd", "NORN_ETCD_ENDPOINTS="+endpoints, "NORN_ETCD_PREFIX="+prefix, "NORN_CONTROL_AUTHORITY="+authority,
		"NORN_PROFILE=development", "NORN_ENVIRONMENT=staging", "NORN_QUALIFICATION_SIGNING_KEY="+testEd25519Private('r'), "NORN_ALLOW_DEVELOPMENT_GITHUB_ACTIONS_OIDC=true", "NORN_API_TOKEN="+secret, "NORN_AUDIT_SIGNING_KEY="+auditKey, "NORN_AUDIT_RETENTION_DAYS=365", "NORN_REQUIRE_EXPLICIT_AUTH=true",
		"NORN_NOMAD_ADDR=https://nomad.example.test:4646", "NORN_CONSUL_ADDR=https://consul.example.test:8501", "NORN_REGISTRY_URL=registry.example.test/norn", "NORN_LEGACY_TOKEN_SIGNING_UNTIL=2020-01-01T00:00:00Z",
		"NORN_BIND_ADDR=127.0.0.1", fmt.Sprintf("NORN_PORT=%d", port), "NORN_FLEET_CONFIG="+configPath, "NORN_UI_DIR=",
		"NORN_FLEET_GITHUB_APP_ID=test-app", "NORN_FLEET_GITHUB_INSTALLATION_ID=1", "NORN_FLEET_GITHUB_PRIVATE_KEY_FILE="+filepath.Join(t.TempDir(), "unused-app-key.pem"), "NORN_FLEET_GITHUB_REPOSITORY=acme/norn-fleet", "NORN_FLEET_GITHUB_CONFIG_PATH=environments/staging/nyc3/cluster.yaml", "NORN_FLEET_GITHUB_API_BASE_URL=http://127.0.0.1:9",
		"NORN_GITHUB_ACTIONS_ALLOWED_REPOSITORIES=acme/norn-fleet@101@202", "NORN_GITHUB_ACTIONS_ALLOWED_WORKFLOW_REFS=acme/norn-fleet/.github/workflows/apply.yml@"+strings.Repeat("a", 40), "NORN_GITHUB_ACTIONS_ALLOWED_REFS=refs/heads/main", "NORN_GITHUB_ACTIONS_ALLOWED_EVENTS=workflow_dispatch", "NORN_GITHUB_ACTIONS_ALLOWED_APPS=fleet", "NORN_GITHUB_ACTIONS_ALLOWED_ENVIRONMENTS=staging", "NORN_GITHUB_ACTIONS_DEFAULT_BRANCH=main",
		"NORN_GITHUB_ACTIONS_OIDC_AUDIENCE=norn-fleet-staging", "NORN_GITHUB_ACTIONS_OIDC_JWKS_URL=https://token.actions.githubusercontent.com/.well-known/jwks", "NORN_GITHUB_ACTIONS_FLEET_ALLOWED_REPOSITORY=acme/norn-fleet@101@202", "NORN_GITHUB_ACTIONS_FLEET_ALLOWED_WORKFLOW_REFS=acme/norn-fleet/.github/workflows/apply.yml@"+strings.Repeat("a", 40), "NORN_GITHUB_ACTIONS_FLEET_ALLOWED_ENVIRONMENTS=staging", "NORN_GITHUB_ACTIONS_FLEET_ALLOWED_INTENTS=apply",
	)
	if databaseURL != "" {
		command.Env = append(command.Env, "NORN_DATABASE_URL="+databaseURL)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Signal(os.Interrupt); _ = command.Wait() })
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForSourceHealth(t, base, &output)
	capabilities := processJSONRequest(t, http.MethodGet, base+"/api/v1/capabilities", "", "", nil)
	if capabilities.StatusCode != http.StatusOK || !bytes.Contains(capabilities.Body, []byte("fleet-runner-attempts-v1")) || !bytes.Contains(capabilities.Body, []byte("fleet-reconciliation-v1")) {
		t.Fatalf("configured GitHub capability response=%d body=%s logs=%s", capabilities.StatusCode, capabilities.Body, output.String())
	}
	if response := processJSONRequest(t, http.MethodPost, base+"/api/v1/auth/github-actions/exchange", "", "", map[string]string{"scope": handler.ScopeFleetOperate, "environment": "staging", "intent": "apply"}); response.StatusCode == http.StatusNotFound {
		t.Fatalf("OIDC route was not registered: %s", response.Body)
	}
	planResponse := processJSONRequest(t, http.MethodPost, base+"/api/v1/fleet/node-pools/control/plan", operatorToken, "router-plan", map[string]interface{}{"desired": 4, "reason": "PG-free router qualification"})
	if planResponse.StatusCode != http.StatusCreated {
		t.Fatalf("plan=%d body=%s logs=%s", planResponse.StatusCode, planResponse.Body, output.String())
	}
	var plan struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(planResponse.Body, &plan); err != nil || plan.ID == "" {
		t.Fatalf("plan response=%s err=%v", planResponse.Body, err)
	}
	signer, err := store.NewHMACAcceptanceSigner(auditKey)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	storedPlan, err := operations.GetOperation(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	encodedPlan, _ := json.Marshal(storedPlan.Payload)
	var typedPlan fleet.CapacityPlan
	if err := json.Unmarshal(encodedPlan, &typedPlan); err != nil {
		t.Fatal(err)
	}
	_, prepared, err := acceptEtcdFleetGitHubDispatch(context.Background(), operations, handler.AccessPrincipal{TokenID: "test", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeAPIWrite}}, storedPlan, typedPlan, &githubapp.Dispatch{PlanRunID: 7, PlanSHA: strings.Repeat("b", 64), ApprovedHeadSHA: ci.SHA}, "staging/nyc3", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.FinishFleetGitHubDispatch(context.Background(), plan.ID, prepared.DispatchNonceSHA256, 7, "https://github.com/acme/norn-fleet/actions/runs/7"); err != nil {
		t.Fatal(err)
	}
	path := base + "/api/v1/fleet/plans/" + plan.ID
	create := processJSONRequest(t, http.MethodPost, path+"/attempts", runnerToken, "attempt", fleet.RunnerAttemptCreateRequest{SchemaVersion: fleet.RunnerAttemptSchemaVersion, RunnerAttemptID: "github-actions:acme/norn-fleet:7:1", CommitSHA: ci.SHA, PlanSHA256: strings.Repeat("b", 64), WorkflowURL: "https://github.com/acme/norn-fleet/actions/runs/7", DispatchNonce: prepared.DispatchNonce, SourceDispatchRunID: "7", HeartbeatTimeoutSeconds: 120})
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("attempt=%d body=%s", create.StatusCode, create.Body)
	}
	var attempt fleet.RunnerAttempt
	if err := json.Unmarshal(create.Body, &attempt); err != nil || attempt.ID == "" {
		t.Fatalf("attempt body=%s err=%v", create.Body, err)
	}
	checkpoint := fleet.ReconciliationRequest{SchemaVersion: fleet.ReconciliationSchemaVersion, Phase: attempt.CurrentPhase, Status: "succeeded", CommitSHA: ci.SHA, PlanSHA256: strings.Repeat("b", 64), EvidenceDigest: "sha256:" + strings.Repeat("d", 64), AttemptID: attempt.ID}
	if response := processJSONRequest(t, http.MethodPost, path+"/reconciliations", runnerToken, "checkpoint", checkpoint); response.StatusCode != http.StatusCreated {
		t.Fatalf("checkpoint=%d body=%s", response.StatusCode, response.Body)
	}
	advance := fleet.RunnerAttemptAdvanceRequest{SchemaVersion: fleet.RunnerAttemptSchemaVersion, ExpectedPhase: attempt.CurrentPhase, Revision: attempt.Revision}
	if response := processJSONRequest(t, http.MethodPost, path+"/attempts/"+attempt.ID+"/advance", runnerToken, "", advance); response.StatusCode != http.StatusOK {
		t.Fatalf("advance=%d body=%s", response.StatusCode, response.Body)
	}
}

func signedProcessManagedCIToken(secret string, identities *etcdstore.AuthStore, ci *handler.CIIdentity) (string, error) {
	now := time.Now().UTC()
	jti := "norn_" + uuid.NewString()
	payload, err := json.Marshal(map[string]interface{}{"sub": "github-actions:" + ci.Repository + ":" + ci.RunID, "iss": "norn", "aud": "norn-control", "use": "access", "managed": true, "iat": now.Unix(), "exp": now.Add(15 * time.Minute).Unix(), "jti": jti, "scp": []string{handler.ScopeFleetOperate}, "environment": ci.Environment, "ci": ci})
	if err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	keyMAC := hmac.New(sha256.New, []byte(secret))
	_, _ = keyMAC.Write([]byte("norn.jwt-signing/v1"))
	signature := hmac.New(sha256.New, keyMAC.Sum(nil))
	_, _ = signature.Write([]byte(unsigned))
	if err := identities.RecordAccessToken(context.Background(), &store.AccessToken{JTI: jti, Subject: "github-actions:" + ci.Repository + ":" + ci.RunID, Scopes: []string{handler.ScopeFleetOperate}, IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute)}); err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil)), nil
}

type processHTTPResponse struct {
	StatusCode int
	Body       []byte
}

func processJSONRequest(t *testing.T, method, url, token, key string, value interface{}) processHTTPResponse {
	t.Helper()
	var body []byte
	if value != nil {
		body, _ = json.Marshal(value)
	}
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return processHTTPResponse{StatusCode: response.StatusCode, Body: data}
}
