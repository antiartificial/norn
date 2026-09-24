package supervisor

import (
	"context"
	"errors"
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

func TestDiscardPublishedSnapshotQuarantinesCorruptPrivateArchive(t *testing.T) {
	backend := newBackendFake()
	backend.snapshotQueryErr = &SnapshotArtifactIntegrityError{Cause: errors.New("snapshot artifact does not match signed status")}
	manager := testManager(t, t.TempDir(), backend)
	if err := manager.SetSnapshotArtifactBudget(MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	material := SnapshotLaunchMaterial{PGDumpPath: "/usr/bin/pg_dump", PGDumpSHA256: strings.Repeat("a", 64), ServiceName: "demo", ServiceFile: []byte("[demo]\nhost=localhost\nuser=demo\n"), Password: "secret", Subject: "app:demo/db:main@generation:1", Timeout: time.Minute}
	payload, err := manager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "authority", Resource: "app/demo/app.snapshot", OperationClaim: effect.OperationClaim{OperationID: "snapshot-op", OwnerID: "worker", Generation: 1}, Stage: SnapshotStage, Supervisor: "snapshot-runner", SupervisorExecutionID: "snapshot-corrupt", LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
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
	if err := os.WriteFile(filepath.Join(private, "archive.dump"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"service.conf", "passfile"} {
		if err := os.WriteFile(filepath.Join(private, name), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err = manager.DiscardSnapshotArtifact(context.Background(), reservation, identity)
	var corruption *PublishedSnapshotCorruptionError
	if !errors.As(err, &corruption) {
		t.Fatalf("corruption report = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(private, "archive.dump")); !os.IsNotExist(err) {
		t.Fatalf("corrupt archive remained live: %v", err)
	}
	for _, name := range []string{"service.conf", "passfile"} {
		if _, err := os.Lstat(filepath.Join(private, name)); !os.IsNotExist(err) {
			t.Fatalf("private snapshot credential %s survived cleanup: %v", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(directory, "snapshot-admission")); !os.IsNotExist(err) {
		t.Fatalf("snapshot admission survived cleanup: %v", err)
	}
	if err := manager.withExecutionLock("next", func(string) error { return manager.admitSnapshotArtifact("next") }); err != nil {
		t.Fatalf("corrupt artifact still consumed snapshot budget: %v", err)
	}
}

func TestDiscardPublishedSnapshotDoesNotHideTransientBackendFailure(t *testing.T) {
	backend := newBackendFake()
	backend.snapshotQueryErr = errors.New("snapshot backend temporarily unavailable")
	manager := testManager(t, t.TempDir(), backend)
	if err := manager.SetSnapshotArtifactBudget(MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}
	material := SnapshotLaunchMaterial{PGDumpPath: "/usr/bin/pg_dump", PGDumpSHA256: strings.Repeat("a", 64), ServiceName: "demo", ServiceFile: []byte("[demo]\nhost=localhost\nuser=demo\n"), Password: "secret", Subject: "app:demo/db:main@generation:1", Timeout: time.Minute}
	payload, err := manager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "authority", Resource: "app/demo/app.snapshot", OperationClaim: effect.OperationClaim{OperationID: "snapshot-op", OwnerID: "worker", Generation: 1}, Stage: SnapshotStage, Supervisor: "snapshot-runner", SupervisorExecutionID: "snapshot-filesystem", LaunchPayload: payload}
	reservation.InputDigest, _ = effect.ComputeInputDigest(reservation)
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
	err = manager.DiscardSnapshotArtifact(context.Background(), reservation, identity)
	var corruption *PublishedSnapshotCorruptionError
	if err == nil || errors.As(err, &corruption) {
		t.Fatalf("transient backend failure was hidden: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(private, "archive.dump")); err != nil {
		t.Fatalf("archive changed after transient backend failure: %v", err)
	}
}
