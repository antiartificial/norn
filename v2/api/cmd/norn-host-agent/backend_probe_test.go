package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEtcdBackendProbeReportsSelectionWithoutOpeningPostgres(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "norn-host-agent")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build host agent: %v: %s", err, out)
	}
	cmd := exec.Command(binary, "--norn-control-backend-probe")
	cmd.Env = append(os.Environ(), "NORN_CONTROL_BACKEND=etcd", "NORN_ETCD_ENDPOINTS=https://127.0.0.1:1", "NORN_DATABASE_URL=postgres://127.0.0.1:1/poison")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("etcd probe failed: %v: %s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, `"backend":"etcd"`) {
		t.Fatalf("probe did not report etcd selection: %s", text)
	}
	if strings.Contains(strings.ToLower(text), "database:") {
		t.Fatalf("probe attempted postgres: %s", text)
	}
}
