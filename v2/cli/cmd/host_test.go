package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveHostScriptUsesExplicitPath(t *testing.T) {
	temp := t.TempDir()
	path := filepath.Join(temp, "host-runtime")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := hostScript
	hostScript = path
	t.Cleanup(func() { hostScript = previous })

	got, err := resolveHostScript()
	if err != nil {
		t.Fatalf("resolveHostScript: %v", err)
	}
	if got != path {
		t.Fatalf("path = %q, want %q", got, path)
	}
}

func TestResolveHostScriptUsesRepo(t *testing.T) {
	temp := t.TempDir()
	path := filepath.Join(temp, "v2", "scripts", "host-runtime")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previousScript, previousRepo := hostScript, hostRepo
	hostScript, hostRepo = "", temp
	t.Cleanup(func() { hostScript, hostRepo = previousScript, previousRepo })

	got, err := resolveHostScript()
	if err != nil {
		t.Fatalf("resolveHostScript: %v", err)
	}
	if got != path {
		t.Fatalf("path = %q, want %q", got, path)
	}
}

func TestHostRepoFromArgs(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "separate", args: []string{"install", "--repo", "/srv/norn"}, want: "/srv/norn"},
		{name: "equals", args: []string{"install", "--repo=/srv/norn"}, want: "/srv/norn"},
		{name: "missing", args: []string{"status"}, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := hostRepoFromArgs(tt.args); got != tt.want {
				t.Fatalf("hostRepoFromArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHostPrerequisitesRejectsUnknownConnectorBeforeCheckingHost(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", filepath.Join(repo, "v2", "scripts", "host-runtime"), "prerequisites", "--connector", "not-a-connector", "--check")
	command.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("prerequisites accepted an unknown connector:\n%s", out)
	}
	if !strings.Contains(string(out), "unsupported connector not-a-connector") {
		t.Fatalf("unexpected prerequisite error:\n%s", out)
	}
}
