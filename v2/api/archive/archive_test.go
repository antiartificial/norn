package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreVerifiedRoundTrip(t *testing.T) {
	ctx := context.Background()
	a, err := NewFSArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"receipt":"signed-bytes","sig":"abc"}`)
	ref, err := StoreVerified(ctx, a, "evidence/2026/09/rec-1.json", original)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Size != int64(len(original)) || ref.Checksum == "" {
		t.Fatalf("reference wrong: %+v", ref)
	}
	got, err := a.Get(ctx, ref.Key)
	if err != nil {
		t.Fatal(err)
	}
	// Original signed bytes survive byte-for-byte.
	if string(got) != string(original) {
		t.Fatalf("archived bytes differ: %q", got)
	}
	if err := a.Verify(ctx, ref); err != nil {
		t.Fatalf("verify of a good object failed: %v", err)
	}
}

func TestVerifyDetectsCorruptionAndMissing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, err := NewFSArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := StoreVerified(ctx, a, "rec.json", []byte("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the stored object on disk; verify must fail (so a caller would not
	// prune the source).
	if err := os.WriteFile(filepath.Join(dir, "rec.json"), []byte("tampered!!!!"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(ctx, ref); err == nil {
		t.Fatal("verify should fail on a corrupted object")
	}
	// A missing object is ErrNotFound / verify error.
	if _, err := a.Get(ctx, "does-not-exist"); err != ErrNotFound {
		t.Fatalf("missing get = %v, want ErrNotFound", err)
	}
	if err := a.Verify(ctx, Reference{Key: "does-not-exist", Checksum: "x", Size: 1}); err == nil {
		t.Fatal("verify of a missing object should fail")
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	a, err := NewFSArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := StoreVerified(ctx, a, "rec.json", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, ref.Key); err != nil {
		t.Fatal(err)
	}
	// Deleting again is a no-op.
	if err := a.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("second delete should be a no-op: %v", err)
	}
	if _, err := a.Get(ctx, ref.Key); err != ErrNotFound {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}
