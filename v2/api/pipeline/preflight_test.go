package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParentPathReferencesFindsGoModReplace(t *testing.T) {
	root := t.TempDir()
	modDir := filepath.Join(root, "service")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goMod := []byte(`module example.com/service

go 1.25

replace github.com/antiartificial/contextdb => ../contextdb
`)
	if err := os.WriteFile(filepath.Join(modDir, "go.mod"), goMod, 0o644); err != nil {
		t.Fatal(err)
	}

	refs := parentPathReferences(root)
	if len(refs) != 1 {
		t.Fatalf("refs = %d, want 1: %#v", len(refs), refs)
	}
	if refs[0] != "service/go.mod: replace github.com/antiartificial/contextdb => ../contextdb" {
		t.Fatalf("ref = %q", refs[0])
	}
}

func TestParentPathReferencesIgnoresOrdinaryGoMod(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	refs := parentPathReferences(root)
	if len(refs) != 0 {
		t.Fatalf("refs = %#v, want none", refs)
	}
}
