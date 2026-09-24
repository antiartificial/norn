package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// TestEtcdFleetRuntimeProcess proves the normal norn-api binary starts before
// touching a poisoned PostgreSQL URL and exposes only the bounded Fleet slice.
func TestEtcdFleetRuntimeProcess(t *testing.T) {
	for _, tt := range []struct {
		name        string
		databaseURL string
	}{
		{name: "absent PostgreSQL DSN"},
		{name: "poisoned PostgreSQL DSN", databaseURL: "postgres://poisoned.invalid:1/never-open"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testEtcdFleetRuntimeProcess(t, tt.databaseURL)
		})
	}
}

func testEtcdFleetRuntimeProcess(t *testing.T, databaseURL string) {
	t.Helper()
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/fleet-runtime/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	secret := "fleet-runtime-process-token-secret-000"
	authority := uuid.NewString()
	identities := etcdstore.NewAuthStore(client, prefix)
	token, _, err := handler.IssueManagedAccessToken(context.Background(), secret, identities, "operator", []string{handler.ScopeAPIRead, handler.ScopeAPIWrite}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	runnerToken, _, err := handler.IssueManagedAccessToken(context.Background(), secret, identities, "fleet-runner", []string{handler.ScopeFleetOperate}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := store.NewHMACAcceptanceSigner("fleet-runtime-process-audit-key-000")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	nonFleet := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "test"}, Kind: "app.preflight", Resource: "demo", Key: "non-fleet-operation"},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: "demo", Status: model.OperationQueued, Source: "test", Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}},
		Audit:     store.AcceptanceAuditContext{Source: "etcd-fleet-runtime-process-test"},
	}
	nonFleet.Fingerprint, err = store.CanonicalOperationRequestFingerprint(nonFleet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(context.Background(), nonFleet); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "cluster.yaml")
	document := []byte("apiVersion: norn.dev/fleet/v1\nkind: Cluster\nmetadata:\n  repository: example/norn-fleet\n  workflowURL: https://example.test/apply\ncluster:\n  name: test\n  provider: digitalocean\n  region: nyc3\nnodePools:\n  control:\n    size: s-2vcpu-4gb\n    min: 3\n    desired: 3\n    max: 5\n    labels: { workload: control-plane }\n    replacement:\n      strategy: blueGreen\n      requireCapacityHeadroom: true\n      requireReadiness: true\n      drainTimeout: 15m\n")
	if err := os.WriteFile(configPath, document, 0o600); err != nil {
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
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	command := exec.Command(binary)
	var output lockedBuffer
	command.Stdout, command.Stderr = &output, &output
	environment := os.Environ()
	if databaseURL == "" {
		environment = withoutEnvironment(environment, "NORN_DATABASE_URL")
	}
	command.Env = append(environment,
		"NORN_CONTROL_BACKEND=etcd", "NORN_ETCD_ENDPOINTS="+endpoints, "NORN_ETCD_PREFIX="+prefix, "NORN_CONTROL_AUTHORITY="+authority,
		"NORN_PROFILE=production", "NORN_ENVIRONMENT=production", "NORN_API_TOKEN="+secret, "NORN_AUDIT_SIGNING_KEY=fleet-runtime-process-audit-key-000", "NORN_AUDIT_RETENTION_DAYS=365", "NORN_REQUIRE_EXPLICIT_AUTH=true",
		"NORN_NOMAD_ADDR=https://nomad.example.test:4646", "NORN_CONSUL_ADDR=https://consul.example.test:8501", "CONSUL_HTTP_SSL_VERIFY=true", "NORN_REGISTRY_URL=registry.example.test/norn",
		"NORN_RELEASE_ADMISSION_MODE=keyless", "NORN_RELEASE_ATTESTATION_ISSUER=https://token.actions.githubusercontent.com", "NORN_RELEASE_ATTESTATION_ALLOWED_REPOSITORIES=example/norn", "NORN_RELEASE_ATTESTATION_ALLOWED_WORKFLOW_REFS=example/norn/.github/workflows/release.yml@"+strings.Repeat("a", 40), "NORN_RELEASE_REQUIRE_SBOM=true",
		"NORN_TRUSTED_QUALIFICATION_SIGNING_KEYS="+testEd25519Public('q'), "NORN_GITHUB_ACTIONS_OIDC_AUDIENCE=norn", "NORN_GITHUB_ACTIONS_ALLOWED_REPOSITORIES=example/norn@1@2", "NORN_GITHUB_ACTIONS_ALLOWED_WORKFLOW_REFS=example/norn/.github/workflows/release.yml@"+strings.Repeat("a", 40), "NORN_GITHUB_ACTIONS_ALLOWED_REFS=refs/tags/v*", "NORN_GITHUB_ACTIONS_ALLOWED_EVENTS=push", "NORN_GITHUB_ACTIONS_ALLOWED_APPS=demo", "NORN_GITHUB_ACTIONS_ALLOWED_ENVIRONMENTS=production", "NORN_GITHUB_ACTIONS_DEFAULT_BRANCH=main", "NORN_LEGACY_TOKEN_SIGNING_UNTIL=2020-01-01T00:00:00Z",
		"NORN_BIND_ADDR=127.0.0.1", fmt.Sprintf("NORN_PORT=%d", port), "NORN_FLEET_CONFIG="+configPath, "NORN_UI_DIR=",
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
	request, err := http.NewRequest(http.MethodPost, base+"/api/v1/fleet/node-pools/control/plan", bytes.NewBufferString(`{"desired":4,"reason":"process proof"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", "fleet-runtime-1")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("plan status=%d output=%s", response.StatusCode, output.String())
	}
	var plan map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	id, _ := plan["id"].(string)
	if id == "" {
		t.Fatalf("plan response missing id: %#v", plan)
	}
	// The receipt remains the replay source after its original mutable YAML
	// document changes or vanishes.
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequest(http.MethodPost, base+"/api/v1/fleet/node-pools/control/plan", bytes.NewBufferString(`{"desired":4,"reason":"process proof"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", "fleet-runtime-1")
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("replay status=%d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, base+"/api/v1/fleet/plans", nil)
	request.Header.Set("Authorization", "Bearer "+runnerToken)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("runner global Fleet list status=%d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodPost, base+"/api/v1/fleet/node-pools/control/plan", bytes.NewBufferString(`{"desired":4}`))
	request.Header.Set("Authorization", "Bearer "+runnerToken)
	request.Header.Set("Idempotency-Key", "runner-must-not-plan")
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("runner plan status=%d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, base+"/api/v1/operations/"+id, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("operation status=%d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, base+"/api/v1/operations/"+nonFleet.Operation.ID, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("non-Fleet operation status=%d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, base+"/api/v1/operations/"+id, nil)
	request.Header.Set("Authorization", "Bearer "+runnerToken)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("runner operation status=%d", response.StatusCode)
	}
	response, err = http.Post(base+"/api/v1/apps/demo/releases/deployments", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotImplemented {
		t.Fatalf("unsupported status=%d", response.StatusCode)
	}
}

func withoutEnvironment(environment []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment))
	for _, value := range environment {
		if !strings.HasPrefix(value, prefix) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}
