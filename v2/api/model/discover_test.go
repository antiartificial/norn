package model

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverAllAppsIncludesNamedDraftsAndSkipsNamelessDocuments(t *testing.T) {
	root := t.TempDir()
	writeDiscoverySpec(t, root, "draft", `name: draft
processes:
  worker:
    command: ./worker
`)
	writeDiscoverySpec(t, root, "unrelated", `deploy: false
volumes:
  - name: projects
`)

	all, err := DiscoverAllApps(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].App != "draft" {
		t.Fatalf("DiscoverAllApps() = %#v, want only named draft", all)
	}

	deployable, err := DiscoverApps(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployable) != 0 {
		t.Fatalf("DiscoverApps() = %#v, want no disabled drafts", deployable)
	}
}

func TestDiscoverAppsIncludesNamedEnabledApp(t *testing.T) {
	root := t.TempDir()
	writeDiscoverySpec(t, root, "enabled", `name: enabled
deploy: true
processes:
  web:
    port: 8080
`)

	specs, err := DiscoverApps(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].App != "enabled" {
		t.Fatalf("DiscoverApps() = %#v, want enabled app", specs)
	}
}

func writeDiscoverySpec(t *testing.T, root, directory, contents string) {
	t.Helper()
	dir := filepath.Join(root, directory)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "infraspec.yaml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
