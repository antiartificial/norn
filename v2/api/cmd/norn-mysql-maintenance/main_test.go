package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/database"
	"norn/v2/api/store"
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

func TestRetainedSourceReplayCleansOnlyVerifiedLocalStage(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "source.sql")
	content := []byte("-- private SQL snapshot\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	emptyDigest := fmt.Sprintf("%x", sha256.Sum256(nil))
	stage := store.SignedMySQLSourceArtifactReceipt{SHA256: "stage-digest", Receipt: store.MySQLSourceArtifactReceipt{
		OperationID: "source-op", ArtifactPath: path,
		Artifact: database.MySQLSQLArtifact{Format: database.MySQLSQLArtifactV2,
			Source:      database.TargetIdentity{ServiceID: "mysql", ServiceGeneration: 1, BindingID: "source", BindingGeneration: 1, Engine: database.EngineMySQL, Database: "source", Role: "source"},
			Expectation: database.MySQLRestoreExpectation{SchemaSHA256: emptyDigest, DataSHA256: emptyDigest},
			Bytes:       int64(len(content)), SHA256: digest},
	}}
	retained := store.SignedMySQLSourceArtifactRetentionReceipt{Receipt: store.MySQLSourceArtifactRetentionReceipt{OperationID: "source-op", StagingReceiptSHA256: stage.SHA256}}
	wrong := retained
	wrong.Receipt.StagingReceiptSHA256 = "other-stage"
	if err := cleanupRetainedSourceStage(directory, stage, wrong); err == nil {
		t.Fatal("unrelated retention receipt cleaned the local SQL")
	}
	if err := os.WriteFile(path, []byte("modified SQL snapshot bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupRetainedSourceStage(directory, stage, retained); err == nil {
		t.Fatal("cleanup removed SQL that differs from the signed artifact")
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupRetainedSourceStage(directory, stage, retained); err != nil {
		t.Fatalf("cleanup verified stage: %v", err)
	}
	if err := cleanupRetainedSourceStage(directory, stage, retained); err != nil {
		t.Fatalf("terminal replay cleanup is not idempotent: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local SQL survived cleanup: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "other.sql")
	stage.Receipt.ArtifactPath = outside
	if err := cleanupRetainedSourceStage(directory, stage, retained); err == nil {
		t.Fatal("cleanup accepted a path outside the private stage directory")
	}
}

func TestRecoveryRequiresExplicitSelection(t *testing.T) {
	for _, args := range [][]string{nil, {"recover"}, {"recover", "--restore-operation-id", "not-an-id"}, {"restore"}, {"restore", "--restore-operation-id", "not-an-id"}, {"admit-restore"}, {"source"}, {"source", "--selection-file", "missing"}, {"inspect-source"}, {"inspect-source", "--source-operation-id", "not-an-id"}, {"unknown"}} {
		if err := run(context.Background(), args, &strings.Builder{}); err == nil || errors.Is(err, context.Canceled) {
			t.Fatalf("incomplete recovery arguments %+v were accepted: %v", args, err)
		}
	}
}

func TestRecoveryRejectsCombinedAcceptAndInspection(t *testing.T) {
	err := run(context.Background(), []string{"recover", "--accept-only", "--inspect-only"}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "separate actions") {
		t.Fatalf("combined recovery modes were accepted: %v", err)
	}
}

func TestRestorePrivateDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(root); err != nil {
		t.Fatalf("owner-only directory rejected: %v", err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(root); err == nil {
		t.Fatal("public materialization directory accepted")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(link); err == nil {
		t.Fatal("symlinked materialization directory accepted")
	}
}
