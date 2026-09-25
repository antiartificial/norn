package artifactstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
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

type blockingMaterializeStore struct {
	Store
	entered chan struct{}
	release chan struct{}
	payload []byte
}

func (s *blockingMaterializeStore) Materialize(ctx context.Context, _ Descriptor, destination io.Writer) error {
	s.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		_, err := destination.Write(s.payload)
		return err
	}
}

func TestMaterializePrivateSerializesSharedDirectory(t *testing.T) {
	payload := []byte("retained bytes")
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	descriptor := Descriptor{Key: KeyForSHA256(digest), SHA256: digest, Size: int64(len(payload))}
	directory := filepath.Join(t.TempDir(), "restore")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	objects := &blockingMaterializeStore{entered: make(chan struct{}, 2), release: make(chan struct{}), payload: payload}
	defer func() {
		select {
		case <-objects.release:
		default:
			close(objects.release)
		}
	}()
	result := make(chan error, 2)
	materialize := func() {
		path, err := MaterializePrivate(context.Background(), objects, descriptor, directory)
		if err == nil {
			err = os.Remove(path)
		}
		result <- err
	}
	go materialize()
	select {
	case <-objects.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first materialization never reached the store")
	}
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		materialize()
	}()
	<-secondStarted
	select {
	case <-objects.entered:
		t.Fatal("second materialization entered the store while the first held the directory lock")
	case <-time.After(150 * time.Millisecond):
	}
	close(objects.release)
	for range 2 {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("materialization did not finish after releasing the store")
		}
	}
}
