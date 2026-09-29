package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writeRunnerScript(t *testing.T, directory, body string) string {
	t.Helper()
	path := filepath.Join(directory, "norn-effect-runner")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVerifyRunnerBinaryRequiresPrivatePinnedCompatibleHelper(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	good := writeRunnerScript(t, directory, `[ "$1" = "--protocol" ] && echo `+ProtocolV1+"\n")
	if err := VerifyRunnerBinary(good, ""); err != nil {
		t.Fatalf("compatible runner rejected: %v", err)
	}
	data, _ := os.ReadFile(good)
	digest := sha256.Sum256(data)
	if err := VerifyRunnerBinary(good, hex.EncodeToString(digest[:])); err != nil {
		t.Fatalf("pinned runner rejected: %v", err)
	}
	if err := VerifyRunnerBinary(good, hex.EncodeToString(make([]byte, 32))); err == nil {
		t.Fatal("runner accepted with a different pinned digest")
	}
	if err := VerifyRunnerBinary("norn-effect-runner", ""); err == nil {
		t.Fatal("relative runner path accepted")
	}

	link := filepath.Join(t.TempDir(), "runner-link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRunnerBinary(link, ""); err == nil {
		t.Fatal("symlinked runner accepted")
	}

	incompatible := writeRunnerScript(t, t.TempDir(), "echo norn.effect-runner/v0\n")
	if err := VerifyRunnerBinary(incompatible, ""); err == nil {
		t.Fatal("incompatible runner protocol accepted")
	}
	failing := writeRunnerScript(t, t.TempDir(), "exit 1\n")
	if err := VerifyRunnerBinary(failing, ""); err == nil {
		t.Fatal("runner that fails the handshake accepted")
	}

	writable := writeRunnerScript(t, t.TempDir(), `echo `+ProtocolV1+"\n")
	if err := os.Chmod(writable, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRunnerBinary(writable, ""); err == nil {
		t.Fatal("group-writable runner accepted")
	}
	openDirectory := t.TempDir()
	openRunner := writeRunnerScript(t, openDirectory, `echo `+ProtocolV1+"\n")
	if err := os.Chmod(openDirectory, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRunnerBinary(openRunner, ""); err == nil {
		t.Fatal("runner in a world-writable directory accepted")
	}
}
