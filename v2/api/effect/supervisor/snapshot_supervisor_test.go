package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
)

func TestManagerLaunchSnapshotUsesPrivateMaterialAndStableDescriptor(t *testing.T) {
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	if err := manager.SetSnapshotArtifactBudget(MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	material := SnapshotLaunchMaterial{PGDumpPath: "/usr/bin/pg_dump", PGDumpSHA256: strings.Repeat("a", 64), ServiceName: "demo", ServiceFile: []byte("[demo]\nhost=localhost\nuser=demo\n"), Password: "rotated-secret", Subject: "app:demo/db:main@generation:1", Timeout: time.Minute}
	payload, err := manager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), material.Password) || strings.Contains(string(payload), "localhost") {
		t.Fatalf("descriptor leaked private material: %s", payload)
	}
	r := effect.Reservation{Authority: "authority", Resource: "app/demo/app.snapshot", OperationClaim: effect.OperationClaim{OperationID: "snapshot-op", OwnerID: "worker", Generation: 1}, Stage: SnapshotStage, Supervisor: "snapshot-runner", LaunchPayload: payload}
	r.InputDigest, err = effect.ComputeInputDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	r.SupervisorExecutionID = "snapshot-execution"
	if err := manager.Prepare(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	identity, err := manager.LaunchSnapshot(context.Background(), r, material)
	if err != nil || identity.RuntimeInstanceID == "" {
		t.Fatalf("launch = %+v, %v", identity, err)
	}
	rotated := material
	rotated.Password = "new-secret"
	if _, err := manager.LaunchSnapshot(context.Background(), r, rotated); err != nil {
		t.Fatalf("rotated credential could not recover launch identity: %v", err)
	}
	backend.mu.Lock()
	starts := backend.starts
	backend.mu.Unlock()
	if starts != 1 {
		t.Fatalf("snapshot launched %d times", starts)
	}
}

func TestSnapshotAdmissionAccountsForPrivateArtifacts(t *testing.T) {
	manager := testManager(t, t.TempDir(), newBackendFake())
	if err := manager.SetSnapshotArtifactBudget(MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(manager.root, "retained", ".snapshot-retained")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "archive.dump"), []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.withExecutionLock("next", func(string) error { return manager.admitSnapshotArtifact("next") }); err == nil {
		t.Fatal("admission accepted a bounded dump beyond the configured budget")
	}
	if err := os.Remove(filepath.Join(private, "archive.dump")); err != nil {
		t.Fatal(err)
	}
	if err := manager.withExecutionLock("next", func(string) error { return manager.admitSnapshotArtifact("next") }); err != nil {
		t.Fatalf("admission did not release removed private artifact capacity: %v", err)
	}
}

func TestDiscardSnapshotArtifactIsIdempotentAfterDurablePublication(t *testing.T) {
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	if err := manager.SetSnapshotArtifactBudget(MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	material := SnapshotLaunchMaterial{PGDumpPath: "/usr/bin/pg_dump", PGDumpSHA256: strings.Repeat("a", 64), ServiceName: "demo", ServiceFile: []byte("[demo]\nhost=localhost\nuser=demo\n"), Password: "secret", Subject: "app:demo/db:main@generation:1", Timeout: time.Minute}
	payload, err := manager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "authority", Resource: "app/demo/app.snapshot", OperationClaim: effect.OperationClaim{OperationID: "snapshot-op", OwnerID: "worker", Generation: 1}, Stage: SnapshotStage, Supervisor: "snapshot-runner", SupervisorExecutionID: "snapshot-execution", LaunchPayload: payload}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	identity, err := manager.LaunchSnapshot(context.Background(), reservation, material)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(manager.root, sha256DirectoryName(reservation.SupervisorExecutionID))
	private := filepath.Join(directory, ".snapshot-published")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(private, "archive.dump")
	if err := os.WriteFile(archive, []byte("published"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.DiscardSnapshotArtifact(context.Background(), reservation, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(archive); !os.IsNotExist(err) {
		t.Fatalf("private archive remained after cleanup: %v", err)
	}
	if err := manager.DiscardSnapshotArtifact(context.Background(), reservation, identity); err != nil {
		t.Fatalf("reconciled cleanup was not idempotent: %v", err)
	}
}

func TestDiscardSnapshotArtifactReconcilesAdmissionAfterArchiveRemovalCrash(t *testing.T) {
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	if err := manager.SetSnapshotArtifactBudget(MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	material := SnapshotLaunchMaterial{PGDumpPath: "/usr/bin/pg_dump", PGDumpSHA256: strings.Repeat("a", 64), ServiceName: "demo", ServiceFile: []byte("[demo]\nhost=localhost\nuser=demo\n"), Password: "secret", Subject: "app:demo/db:main@generation:1", Timeout: time.Minute}
	payload, err := manager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "authority", Resource: "app/demo/app.snapshot", OperationClaim: effect.OperationClaim{OperationID: "snapshot-op", OwnerID: "worker", Generation: 1}, Stage: SnapshotStage, Supervisor: "snapshot-runner", SupervisorExecutionID: "snapshot-crash-window", LaunchPayload: payload}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	identity, err := manager.LaunchSnapshot(context.Background(), reservation, material)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(manager.root, sha256DirectoryName(reservation.SupervisorExecutionID))
	private := filepath.Join(directory, ".snapshot-published")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "archive.dump"), []byte("published"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Model a crash after archive removal but before admission-marker cleanup.
	if err := os.Remove(filepath.Join(private, "archive.dump")); err != nil {
		t.Fatal(err)
	}
	if err := manager.DiscardSnapshotArtifact(context.Background(), reservation, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "snapshot-admission")); !os.IsNotExist(err) {
		t.Fatalf("admission marker remained after crash recovery: %v", err)
	}
}
