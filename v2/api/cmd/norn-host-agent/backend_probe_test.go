package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEtcdBackendProbeNeverFallsBackToPostgres(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "norn-host-agent")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build host agent: %v: %s", err, out)
	}
	cmd := exec.Command(binary, "--norn-control-backend-probe")
	cmd.Env = append(os.Environ(), "NORN_CONTROL_BACKEND=etcd", "NORN_ETCD_ENDPOINTS=https://127.0.0.1:1", "NORN_DATABASE_URL=postgres://127.0.0.1:1/poison")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("etcd probe unexpectedly succeeded")
	}
	text := string(out)
	if !strings.Contains(text, "etcd control backend is not available") {
		t.Fatalf("probe did not fail closed: %s", text)
	}
	if strings.Contains(strings.ToLower(text), "database:") {
		t.Fatalf("probe attempted postgres: %s", text)
	}
}
