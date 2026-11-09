package secrets

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotEncryptedReturnsExactCiphertextAndDigest(t *testing.T) {
	apps := t.TempDir()
	appDir := filepath.Join(apps, "demo")
	if err := os.Mkdir(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	want := []byte("opaque-sops-ciphertext")
	path := filepath.Join(appDir, "secrets.enc.yaml")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := NewManager(apps).SnapshotEncrypted("demo")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(want)
	if snapshot.Digest != "sha256:"+hex.EncodeToString(sum[:]) || string(snapshot.Ciphertext) != string(want) {
		t.Fatalf("snapshot digest=%q ciphertext=%q", snapshot.Digest, snapshot.Ciphertext)
	}
	snapshot.Ciphertext[0] ^= 0xff
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != string(want) {
		t.Fatalf("mutating returned snapshot changed source: %q, %v", stored, err)
	}
}

func TestSnapshotEncryptedRejectsUnsafeInputs(t *testing.T) {
	apps := t.TempDir()
	appDir := filepath.Join(apps, "demo")
	if err := os.Mkdir(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(appDir, "secrets.enc.yaml")
	write := func(mode os.FileMode, body []byte) {
		t.Helper()
		if err := os.WriteFile(path, body, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(apps)

	for _, appID := range []string{"../demo", "Demo", ""} {
		if _, err := manager.SnapshotEncrypted(appID); err == nil {
			t.Errorf("accepted invalid app ID %q", appID)
		}
	}
	if err := os.Chmod(apps, 0770); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SnapshotEncrypted("demo"); err == nil {
		t.Fatal("accepted group-writable app root")
	}
	if err := os.Chmod(apps, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(appDir, 0770); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SnapshotEncrypted("demo"); err == nil {
		t.Fatal("accepted group-writable app directory")
	}
	if err := os.Chmod(appDir, 0700); err != nil {
		t.Fatal(err)
	}

	write(0644, []byte("private data"))
	if _, err := manager.SnapshotEncrypted("demo"); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("accepted group/world-readable file: %v", err)
	}

	write(0600, []byte("private data"))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(appDir, "missing-target"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SnapshotEncrypted("demo"); err == nil {
		t.Fatal("accepted symlink secret file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	write(0600, []byte(""))
	if _, err := manager.SnapshotEncrypted("demo"); err == nil {
		t.Fatal("accepted empty secret file")
	}
	write(0600, make([]byte, maxEncryptedSnapshotBytes+1))
	if _, err := manager.SnapshotEncrypted("demo"); err == nil {
		t.Fatal("accepted oversized secret file")
	}

	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "secrets.enc.yaml"), []byte("outside-root"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(appDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, appDir); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SnapshotEncrypted("demo"); err == nil {
		t.Fatal("accepted app directory symlink")
	}
}

func TestWriteEncryptedSecretAtomicallyReplacesOwnerOnlyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc.yaml")
	if err := os.WriteFile(path, []byte("old-ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	want := []byte("new-ciphertext")
	if err := writeEncryptedSecretAtomically(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("replacement = %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("replacement mode = %v", info.Mode().Perm())
	}
}

func TestEncryptPassesPlaintextToSOPSOverStdin(t *testing.T) {
	root := t.TempDir()
	apps := filepath.Join(root, "apps")
	appDir := filepath.Join(apps, "demo")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{apps, appDir, binDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	argsPath := filepath.Join(root, "sops-args")
	stdinPath := filepath.Join(root, "sops-stdin")
	sopsPath := filepath.Join(binDir, "sops")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$NORN_TEST_SOPS_ARGS\"\ncat > \"$NORN_TEST_SOPS_STDIN\"\nprintf 'encrypted-fixture'\n"
	if err := os.WriteFile(sopsPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, ".sops.yaml"), []byte("creation_rules: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NORN_TEST_SOPS_ARGS", argsPath)
	t.Setenv("NORN_TEST_SOPS_STDIN", stdinPath)

	if err := NewManager(apps).encrypt("demo", map[string]string{"TOKEN": "test-only-plaintext"}); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.ReadFile(stdinPath)
	if err != nil || !strings.Contains(string(stdin), "test-only-plaintext") {
		t.Fatalf("SOPS stdin = %q, %v", stdin, err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil || !strings.Contains(string(args), "/dev/stdin") {
		t.Fatalf("SOPS args = %q, %v", args, err)
	}
	if strings.Contains(string(args), "test-only-plaintext") {
		t.Fatal("plaintext appeared in SOPS arguments")
	}
	entries, err := os.ReadDir(appDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != ".sops.yaml" || entries[1].Name() != "secrets.enc.yaml" {
		t.Fatalf("app directory contains unexpected files: %v", entries)
	}
}
