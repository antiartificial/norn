package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Downloaded manifests and dumps are opened without following symlinks and
// without blocking; FIFOs, symlinks and oversized manifests are refused
// before any read.
func TestImportReadsAreBoundedAndRefuseNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan error, 2)
	go func() { _, err := readExportManifest(fifo); done <- err }()
	go func() { done <- verifyRegularFileDigest(fifo, strings.Repeat("0", 64), 1) }()
	for range 2 {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("a FIFO was accepted")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reading a FIFO blocked")
		}
	}
	large := filepath.Join(dir, "large.json")
	if err := os.WriteFile(large, []byte(`{"schema":"`+strings.Repeat("x", maxExportManifestBytes)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readExportManifest(large); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized manifest = %v", err)
	}
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"schema":"`+snapshotExportSchema+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readExportManifest(link); err == nil {
		t.Fatal("a symlinked manifest was followed")
	}
	if _, err := readExportManifest(target); err != nil {
		t.Fatalf("regular manifest = %v", err)
	}
}
