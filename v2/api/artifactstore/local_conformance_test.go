package artifactstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalStoreConformance(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := OpenLocal(root, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runStoreConformance(t, store, root)
}

func TestDescriptorAllowsExactly64GiB(t *testing.T) {
	digest := stringsOf('0', 64)
	maximum := Descriptor{Key: KeyForSHA256(digest), SHA256: digest, Size: MaxArtifactBytes}
	if err := maximum.Validate(); err != nil {
		t.Fatalf("64 GiB descriptor = %v", err)
	}
	maximum.Size++
	if err := maximum.Validate(); !errors.Is(err, ErrArtifactTooLarge) {
		t.Fatalf("64 GiB plus one = %v", err)
	}
}

// runStoreConformance captures the portable Store guarantees for a future S3
// adapter without inheriting archive.Store's []byte semantics.
func runStoreConformance(t *testing.T, store Store, localRoot string) {
	t.Helper()
	ctx := context.Background()
	payload := bytes.Repeat([]byte("mysql-artifact-stream\n"), 256*1024) // 5 MiB streamed in 128 KiB chunks.
	expected := descriptorFor(payload)

	source := &sizedReader{reader: bytes.NewReader(payload), maxRead: streamBufferBytes}
	got, err := store.Publish(ctx, expected, source)
	if err != nil || got != expected {
		t.Fatalf("publish = %+v, %v", got, err)
	}
	if source.maxSeen > streamBufferBytes {
		t.Fatalf("publish asked source for %d bytes; max is %d", source.maxSeen, streamBufferBytes)
	}

	var materialized boundedBuffer
	if err := store.Materialize(ctx, expected, &materialized); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !bytes.Equal(materialized.Bytes(), payload) || materialized.maxWrite > streamBufferBytes {
		t.Fatalf("materialized bytes=%d max write=%d", materialized.Len(), materialized.maxWrite)
	}
	if err := store.Verify(ctx, expected); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// The same content is idempotent, while a digest/key mismatch is refused
	// before touching the source stream.
	if _, err := store.Publish(ctx, expected, bytes.NewReader(payload)); err != nil {
		t.Fatalf("identical publish: %v", err)
	}
	wrongKey := expected
	wrongKey.Key = "mysql/meaningful-name.sql"
	untouched := &failReader{err: errors.New("source should not be read")}
	if _, err := store.Publish(ctx, wrongKey, untouched); !errors.Is(err, ErrInvalidDescriptor) || !untouched.unread() {
		t.Fatalf("non-content key = %v, source read=%v", err, !untouched.unread())
	}

	wrongDigest := expected
	wrongDigest.SHA256 = stringsOf('0', 64)
	wrongDigest.Key = KeyForSHA256(wrongDigest.SHA256)
	if _, err := store.Publish(ctx, wrongDigest, bytes.NewReader(payload)); !errors.Is(err, ErrArtifactCorrupt) {
		t.Fatalf("wrong digest = %v", err)
	}
	if _, err := store.Open(ctx, Descriptor{Key: expected.Key, SHA256: expected.SHA256, Size: MaxArtifactBytes + 1}); !errors.Is(err, ErrArtifactTooLarge) {
		t.Fatalf("over-64GiB open = %v", err)
	}

	if localRoot != "" {
		path := filepath.Join(localRoot, filepath.FromSlash(expected.Key))
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append([]byte(nil), payload[:len(payload)-1]...), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.Verify(ctx, expected); !errors.Is(err, ErrArtifactCorrupt) {
			t.Fatalf("tampered artifact verify = %v", err)
		}
	}
}

func TestLocalStoreCapacityAndSymlinkParent(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenLocal(root, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	payload := []byte("123456789")
	if _, err := store.Publish(context.Background(), descriptorFor(payload), bytes.NewReader(payload)); !errors.Is(err, ErrArtifactFull) {
		t.Fatalf("capacity = %v", err)
	}
	symlinkRoot := t.TempDir()
	if err := os.Chmod(symlinkRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkStore, err := OpenLocal(symlinkRoot, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = symlinkStore.Close() })
	if err := os.Symlink(outside, filepath.Join(symlinkRoot, "sha256")); err != nil {
		t.Fatal(err)
	}
	payload = []byte("safe")
	if _, err := symlinkStore.Publish(context.Background(), descriptorFor(payload), bytes.NewReader(payload)); err == nil {
		t.Fatal("publication followed a symlinked content directory")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("wrote through symlink: %v %v", entries, err)
	}
}

func descriptorFor(data []byte) Descriptor {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return Descriptor{Key: KeyForSHA256(digest), SHA256: digest, Size: int64(len(data))}
}

type sizedReader struct {
	reader  io.Reader
	maxRead int
	maxSeen int
}

func (r *sizedReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRead {
		r.maxSeen = len(p)
		return 0, errors.New("source read buffer was not bounded")
	}
	if len(p) > r.maxSeen {
		r.maxSeen = len(p)
	}
	return r.reader.Read(p)
}

type boundedBuffer struct {
	bytes.Buffer
	maxWrite int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > streamBufferBytes {
		return 0, errors.New("materialize write buffer was not bounded")
	}
	if len(p) > b.maxWrite {
		b.maxWrite = len(p)
	}
	return b.Buffer.Write(p)
}

type failReader struct {
	err  error
	read bool
}

func (r *failReader) Read([]byte) (int, error) {
	r.read = true
	return 0, r.err
}

func (r *failReader) unread() bool { return !r.read }

func stringsOf(character byte, count int) string {
	return string(bytes.Repeat([]byte{character}, count))
}
