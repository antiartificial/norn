package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateFleetDeployFileRejectsSharedAndLinkedMaterial(t *testing.T) {
	directory := t.TempDir()
	private := filepath.Join(directory, "private.json")
	if err := os.WriteFile(private, []byte(`{"bind":"127.0.0.1:18082"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := privateFleetDeployFile(private); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(private, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := privateFleetDeployFile(private); err == nil {
		t.Fatal("group-readable deploy material was accepted")
	}
	if err := os.Chmod(private, 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(directory, "linked.json")
	if err := os.Symlink(private, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := privateFleetDeployFile(linked); err == nil {
		t.Fatal("linked deploy material was accepted")
	}
}
