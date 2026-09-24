package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const snapshotSecretCanary = "NORN_SNAPSHOT_PASSWORD_CANARY_5d21"

func snapshotHelperRequest(t *testing.T, directory string) (snapshotRunnerRequest, []byte) {
	t.Helper()
	pgDump := filepath.Join(t.TempDir(), "pg_dump")
	script := strings.Join([]string{
		"#!/bin/sh", "set -eu", "out=''", "while [ \"$#\" -gt 0 ]; do", "  if [ \"$1\" = \"--file\" ]; then out=\"$2\"; shift 2; continue; fi", "  shift", "done", "[ -n \"$out\" ]", "printf 'pg-dump-artifact' > \"$out\"", "printf 'diagnostic only' >&2", "",
	}, "\n")
	if err := os.WriteFile(pgDump, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(pgDump)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	runtimeID := "snapshot-runtime-" + filepath.Base(directory)
	key := runnerStatusKey(testSigningKey, runtimeID)
	material := SnapshotLaunchMaterial{PGDumpPath: pgDump, PGDumpSHA256: hex.EncodeToString(digest[:]), ServiceName: "norn_snapshot", ServiceFile: []byte("[norn_snapshot]\nhost=localhost\nuser=snapshot\ndbname=norn\n"), Password: snapshotSecretCanary, Subject: "app:demo/database:main@generation:7", Timeout: time.Minute}
	manager, err := NewManager(t.TempDir(), testSigningKey, &backendFake{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := manager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor SnapshotDescriptor
	if err := json.Unmarshal(encoded, &descriptor); err != nil {
		t.Fatal(err)
	}
	return snapshotRunnerRequest{
		Protocol: SnapshotProtocolV1, Execution: BackendExecution{SupervisorExecutionID: "snapshot-execution-" + filepath.Base(directory), RuntimeInstanceID: runtimeID, StateDirectory: directory}, Descriptor: descriptor, Material: material, StatusKey: hex.EncodeToString(key),
	}, key
}

func runSnapshotRequest(t *testing.T, request snapshotRunnerRequest) error {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return RunHelper(strings.NewReader(string(data)))
}

func TestSnapshotRunnerProducesAttestedPrivateArtifactOnlyAfterContainment(t *testing.T) {
	directory := t.TempDir()
	request, key := snapshotHelperRequest(t, directory)
	if err := runSnapshotRequest(t, request); err != nil {
		t.Fatal(err)
	}
	status, err := readSnapshotStatus(directory, key, request.Execution.RuntimeInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Artifact == nil || status.Artifact.Bytes != int64(len("pg-dump-artifact")) || status.Artifact.Reference != "snapshot/"+request.Execution.SupervisorExecutionID || status.DiagnosticBytes == 0 {
		t.Fatalf("status = %+v", status)
	}
	if _, err := ReadSnapshotManifest(directory, key, request.Execution.RuntimeInstanceID, false); err == nil {
		t.Fatal("snapshot manifest was issued without containment")
	}
	manifest, err := ReadSnapshotManifest(directory, key, request.Execution.RuntimeInstanceID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.ContainmentProven || !manifest.Artifact.Regular || !manifest.Artifact.NoFollow || manifest.MAC == "" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if err := VerifySnapshotManifest(manifest, key, request.Execution.RuntimeInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := VerifySnapshotManifestForDescriptor(manifest, request.Descriptor, key, request.Execution.RuntimeInstanceID); err != nil {
		t.Fatal(err)
	}
	otherManager, err := NewManager(t.TempDir(), testSigningKey, &backendFake{})
	if err != nil {
		t.Fatal(err)
	}
	otherMaterial := request.Material
	otherMaterial.Subject = "app:other/database:main@generation:7"
	otherEncoded, err := otherManager.BuildSnapshotDescriptor(otherMaterial)
	if err != nil {
		t.Fatal(err)
	}
	var otherDescriptor SnapshotDescriptor
	if err := json.Unmarshal(otherEncoded, &otherDescriptor); err != nil {
		t.Fatal(err)
	}
	if err := VerifySnapshotManifestForDescriptor(manifest, otherDescriptor, key, request.Execution.RuntimeInstanceID); err == nil {
		t.Fatal("cross-target manifest substitution was accepted")
	}
	forged := manifest
	forged.Artifact.Bytes++
	if err := VerifySnapshotManifest(forged, key, request.Execution.RuntimeInstanceID); err == nil {
		t.Fatal("forged manifest was accepted")
	}
	private := snapshotPrivateDir(t, directory)
	for _, name := range []string{"service.conf", "passfile"} {
		if _, err := os.Lstat(filepath.Join(private, name)); !os.IsNotExist(err) {
			t.Fatalf("private connection file %s remained after pg_dump: %v", name, err)
		}
	}
	if err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(data), snapshotSecretCanary) {
				return os.ErrPermission
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("private request material was retained on disk: %v", err)
	}
	encoded, _ := json.Marshal(manifest)
	if strings.Contains(string(encoded), snapshotSecretCanary) || strings.Contains(string(encoded), "localhost") {
		t.Fatalf("manifest leaked launch material: %s", encoded)
	}
}

func TestSnapshotRunnerExecutesTheVerifiedDescriptorAfterPathReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "pg_dump")
	old := []byte("#!/bin/sh\nprintf old\n")
	if err := os.WriteFile(path, old, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(old)
	binary, err := openVerifiedSnapshotPGDump(path, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	defer binary.Close()
	replacement := filepath.Join(directory, "replacement")
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\nprintf replacement\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	command := verifiedSnapshotCommand(binary)
	command.ExtraFiles = []*os.File{binary}
	output, err := command.Output()
	if err != nil || string(output) != "old" {
		t.Fatalf("verified descriptor output = %q, %v", output, err)
	}
}

func TestSnapshotDescriptorIsStableAndSecretFree(t *testing.T) {
	manager, err := NewManager(t.TempDir(), testSigningKey, &backendFake{})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := snapshotHelperRequest(t, t.TempDir())
	descriptor, err := manager.BuildSnapshotDescriptor(request.Material)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(descriptor), snapshotSecretCanary) || strings.Contains(string(descriptor), "localhost") || strings.Contains(string(descriptor), "service.conf") {
		t.Fatalf("descriptor leaked secret material: %s", descriptor)
	}
	if _, err := manager.verifySnapshotDescriptor(descriptor, &request.Material); err != nil {
		t.Fatal(err)
	}
	rotated := request.Material
	rotated.Password = "different"
	if _, err := manager.verifySnapshotDescriptor(descriptor, &rotated); err != nil {
		t.Fatalf("password rotation blocked recovery: %v", err)
	}
	tampered := request.Material
	tampered.ServiceFile = append(append([]byte(nil), request.Material.ServiceFile...), []byte("port=55432\n")...)
	if _, err := manager.verifySnapshotDescriptor(descriptor, &tampered); err == nil {
		t.Fatal("descriptor accepted changed service material")
	}
}

func TestSnapshotRunnerFailsClosedForCrashTamperAndForeignArtifact(t *testing.T) {
	t.Run("crash before signed terminal status", func(t *testing.T) {
		directory := t.TempDir()
		request, key := snapshotHelperRequest(t, directory)
		beforeSnapshotTerminalStatus = func() error { return os.ErrClosed }
		t.Cleanup(func() { beforeSnapshotTerminalStatus = nil })
		if err := runSnapshotRequest(t, request); err == nil {
			t.Fatal("interrupted runner reported success")
		}
		if _, err := ReadSnapshotManifest(directory, key, request.Execution.RuntimeInstanceID, true); err == nil {
			t.Fatal("crashed runner artifact was accepted")
		}
	})
	t.Run("tampered artifact", func(t *testing.T) {
		directory := t.TempDir()
		request, key := snapshotHelperRequest(t, directory)
		if err := runSnapshotRequest(t, request); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(snapshotPrivateDir(t, directory), "archive.dump"), []byte("tampered"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSnapshotManifest(directory, key, request.Execution.RuntimeInstanceID, true); err == nil {
			t.Fatal("tampered artifact was accepted")
		}
	})
	t.Run("foreign precreated artifact", func(t *testing.T) {
		directory := t.TempDir()
		request, _ := snapshotHelperRequest(t, directory)
		beforeSnapshotArtifactReservation = func(path string) error { return os.WriteFile(path, []byte("foreign"), 0o600) }
		t.Cleanup(func() { beforeSnapshotArtifactReservation = nil })
		if err := runSnapshotRequest(t, request); err == nil {
			t.Fatal("foreign artifact was accepted")
		}
		if _, err := os.Stat(filepath.Join(directory, "snapshot-status.json")); err != nil {
			t.Fatal("runner should retain only its non-terminal running status")
		}
	})
}

func snapshotPrivateDir(t *testing.T, directory string) string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".snapshot-") {
			return filepath.Join(directory, entry.Name())
		}
	}
	t.Fatal("snapshot private directory not found")
	return ""
}
