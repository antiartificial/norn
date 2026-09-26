package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryPrivateInputFiles(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.WriteFile(private, []byte("secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := readPrivateText(private); err != nil || value != "secret-value" {
		t.Fatalf("owner-only private input = %q, %v", value, err)
	}
	if err := os.Chmod(private, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateText(private); err == nil {
		t.Fatal("world-readable signing material was accepted")
	}
	if err := os.Chmod(private, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "symlink")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateText(link); err == nil {
		t.Fatal("symlinked signing material was accepted")
	}
	if err := os.WriteFile(private, []byte("secret\ninjected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateText(private); err == nil {
		t.Fatal("multiline signing material was accepted")
	}
}

func TestRecoveryRequiresExplicitSelection(t *testing.T) {
	for _, args := range [][]string{nil, {"recover"}, {"recover", "--restore-operation-id", "not-an-id"}, {"unknown"}} {
		if err := run(context.Background(), args, &strings.Builder{}); err == nil || errors.Is(err, context.Canceled) {
			t.Fatalf("incomplete recovery arguments %+v were accepted: %v", args, err)
		}
	}
}
