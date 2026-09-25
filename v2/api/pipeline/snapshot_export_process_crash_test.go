package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type processCrashSnapshotObjects struct {
	root          string
	exitAfterDump bool
}

func (s processCrashSnapshotObjects) PutObject(ctx context.Context, bucket, key, path string) error {
	return s.PutObjectIfAbsent(ctx, bucket, key, path)
}

func (s processCrashSnapshotObjects) PutObjectIfAbsent(_ context.Context, bucket, key, path string) error {
	destination := filepath.Join(s.root, bucket, key)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return copyErr
	}
	if s.exitAfterDump && filepath.Ext(key) != ".json" {
		os.Exit(47)
	}
	return nil
}

func (s processCrashSnapshotObjects) GetObject(_ context.Context, bucket, key, path string) error {
	in, err := os.Open(filepath.Join(s.root, bucket, key))
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	return copyErr
}

func TestClaimedSnapshotPublicationRecoversAfterProcessCrash(t *testing.T) {
	const bucket, key = "disposable", "snapshots/app/operations/fixed/export.dump"
	if os.Getenv("NORN_SNAPSHOT_CRASH_CHILD") == "1" {
		root := os.Getenv("NORN_SNAPSHOT_CRASH_ROOT")
		private := os.Getenv("NORN_SNAPSHOT_CRASH_PRIVATE")
		data := []byte("pinned dump bytes")
		digest := sha256.Sum256(data)
		if err := publishClaimedSnapshot(context.Background(), processCrashSnapshotObjects{root: root, exitAfterDump: true}, bucket, key,
			private, filepath.Join(private, "dump"), filepath.Join(private, "manifest.json"), []byte("pinned manifest"), hex.EncodeToString(digest[:]), int64(len(data))); err != nil {
			t.Fatal(err)
		}
		t.Fatal("child returned without crashing after the dump write")
	}
	root := t.TempDir()
	private := t.TempDir()
	data, manifest := []byte("pinned dump bytes"), []byte("pinned manifest")
	if err := os.WriteFile(filepath.Join(private, "dump"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestClaimedSnapshotPublicationRecoversAfterProcessCrash$")
	child.Env = append(os.Environ(), "NORN_SNAPSHOT_CRASH_CHILD=1", "NORN_SNAPSHOT_CRASH_ROOT="+root, "NORN_SNAPSHOT_CRASH_PRIVATE="+private)
	if output, err := child.CombinedOutput(); err == nil {
		t.Fatal("child did not exit during remote publication")
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 47 {
			t.Fatalf("child exit = %v: %s", err, output)
		}
	}
	remoteDump := filepath.Join(root, bucket, key)
	if actual, err := os.ReadFile(remoteDump); err != nil || string(actual) != string(data) {
		t.Fatalf("crashed publication dump = %q, %v", actual, err)
	}
	if _, err := os.Stat(remoteDump + snapshotManifestSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crashed publication unexpectedly committed a manifest: %v", err)
	}
	digest := sha256.Sum256(data)
	retryPrivate := t.TempDir()
	if err := os.WriteFile(remoteDump, []byte("changed dump bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishClaimedSnapshot(context.Background(), processCrashSnapshotObjects{root: root}, bucket, key,
		retryPrivate, filepath.Join(private, "dump"), filepath.Join(private, "manifest.json"), manifest, hex.EncodeToString(digest[:]), int64(len(data))); err == nil {
		t.Fatal("changed remote dump was accepted after process crash")
	}
	if _, err := os.Stat(remoteDump + snapshotManifestSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed dump acquired a completion manifest: %v", err)
	}
	if err := os.WriteFile(remoteDump, data, 0o600); err != nil {
		t.Fatal(err)
	}
	resumePrivate := t.TempDir()
	if err := publishClaimedSnapshot(context.Background(), processCrashSnapshotObjects{root: root}, bucket, key,
		resumePrivate, filepath.Join(private, "dump"), filepath.Join(private, "manifest.json"), manifest, hex.EncodeToString(digest[:]), int64(len(data))); err != nil {
		t.Fatalf("verified continuation after process crash: %v", err)
	}
	if actual, err := os.ReadFile(remoteDump + snapshotManifestSuffix); err != nil || string(actual) != string(manifest) {
		t.Fatalf("continued publication manifest = %q, %v", actual, err)
	}
}
