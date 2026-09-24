package main

import (
	"context"
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
)

// TestEtcdPassiveRuntimeProcess proves the real binary can expose its
// candidate health and schema-check contract without opening control PG.
// The endpoint fixture is opt-in, as with the normal etcd process tests.
func TestEtcdPassiveRuntimeProcess(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/passive-process/" + uuid.NewString()
	if _, err := client.Get(context.Background(), prefix); err != nil {
		t.Fatalf("etcd fixture unavailable: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	binary := filepath.Join(t.TempDir(), "norn-api")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build passive candidate: %v\n%s", err, output)
	}
	command := exec.Command(binary)
	var output lockedBuffer
	command.Stdout, command.Stderr = &output, &output
	command.Env = append(os.Environ(),
		"NORN_CONTROL_BACKEND=etcd", "NORN_ETCD_ENDPOINTS="+endpoints, "NORN_ETCD_PREFIX="+prefix,
		"NORN_DATABASE_URL=postgres://poisoned.invalid:1/never-open",
		"NORN_STARTUP_MODE=passive", "NORN_SCHEMA_MODE=check",
		"NORN_API_TOKEN=passive-etcd-process-token-000000000", "NORN_AUDIT_SIGNING_KEY=passive-etcd-process-audit-key-0000",
		"NORN_BIND_ADDR=127.0.0.1", fmt.Sprintf("NORN_PORT=%d", port), "NORN_UI_DIR=",
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Signal(os.Interrupt); _ = command.Wait() })
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForSourceHealth(t, base, &output)
	response, err := http.Get(base + "/api/schema")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("passive schema status=%d logs=%s", response.StatusCode, output.String())
	}
	response, err = http.Get(base + "/api/v1/fleet/plans")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("passive candidate exposed normal route status=%d", response.StatusCode)
	}
}
