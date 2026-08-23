package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostSecurityInitDryRunIsNonMutating(t *testing.T) {
	repo, err := hostSecurityTestRepo()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo, "v2", "scripts", "host-runtime")
	configDir := filepath.Join(t.TempDir(), "config")
	stateDir := filepath.Join(t.TempDir(), "state")
	command := exec.Command("bash", script, "security-init", "--repo", repo, "--config-dir", configDir, "--state-dir", stateDir, "--address", "127.0.0.1", "--dry-run")
	command.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("security-init dry-run: %v\n%s", err, out)
	}
	text := string(out)
	for _, expected := range []string{"would generate a non-active", "would not modify managed configs", "bootstrap ACL tokens"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("dry-run missing %q:\n%s", expected, text)
		}
	}
	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Fatalf("dry-run created config directory: %v", err)
	}
}

func TestHostRenderRejectsActivatedSecurityFragmentsUntilRecoveryIsTLSAware(t *testing.T) {
	repo, err := hostSecurityTestRepo()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo, "v2", "scripts", "host-runtime")
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	stateDir := filepath.Join(root, "state")
	for _, dir := range []string{filepath.Join(configDir, "sources"), filepath.Join(configDir, "security", "active")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	nomadSource := "data_dir = \"/tmp/old\"\nbind_addr = \"0.0.0.0\"\nadvertise {\n  http = \"127.0.0.1\"\n  rpc = \"127.0.0.1\"\n  serf = \"127.0.0.1\"\n}\nclient {\n}\n"
	consulSource := "data_dir = \"/tmp/old\"\nbind_addr = \"127.0.0.1\"\n"
	files := map[string]string{
		filepath.Join(configDir, "sources", "nomad.hcl"):             nomadSource,
		filepath.Join(configDir, "sources", "consul.hcl"):            consulSource,
		filepath.Join(configDir, "security", "active", "nomad.hcl"):  "acl { enabled = true }\n",
		filepath.Join(configDir, "security", "active", "consul.hcl"): "acl { enabled = true }\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("bash", script, "render", "--repo", repo, "--config-dir", configDir, "--state-dir", stateDir, "--address", "127.0.0.2")
	command.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("host render unexpectedly accepted active fragments:\n%s", out)
	}
	if !strings.Contains(string(out), "activation is unavailable") {
		t.Fatalf("host render did not explain activation refusal:\n%s", out)
	}
}

func TestHostSecurityInitPublishesPrivateStageAtomically(t *testing.T) {
	repo, err := hostSecurityTestRepo()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeHostSecurityStub(t, filepath.Join(binDir, "nomad"), `
case "$*" in
  *"tls ca create"*) touch nomad-agent-ca.pem nomad-agent-ca-key.pem ;;
  *"tls cert create -server"*) touch global-server-nomad.pem global-server-nomad-key.pem ;;
  *"tls cert create -cli"*) touch global-cli-nomad.pem global-cli-nomad-key.pem ;;
esac`)
	writeHostSecurityStub(t, filepath.Join(binDir, "consul"), `
case "$*" in
  *"tls ca create"*) touch consul-agent-ca.pem consul-agent-ca-key.pem ;;
  *"tls cert create -server"*) touch dc1-server-consul-0.pem dc1-server-consul-0-key.pem ;;
  *"tls cert create -client"*) touch dc1-client-consul-0.pem dc1-client-consul-0-key.pem ;;
esac`)

	securityDir := filepath.Join(root, "security")
	command := exec.Command(filepath.Join(repo, "v2", "scripts", "host-runtime"), "security-init", "--repo", repo, "--security-dir", securityDir, "--address", "127.0.0.1")
	command.Env = append(os.Environ(), "HOME="+filepath.Join(root, "home"), "PATH="+binDir+":"+os.Getenv("PATH"))
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("security-init: %v\n%s", err, out)
	}
	stages, err := filepath.Glob(filepath.Join(securityDir, "staged-*"))
	if err != nil || len(stages) != 1 {
		t.Fatalf("published stages = %v, err=%v", stages, err)
	}
	for path, want := range map[string]os.FileMode{
		stages[0]:                         0o700,
		filepath.Join(stages[0], "nomad"): 0o700,
		filepath.Join(stages[0], "nomad", "nomad-agent-ca-key.pem"): 0o600,
		filepath.Join(stages[0], "nomad", "nomad-agent-ca.pem"):     0o644,
		filepath.Join(stages[0], "manifest.json"):                   0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(securityDir, ".staging.*")); len(leftovers) != 0 {
		t.Fatalf("temporary security stages remain: %v", leftovers)
	}
}

func TestHostSecurityInitCleansFailedStage(t *testing.T) {
	repo, err := hostSecurityTestRepo()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeHostSecurityStub(t, filepath.Join(binDir, "nomad"), `exit 42`)
	writeHostSecurityStub(t, filepath.Join(binDir, "consul"), `exit 0`)
	securityDir := filepath.Join(root, "security")
	command := exec.Command(filepath.Join(repo, "v2", "scripts", "host-runtime"), "security-init", "--repo", repo, "--security-dir", securityDir, "--address", "127.0.0.1")
	command.Env = append(os.Environ(), "HOME="+filepath.Join(root, "home"), "PATH="+binDir+":"+os.Getenv("PATH"))
	if out, err := command.CombinedOutput(); err == nil {
		t.Fatalf("security-init unexpectedly succeeded:\n%s", out)
	}
	entries, err := os.ReadDir(securityDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed security-init left artifacts: %v", entries)
	}
}

func writeHostSecurityStub(t *testing.T, path, body string) {
	t.Helper()
	content := "#!/bin/sh\nset -eu\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func hostSecurityTestRepo() (string, error) {
	return filepath.Abs(filepath.Join("..", "..", ".."))
}
