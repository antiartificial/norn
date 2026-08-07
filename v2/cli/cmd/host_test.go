package cmd

import (
	"os"
	"path/filepath"
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
