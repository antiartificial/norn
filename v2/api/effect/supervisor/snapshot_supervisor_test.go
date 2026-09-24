package supervisor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestSnapshotTerminalResultReusesFirstAuthenticatedManifestBeforeCompletion(t *testing.T) {
	manager := testManager(t, t.TempDir(), newBackendFake())
	material := SnapshotLaunchMaterial{PGDumpPath: "/usr/bin/pg_dump", PGDumpSHA256: strings.Repeat("a", 64), ServiceName: "demo", ServiceFile: []byte("[demo]\nhost=localhost\nuser=demo\n"), Password: "secret", Subject: "app:demo/db:main@generation:1", Timeout: time.Minute}
	payload, err := manager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor SnapshotDescriptor
	if err := json.Unmarshal(payload, &descriptor); err != nil {
		t.Fatal(err)
	}
	record := journal{InputDigest: "sha256:" + strings.Repeat("b", 64), SupervisorExecutionID: "snapshot-pre-completion", RuntimeInstanceID: "snapshot-runtime"}
	manifest := func(observedAt time.Time) []byte {
		m := SnapshotManifest{Protocol: SnapshotProtocolV1, RuntimeInstanceID: record.RuntimeInstanceID, DescriptorSHA256: mustSnapshotDescriptorDigest(t, descriptor), Artifact: SnapshotArtifact{Reference: "snapshot/" + record.SupervisorExecutionID, Bytes: 7, SHA256: strings.Repeat("c", 64), Regular: true, NoFollow: true}, ContainmentProven: true, ObservedAt: observedAt}
		encoded, _ := json.Marshal(snapshotManifestPayload{m.Protocol, m.RuntimeInstanceID, m.DescriptorSHA256, m.Artifact, m.ContainmentProven, m.ObservedAt})
		mac := hmac.New(sha256.New, runnerStatusKey(testSigningKey, record.RuntimeInstanceID))
		mac.Write(encoded)
		m.MAC = hex.EncodeToString(mac.Sum(nil))
		output, _ := json.Marshal(m)
		return output
	}
	first := manifest(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))
	second := manifest(time.Date(2026, 9, 24, 12, 0, 1, 0, time.UTC))
	if string(first) == string(second) {
		t.Fatal("test manifests must differ by observed timestamp")
	}
	exit := 0
	firstObservation, err := manager.observation(record, BackendState{Phase: effect.SupervisorSucceeded, ExitCode: &exit, Output: first, ContainmentProven: true, EvidenceReference: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.writeSnapshotResult(manager.root, record, descriptor, firstObservation); err != nil {
		t.Fatal(err)
	}
	secondObservation, err := manager.observation(record, BackendState{Phase: effect.SupervisorSucceeded, ExitCode: &exit, Output: second, ContainmentProven: true, EvidenceReference: "second"})
	if err != nil {
		t.Fatal(err)
	}
	stable, err := manager.writeSnapshotResult(manager.root, record, descriptor, secondObservation)
	if err != nil || string(stable.Output) != string(first) {
		t.Fatalf("interrupted pre-completion replay = %+v, %v", stable, err)
	}
}

func TestSnapshotObserveReplaysSignedTerminalEvidenceAfterRebootBeforeCompletion(t *testing.T) {
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
	var descriptor SnapshotDescriptor
	if err := json.Unmarshal(payload, &descriptor); err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "authority", Resource: "app/demo/snapshot", OperationClaim: effect.OperationClaim{OperationID: "reboot-before-completion", OwnerID: "worker", Generation: 1}, Stage: SnapshotStage, Supervisor: "snapshot-runner", SupervisorExecutionID: "snapshot-reboot-before-completion", LaunchPayload: payload}
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
	digest := mustSnapshotDescriptorDigest(t, descriptor)
	manifest := SnapshotManifest{Protocol: SnapshotProtocolV1, RuntimeInstanceID: identity.RuntimeInstanceID, DescriptorSHA256: digest, Artifact: SnapshotArtifact{Reference: "snapshot/" + reservation.SupervisorExecutionID, Bytes: 7, SHA256: strings.Repeat("c", 64), Regular: true, NoFollow: true}, ContainmentProven: true, ObservedAt: time.Now().UTC()}
	encoded, _ := json.Marshal(snapshotManifestPayload{manifest.Protocol, manifest.RuntimeInstanceID, manifest.DescriptorSHA256, manifest.Artifact, manifest.ContainmentProven, manifest.ObservedAt})
	mac := hmac.New(sha256.New, runnerStatusKey(testSigningKey, identity.RuntimeInstanceID))
	mac.Write(encoded)
	manifest.MAC = hex.EncodeToString(mac.Sum(nil))
	output, _ := json.Marshal(manifest)
	exit := 0
	backend.mu.Lock()
	backend.states[reservation.SupervisorExecutionID] = BackendState{Phase: effect.SupervisorSucceeded, ExitCode: &exit, Output: output, ContainmentProven: true, EvidenceReference: "contained/" + identity.RuntimeInstanceID}
	backend.mu.Unlock()
	first, err := manager.ObserveSnapshot(context.Background(), reservation, identity)
	if err != nil || first.Phase != effect.SupervisorSucceeded {
		t.Fatalf("first terminal observation = %+v, %v", first, err)
	}
	// Completion has not been stored. A reboot loses cgroup containment, so
	// only the manager-signed terminal observation may carry this forward.
	backend.mu.Lock()
	backend.states[reservation.SupervisorExecutionID] = BackendState{Phase: effect.SupervisorUnknown, EvidenceReference: "rebooted"}
	backend.mu.Unlock()
	replayed, err := manager.ObserveSnapshot(context.Background(), reservation, identity)
	if err != nil || replayed.Phase != effect.SupervisorSucceeded || string(replayed.Output) != string(first.Output) || string(replayed.Evidence.Payload) != string(first.Evidence.Payload) {
		t.Fatalf("reboot observation = %+v, %v", replayed, err)
	}
}

func mustSnapshotDescriptorDigest(t *testing.T, descriptor SnapshotDescriptor) string {
	t.Helper()
	digest, err := snapshotDescriptorDigest(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	return digest
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

func TestFailedSnapshotReleasesAdmissionButLaunchAmbiguityRetainsIt(t *testing.T) {
	makeReservation := func(t *testing.T, manager *Manager, executionID string) (effect.Reservation, SnapshotLaunchMaterial) {
		t.Helper()
		material := SnapshotLaunchMaterial{PGDumpPath: "/usr/bin/pg_dump", PGDumpSHA256: strings.Repeat("a", 64), ServiceName: "demo", ServiceFile: []byte("[demo]\nhost=localhost\nuser=demo\n"), Password: "secret", Subject: "app:demo/db:main@generation:1", Timeout: time.Minute}
		payload, err := manager.BuildSnapshotDescriptor(material)
		if err != nil {
			t.Fatal(err)
		}
		reservation := effect.Reservation{Authority: "authority", Resource: "app/demo/app.snapshot", OperationClaim: effect.OperationClaim{OperationID: executionID, OwnerID: "worker", Generation: 1}, Stage: SnapshotStage, Supervisor: "snapshot-runner", SupervisorExecutionID: executionID, LaunchPayload: payload}
		reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Prepare(context.Background(), reservation); err != nil {
			t.Fatal(err)
		}
		return reservation, material
	}
	t.Run("verified failure releases the full admission", func(t *testing.T) {
		backend := newBackendFake()
		manager := testManager(t, t.TempDir(), backend)
		if err := manager.SetSnapshotArtifactBudget(MaxSnapshotArtifactBytes); err != nil {
			t.Fatal(err)
		}
		reservation, material := makeReservation(t, manager, "snapshot-failed")
		identity, err := manager.LaunchSnapshot(context.Background(), reservation, material)
		if err != nil {
			t.Fatal(err)
		}
		backend.mu.Lock()
		code := 1
		backend.states[reservation.SupervisorExecutionID] = BackendState{Phase: effect.SupervisorFailed, ExitCode: &code, ContainmentProven: true, EvidenceReference: "failed/" + identity.RuntimeInstanceID}
		backend.mu.Unlock()
		if err := manager.DiscardFailedSnapshotArtifact(context.Background(), reservation, identity); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(manager.root, sha256DirectoryName(reservation.SupervisorExecutionID))
		if _, err := os.Lstat(filepath.Join(directory, "snapshot-admission")); !os.IsNotExist(err) {
			t.Fatalf("failed snapshot admission remained: %v", err)
		}
		if err := manager.withExecutionLock("next", func(string) error { return manager.admitSnapshotArtifact("next") }); err != nil {
			t.Fatalf("released capacity did not admit retry: %v", err)
		}
	})
	t.Run("launch ambiguity holds the admission", func(t *testing.T) {
		backend := newBackendFake()
		backend.snapshotStartErr = errors.New("backend launch acknowledgement lost")
		manager := testManager(t, t.TempDir(), backend)
		if err := manager.SetSnapshotArtifactBudget(MaxSnapshotArtifactBytes); err != nil {
			t.Fatal(err)
		}
		reservation, material := makeReservation(t, manager, "snapshot-ambiguous")
		if _, err := manager.LaunchSnapshot(context.Background(), reservation, material); err == nil {
			t.Fatal("ambiguous launch unexpectedly succeeded")
		}
		directory := filepath.Join(manager.root, sha256DirectoryName(reservation.SupervisorExecutionID))
		if _, err := os.Lstat(filepath.Join(directory, "snapshot-admission")); err != nil {
			t.Fatalf("launch ambiguity released admission: %v", err)
		}
		if err := manager.withExecutionLock("next", func(string) error { return manager.admitSnapshotArtifact("next") }); err == nil {
			t.Fatal("launch ambiguity admitted a second full-sized snapshot")
		}
	})
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
