package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestReadPrivateBytesUsesOnePrivateRegularDescriptor(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "private")
	if err := os.WriteFile(privatePath, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPrivateBytes(privatePath)
	if err != nil || string(got) != "secret" {
		t.Fatalf("read private file = %q, %v", got, err)
	}
	symlinkPath := filepath.Join(directory, "private-link")
	if err := os.Symlink(privatePath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateBytes(symlinkPath); err == nil {
		t.Fatal("private input symlink was accepted")
	}
	if err := os.Chmod(privatePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateBytes(privatePath); err == nil {
		t.Fatal("group/world-readable private input was accepted")
	}
}

func TestReadLinesReportsExplicitFileFailure(t *testing.T) {
	if _, err := readLines(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing requested key ID file was silently ignored")
	}
}

func TestWriteNewAtomicPublishesOnlyCompleteNewFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "inspection.json")
	writeFailure := errors.New("fixture failure")
	if err := writeNewAtomic(path, func(writer io.Writer) error {
		_, _ = writer.Write([]byte("partial-secret"))
		return writeFailure
	}); !errors.Is(err, writeFailure) {
		t.Fatalf("write failure = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("partial inspection was published")
	}
	if err := writeNewAtomic(path, func(writer io.Writer) error {
		_, err := writer.Write([]byte("complete"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeNewAtomic(path, func(writer io.Writer) error {
		_, err := writer.Write([]byte("replacement"))
		return err
	}); err == nil {
		t.Fatal("existing inspection destination was replaced")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "complete" {
		t.Fatalf("published inspection = %q, %v", got, err)
	}
}
