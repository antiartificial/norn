package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlatformExecutionPathPreservesCallerAndAddsInstalledToolDirectories(t *testing.T) {
	got := filepath.SplitList(platformExecutionPath("/custom/bin:/usr/bin:/custom/bin"))
	joined := ":" + strings.Join(got, ":") + ":"
	if !strings.Contains(joined, ":/custom/bin:") || !strings.Contains(joined, ":/usr/bin:") {
		t.Fatalf("caller path was not preserved: %v", got)
	}
	if strings.Count(joined, ":/custom/bin:") != 1 {
		t.Fatalf("path was not deduplicated: %v", got)
	}
	for _, candidate := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() && !strings.Contains(joined, ":"+candidate+":") {
			t.Fatalf("installed tool directory %s was not added: %v", candidate, got)
		}
	}
}

func TestReplaceEnvironmentValueRemovesDuplicateKeys(t *testing.T) {
	got := replaceEnvironmentValue([]string{"HOME=/tmp/home", "PATH=/old", "PATH=/older"}, "PATH", "/new")
	if strings.Join(got, "|") != "HOME=/tmp/home|PATH=/new" {
		t.Fatalf("unexpected environment: %v", got)
	}
}

func TestPlatformScriptCandidatesPreferExplicitAndManagedPortablePaths(t *testing.T) {
	candidates := platformScriptCandidates("/repo", "/work", "/opt/norn/bin/norn", "/operator")
	want := []string{
		"/repo/v2/scripts/platform-upgrade",
		"/work/v2/scripts/platform-upgrade",
		"/work/scripts/platform-upgrade",
		"/opt/norn/bin/platform-upgrade",
		"/opt/norn/scripts/platform-upgrade",
		"/operator/.config/norn/host/bin/platform-upgrade",
		"/operator/projects/norn/v2/scripts/platform-upgrade",
	}
	if strings.Join(candidates, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected platform script search order: %v", candidates)
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, "/Users/0xadb/") {
			t.Fatalf("machine-specific platform path leaked into discovery: %s", candidate)
		}
	}
}

func TestPlatformRebuildArgumentsRequireFullSHAAndVerification(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	arguments, err := platformRebuildArguments(sha, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(arguments, " "), "rebuild "+sha+" --verify"; got != want {
		t.Fatalf("arguments = %q, want %q", got, want)
	}
	for _, test := range []struct {
		name   string
		sha    string
		verify bool
	}{
		{name: "short SHA", sha: sha[:12], verify: true},
		{name: "non-hex SHA", sha: strings.Repeat("g", 40), verify: true},
		{name: "verification omitted", sha: sha, verify: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := platformRebuildArguments(test.sha, test.verify); err == nil {
				t.Fatal("expected rebuild argument validation to fail")
			}
		})
	}
}
