package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPlatformReleasesUsesVersionFirstDisplayAndExactArtifactIdentity(t *testing.T) {
	releasesDir := t.TempDir()
	currentLink := filepath.Join(t.TempDir(), "current")
	t.Setenv("NORN_RELEASES_DIR", releasesDir)
	t.Setenv("NORN_CURRENT_LINK", currentLink)

	semanticSHA := "1111111111111111111111111111111111111111"
	rebuiltOldSHA := "1414141414141414141414141414141414141414"
	legacySHA := "2222222222222222222222222222222222222222"
	currentSHA := "3333333333333333333333333333333333333333"
	writeReleaseFixture(t, releasesDir, semanticSHA, map[string]any{
		"sha": semanticSHA, "version": "v2.20.0-control", "createdAt": "2026-08-27T12:00:00Z",
	})
	writeReleaseFixture(t, releasesDir, rebuiltOldSHA, map[string]any{
		"sha": rebuiltOldSHA, "version": "v2.14.1-platform-34-g1414141", "createdAt": "2026-08-27T12:30:00Z",
	})
	writeReleaseFixture(t, releasesDir, legacySHA, map[string]any{
		"sha": legacySHA, "version": "platform-1111111111111111111111111111111111111111-1-g2222222", "createdAt": "2026-08-27T13:00:00Z",
	})
	writeReleaseFixture(t, releasesDir, currentSHA, map[string]any{
		// Metadata cannot replace the exact directory identity or artifact path.
		"sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "version": "platform-3333333333333333333333333333333333333333", "createdAt": "2026-08-27T09:00:00-05:00", "path": "/tmp/not-the-artifact",
	})
	writeReleaseFixture(t, releasesDir, ".staging-3333333", map[string]any{
		"sha": currentSHA, "version": "platform-3333333333333333333333333333333333333333", "createdAt": "2026-08-27T13:00:00Z",
	})
	writeReleaseFixture(t, releasesDir, currentSHA+".unsigned-local", map[string]any{
		"sha": currentSHA, "version": "platform-3333333333333333333333333333333333333333", "createdAt": "2026-08-27T14:00:00Z",
	})
	if err := os.Symlink(filepath.Join(releasesDir, currentSHA), currentLink); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	(&Handler{}).PlatformReleases(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/releases", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response platformReleaseList
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Releases) != 4 {
		t.Fatalf("release count = %d, want 4: %#v", len(response.Releases), response.Releases)
	}
	wantDisplays := []string{
		"v2.20.0-platform-g3333333",
		"v2.20.0-platform-g2222222",
		"v2.14.1-platform-34-g1414141",
		"v2.20.0-platform",
	}
	for i, want := range wantDisplays {
		if response.Releases[i].DisplayVersion != want {
			t.Errorf("release %d displayVersion = %q, want %q", i, response.Releases[i].DisplayVersion, want)
		}
	}
	current := response.Releases[0]
	if current.SHA != currentSHA || current.Path != filepath.Join(releasesDir, currentSHA) || !current.Current {
		t.Errorf("current exact artifact identity was not preserved: %#v", current)
	}
}

func TestPlatformReleaseDisplayVersionPreservesDescribeDistance(t *testing.T) {
	display, base := platformReleaseDisplayVersion("v2.14.1-platform-34-g29158d0", "29158d0d019232385962df8a4d5958a124f0c19a", "")
	if display != "v2.14.1-platform-34-g29158d0" || base != "v2.14.1" {
		t.Fatalf("display = %q, base = %q", display, base)
	}
}

func TestPlatformReleasesPreservesCandidateLabelWithoutAdvancingStableBase(t *testing.T) {
	releasesDir := t.TempDir()
	t.Setenv("NORN_RELEASES_DIR", releasesDir)
	t.Setenv("NORN_CURRENT_LINK", filepath.Join(t.TempDir(), "current"))

	stableSHA := "1111111111111111111111111111111111111111"
	candidateSHA := "2222222222222222222222222222222222222222"
	transportSHA := "3333333333333333333333333333333333333333"
	writeReleaseFixture(t, releasesDir, stableSHA, map[string]any{
		"version": "v2.20.0-platform", "createdAt": "2026-10-10T10:00:00Z",
	})
	writeReleaseFixture(t, releasesDir, candidateSHA, map[string]any{
		"version": "v3.0.0-rc.1-platform", "createdAt": "2026-10-10T11:00:00Z",
	})
	writeReleaseFixture(t, releasesDir, transportSHA, map[string]any{
		"version": "platform-3333333333333333333333333333333333333333-1-g3333333", "createdAt": "2026-10-10T12:00:00Z",
	})

	recorder := httptest.NewRecorder()
	(&Handler{}).PlatformReleases(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/releases", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response platformReleaseList
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Releases) != 3 {
		t.Fatalf("release count = %d, want 3", len(response.Releases))
	}
	// The API sorts newest first. The RC remains verbatim, while a later
	// transport tag still inherits the last GA semantic base, v2.20.0.
	if got, want := response.Releases[1].DisplayVersion, "v3.0.0-rc.1-platform"; got != want {
		t.Errorf("candidate displayVersion = %q, want %q", got, want)
	}
	if got, want := response.Releases[0].DisplayVersion, "v2.20.0-platform-g3333333"; got != want {
		t.Errorf("transport displayVersion after candidate = %q, want %q", got, want)
	}
}

func TestPlatformReleaseDisplayVersionDoesNotTreatCandidateAsStableBase(t *testing.T) {
	display, base := platformReleaseDisplayVersion("v3.0.0-rc.1-platform", "2222222222222222222222222222222222222222", "v2.20.0")
	if display != "v3.0.0-rc.1-platform" || base != "" {
		t.Fatalf("display = %q, base = %q; want exact candidate label and no stable base", display, base)
	}
}

func TestPlatformReleaseDisplayVersionPreservesOnlyCurrentRCConvention(t *testing.T) {
	for _, version := range []string{
		"v3.0.0-rc.1-platform",
		"v3.0.0-rc.1-platform-34-g29158d0",
	} {
		display, base := platformReleaseDisplayVersion(version, "2222222222222222222222222222222222222222", "v2.20.0")
		if display != version || base != "" {
			t.Errorf("version %q: display = %q, base = %q; want verbatim label and no stable base", version, display, base)
		}
	}
}

func TestPlatformReleaseDisplayVersionDoesNotClassifyMalformedRCAsCandidate(t *testing.T) {
	for _, version := range []string{
		"v3.0.0-preview.1-platform",
		"v3.0.0-rc.preview-platform",
		"v3.0.0-rc.1-control",
	} {
		display, base := platformReleaseDisplayVersion(version, "2222222222222222222222222222222222222222", "v2.20.0")
		if display != version || base != "" {
			t.Errorf("malformed candidate %q: display = %q, base = %q; want original label and no stable base", version, display, base)
		}
	}
}

func writeReleaseFixture(t *testing.T, releasesDir, name string, metadata map[string]any) {
	t.Helper()
	dir := filepath.Join(releasesDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
