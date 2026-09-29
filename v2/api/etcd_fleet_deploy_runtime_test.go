package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
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

func TestEtcdFleetDeployWorkerRejectsInvalidPrivateConfigBeforeStoreAccess(t *testing.T) {
	cfg := &config.Config{AppsDir: t.TempDir(), ControlAuthority: "test-authority", DatabaseProfile: "fleet", DatabaseSecretDir: t.TempDir()}
	for _, tt := range []struct {
		name, document, reason string
	}{
		{"trailing JSON", `{} {}`, "trailing content"},
		{"public bind", `{"bind":"0.0.0.0:18082","nodeUris":{"spiffe://test/ingress/one":"one"},"observerPort":18083,"endpointPort":8080}`, "private IP"},
		{"invalid node identity", `{"bind":"127.0.0.1:18082","nodeUris":{"bad-uri":"one"},"observerPort":18083,"publisherPort":18084,"endpointPort":8080}`, "node identity"},
		{"missing publisher port", `{"bind":"127.0.0.1:18084","nodeUris":{"spiffe://test/ingress/one":"one"},"observerPort":18082,"endpointPort":8080}`, "transport is incomplete"},
		{"shared publisher port", `{"bind":"127.0.0.1:18084","nodeUris":{"spiffe://test/ingress/one":"one"},"observerPort":18082,"publisherPort":18082,"endpointPort":8080}`, "transport is incomplete"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "worker.json")
			if err := os.WriteFile(path, []byte(tt.document), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := newEtcdFleetDeployRuntime(context.Background(), cfg, &etcdstore.V3OperationStore{}, path)
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("config error=%v, want %q", err, tt.reason)
			}
		})
	}
}
