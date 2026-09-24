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
)

// TestEtcdSourceValidationProcess proves the norn-api executable selects the
// narrow etcd runtime before PostgreSQL, accepts only managed etcd tokens, and
// drives the signed source-only aggregate through a worker to a terminal replay.
func TestEtcdSourceValidationProcess(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/source-validation-process/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	secret := "source-validation-process-token-secret-000"
	authority := uuid.NewString()
	identities := etcdstore.NewAuthStore(client, prefix)
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSetup()
	if _, err := client.Get(setupCtx, prefix, clientv3.WithLimit(1)); err != nil {
		t.Skipf("etcd endpoint is unavailable: %v", err)
	}
	token, record, err := handler.IssueManagedAccessToken(setupCtx, secret, identities, "operator", []string{handler.ScopeAPIWrite}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	apps := t.TempDir()
	appDir := filepath.Join(apps, "receipt-demo")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte("name: receipt-demo\ndeploy: false\nprocesses:\n  worker:\n    command: sleep 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "README.md"), []byte("source validation fixture\n"), 0o600); err != nil {
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
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	command.Env = append(os.Environ(),
		"NORN_CONTROL_BACKEND=etcd",
		"NORN_ETCD_SOURCE_VALIDATION=true",
		"NORN_ETCD_ENDPOINTS="+endpoints,
		"NORN_ETCD_PREFIX="+prefix,
		"NORN_CONTROL_AUTHORITY="+authority,
		"NORN_DATABASE_URL=postgres://poisoned.invalid:1/never-open",
		"NORN_API_TOKEN="+secret,
		"NORN_AUDIT_SIGNING_KEY=source-validation-process-audit-key-000",
		"NORN_REQUIRE_EXPLICIT_AUTH=true",
		"NORN_BIND_ADDR=127.0.0.1",
		fmt.Sprintf("NORN_PORT=%d", port),
		"NORN_APPS_DIR="+apps,
		"NORN_UI_DIR=",
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		_ = command.Wait()
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForSourceHealth(t, base, &output)

	status, _ := postSourcePreflight(t, base, "", "retry-1")
	if status != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d", status)
	}
	status, _ = postSourcePreflight(t, base, secret, "retry-1")
	if status != http.StatusUnauthorized {
		t.Fatalf("shared API secret bypass status=%d", status)
	}
	beforeUnsupported, err := client.Get(context.Background(), prefix, clientv3.WithPrefix())
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(base+"/api/apps/receipt-demo/deploy", "application/json", strings.NewReader(`{"ref":"HEAD"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unsupported route status=%d", response.StatusCode)
	}
	afterUnsupported, err := client.Get(context.Background(), prefix, clientv3.WithPrefix())
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeUnsupported.Kvs) != len(afterUnsupported.Kvs) {
		t.Fatal("unsupported route wrote to etcd")
	}

	status, accepted := postSourcePreflight(t, base, token, "retry-1")
	if status != http.StatusAccepted || accepted.OperationID == "" || accepted.Replayed {
		t.Fatalf("initial acceptance status=%d payload=%+v", status, accepted)
	}
	var replay sourcePreflightResponse
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		status, replay = postSourcePreflight(t, base, token, "retry-1")
		if status == http.StatusOK && replay.Replayed && replay.Status == "succeeded" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if status != http.StatusOK || !replay.Replayed || replay.Status != "succeeded" || replay.OperationID != accepted.OperationID {
		t.Fatalf("terminal replay status=%d payload=%+v logs=%s", status, replay, output.String())
	}
	operation, err := http.NewRequest(http.MethodGet, base+"/api/v1/source-validation/operations/"+accepted.OperationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.Header.Set("Authorization", "Bearer "+token)
	operationResponse, err := http.DefaultClient.Do(operation)
	if err != nil {
		t.Fatal(err)
	}
	defer operationResponse.Body.Close()
	if operationResponse.StatusCode != http.StatusOK {
		t.Fatalf("operation status=%d", operationResponse.StatusCode)
	}

	revokeCtx, cancelRevoke := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRevoke()
	if _, err := identities.RevokeAccessToken(revokeCtx, record.JTI); err != nil {
		t.Fatal(err)
	}
	status, _ = postSourcePreflight(t, base, token, "retry-2")
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d", status)
	}
}

type sourcePreflightResponse struct {
	OperationID string `json:"operationId"`
	Status      string `json:"status"`
	Replayed    bool   `json:"replayed"`
}

func postSourcePreflight(t *testing.T, base, token, key string) (int, sourcePreflightResponse) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/source-validation/apps/receipt-demo/preflights", strings.NewReader(`{"ref":"HEAD"}`))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Idempotency-Key", key)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload sourcePreflightResponse
	_ = json.NewDecoder(response.Body).Decode(&payload)
	return response.StatusCode, payload
}

func waitForSourceHealth(t *testing.T, base string, output *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(base + "/api/health")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("source-validation process did not become healthy: %s", output.String())
}
