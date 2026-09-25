package artifactstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializePrivateUsesVerifiedRetainedBytes(t *testing.T) {
	root := t.TempDir()
	objects, err := OpenLocal(filepath.Join(root, "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	payload := []byte("retained source bytes")
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	descriptor := Descriptor{Key: KeyForSHA256(digest), SHA256: digest, Size: int64(len(payload))}
	if _, err := objects.Publish(context.Background(), descriptor, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "restore")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := MaterializePrivate(context.Background(), objects, descriptor, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("materialized bytes: %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("materialized mode: %v, %v", info, err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializePrivate(context.Background(), objects, descriptor, directory); err == nil {
		t.Fatal("public directory accepted")
	}
}
