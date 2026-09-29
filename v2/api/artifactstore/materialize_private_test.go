package artifactstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type processMaterializeStore struct {
	Store
	entered string
	release string
	payload []byte
}

func (s processMaterializeStore) Materialize(ctx context.Context, _ Descriptor, destination io.Writer) error {
	if err := os.WriteFile(s.entered, nil, 0o600); err != nil {
		return err
	}
	for {
		if _, err := os.Stat(s.release); err == nil {
			_, err = destination.Write(s.payload)
			return err
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestMaterializePrivateProcessHelper(t *testing.T) {
	if os.Getenv("NORN_MATERIALIZE_PROCESS_HELPER") != "1" {
		return
	}
	payload := []byte("retained bytes")
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	descriptor := Descriptor{Key: KeyForSHA256(digest), SHA256: digest, Size: int64(len(payload))}
	path, err := MaterializePrivate(context.Background(), processMaterializeStore{
		entered: os.Getenv("NORN_MATERIALIZE_ENTERED"),
		release: os.Getenv("NORN_MATERIALIZE_RELEASE"), payload: payload,
	}, descriptor, os.Getenv("NORN_MATERIALIZE_DIRECTORY"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializePrivateProcessCrashReleasesDirectoryLock(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "restore")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(t.TempDir(), "release")
	start := func(marker string) (*exec.Cmd, *bytes.Buffer) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMaterializePrivateProcessHelper$")
		cmd.Env = append(os.Environ(),
			"NORN_MATERIALIZE_PROCESS_HELPER=1",
			"NORN_MATERIALIZE_DIRECTORY="+directory,
			"NORN_MATERIALIZE_ENTERED="+marker,
			"NORN_MATERIALIZE_RELEASE="+release,
		)
		output := &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = output, output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd, output
	}
	waitFor := func(path string) {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(path); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("process did not reach materialization: %s", filepath.Base(path))
	}
	firstMarker := filepath.Join(t.TempDir(), "first-entered")
	first, firstOutput := start(firstMarker)
	waitFor(firstMarker)
	secondMarker := filepath.Join(t.TempDir(), "second-entered")
	second, secondOutput := start(secondMarker)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(secondMarker); !os.IsNotExist(err) {
		t.Fatalf("second process entered while first held lock: %v", err)
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err == nil {
		t.Fatalf("first process unexpectedly exited cleanly: %s", firstOutput.String())
	}
	waitFor(secondMarker)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second process could not materialize after crash: %v\n%s", err, secondOutput.String())
	}
	leftovers, err := filepath.Glob(filepath.Join(directory, ".norn-artifact-*"))
	if err != nil || len(leftovers) != 1 {
		t.Fatalf("expected one inspectable file from killed materialization, got %d: %v", len(leftovers), err)
	}
}

type unverifiedMaterializeStore struct {
	Store
	payload []byte
}

func (s unverifiedMaterializeStore) Materialize(_ context.Context, _ Descriptor, destination io.Writer) error {
	_, err := destination.Write(s.payload)
	return err
}

func TestMaterializePrivateChecksFinalBytesIndependently(t *testing.T) {
	wanted := []byte("retained source bytes")
	digest := fmt.Sprintf("%x", sha256.Sum256(wanted))
	descriptor := Descriptor{Key: KeyForSHA256(digest), SHA256: digest, Size: int64(len(wanted))}
	for name, payload := range map[string][]byte{
		"short":   wanted[:len(wanted)-1],
		"long":    append(bytes.Clone(wanted), '!'),
		"changed": append([]byte("X"), wanted[1:]...),
	} {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "restore")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			_, err := MaterializePrivate(context.Background(), unverifiedMaterializeStore{payload: payload}, descriptor, directory)
			if !errors.Is(err, ErrArtifactCorrupt) {
				t.Fatalf("materialization with %s bytes: %v", name, err)
			}
			files, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				if file.Name() != ".norn-materialize.lock" {
					t.Fatalf("unverified materialization left %q", file.Name())
				}
			}
		})
	}
}

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
