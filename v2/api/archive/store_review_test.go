package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReviewLocalArchiveRejectsSymlinkedParent(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenLocal(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := os.Symlink(outside, filepath.Join(root, "evidence")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutImmutable(context.Background(), "evidence/item.json", []byte("review")); err == nil {
		t.Fatal("archive publication followed a parent symlink outside its root")
	}
	if _, err := os.Lstat(filepath.Join(outside, "item.json")); !os.IsNotExist(err) {
		t.Fatalf("archive wrote outside configured root: %v", err)
	}
}
