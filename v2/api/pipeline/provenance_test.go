package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLocalGitChangesReportsDirtyFiles(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "norn@example.test")
	runGit(t, dir, "config", "user.name", "Norn Test")
	writeFile(t, filepath.Join(dir, "tracked.txt"), "clean\n")
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "commit", "-m", "initial")

	writeFile(t, filepath.Join(dir, "tracked.txt"), "dirty\n")
	writeFile(t, filepath.Join(dir, "new.txt"), "new\n")

	dirty, changes := localGitChanges(context.Background(), dir)
	if !dirty {
		t.Fatal("dirty = false, want true")
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %v, want 2 entries", changes)
	}
}

func TestLocalGitChangesCleanRepo(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "norn@example.test")
	runGit(t, dir, "config", "user.name", "Norn Test")
	writeFile(t, filepath.Join(dir, "tracked.txt"), "clean\n")
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "commit", "-m", "initial")

	dirty, changes := localGitChanges(context.Background(), dir)
	if dirty {
		t.Fatalf("dirty = true with changes %v, want false", changes)
	}
	if len(changes) != 0 {
		t.Fatalf("changes = %v, want none", changes)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
