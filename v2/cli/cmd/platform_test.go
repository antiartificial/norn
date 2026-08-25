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
