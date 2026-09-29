package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"norn/v2/api/config"
	"norn/v2/api/model"
)

func TestListAppsExcludesRetainedSource(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"watchtower", "watchtower.pre-git"} {
		path := filepath.Join(root, directory)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "infraspec.yaml"), []byte("name: watchtower\ndeploy: true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "watchtower.pre-git", model.DiscoveryIgnoreFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	apps := listAppsFromDirectory(t, root)
	if len(apps) != 1 || apps[0].Spec == nil || apps[0].Spec.App != "watchtower" {
		t.Fatalf("inventory has %d records; want one watchtower", len(apps))
	}
}

// Run against a real source directory only when explicitly selected. The
// handler has no database or runtime clients, so this reads source specs only.
func TestListAppsMiniSourceQualification(t *testing.T) {
	root := os.Getenv("NORN_TEST_APPS_DIR")
	wantText := os.Getenv("NORN_TEST_EXPECTED_APPS")
	if root == "" || wantText == "" {
		t.Skip("requires an explicit source directory and expected inventory count")
	}
	want, err := strconv.Atoi(wantText)
	if err != nil || want < 1 {
		t.Fatal("invalid expected inventory count")
	}
	apps := listAppsFromDirectory(t, root)
	if len(apps) != want {
		t.Fatalf("inventory has %d records, want %d", len(apps), want)
	}
	watchtower := 0
	canonical, err := model.LoadInfraSpec(filepath.Join(root, "watchtower", "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	canonicalJSON, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.Spec == nil {
			t.Fatal("inventory contains an empty spec")
		}
		if app.Spec.App == "watchtower" {
			watchtower++
			actualJSON, err := json.Marshal(app.Spec)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actualJSON, canonicalJSON) {
				t.Fatal("watchtower inventory does not match the current checkout")
			}
		}
	}
	if watchtower != 1 {
		t.Fatalf("inventory has %d watchtower records, want one", watchtower)
	}
	t.Logf("candidate handler inventory=%d watchtower=%d", len(apps), watchtower)
}

func listAppsFromDirectory(t *testing.T, root string) []model.AppStatus {
	t.Helper()
	h := &Handler{cfg: &config.Config{AppsDir: root}}
	response := httptest.NewRecorder()
	h.ListApps(response, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("inventory status=%d", response.Code)
	}
	var apps []model.AppStatus
	if err := json.Unmarshal(response.Body.Bytes(), &apps); err != nil {
		t.Fatal(err)
	}
	return apps
}
