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

func TestDiscoverAppsIgnoresRetainedSourceWithExplicitMarker(t *testing.T) {
	root := t.TempDir()
	writeDiscoverySpec(t, root, "watchtower", "name: watchtower\ndeploy: true\n")
	writeDiscoverySpec(t, root, "watchtower.pre-git", "name: watchtower\ndeploy: true\n")
	marker := filepath.Join(root, "watchtower.pre-git", DiscoveryIgnoreFile)
	if err := os.WriteFile(marker, []byte("retained pre-git source; current watchtower checkout owns the app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, discover := range []func(string) ([]*InfraSpec, error){DiscoverApps, DiscoverAllApps} {
		specs, err := discover(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(specs) != 1 || specs[0].App != "watchtower" {
			t.Fatalf("discovery = %#v, want current checkout only", specs)
		}
	}
}

func TestFindByRepoURLRequiresConfiguredHostAndBranch(t *testing.T) {
	spec := &InfraSpec{
		App:  "demo",
		Repo: &RepoSpec{URL: "https://github.com/owner/repo.git", Branch: "main", AutoDeploy: true},
	}
	specs := []*InfraSpec{spec}

	if got := FindByRepoURL(specs, "git@github.com:owner/repo.git", "main"); got != spec {
		t.Fatalf("SSH form of configured repository did not select app: got=%p want=%p", got, spec)
	}
	if got := FindByRepoURL(specs, "https://git.example.test/owner/repo.git", "main"); got != nil {
		t.Fatalf("repository on unrelated host selected app %q", got.App)
	}
	if got := FindByRepoURL(specs, "https://github.com/owner/repo.git", "release"); got != nil {
		t.Fatalf("unconfigured branch selected app %q", got.App)
	}

	spec.Repo.AutoDeploy = false
	if got := FindByRepoURL(specs, "https://github.com/owner/repo.git", "main"); got != nil {
		t.Fatalf("app with autoDeploy disabled selected app %q", got.App)
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
