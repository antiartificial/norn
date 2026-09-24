//go:build linux

package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
)

// TestLinuxCgroupSnapshotRunner is deliberately opt-in. The companion shell
// harness runs it in a disposable privileged postgres:16 container.
func TestLinuxCgroupSnapshotRunner(t *testing.T) {
	if os.Getenv("NORN_REAL_CGROUP_TEST") != "1" {
		t.Skip("set NORN_REAL_CGROUP_TEST=1 in the privileged Linux container harness")
	}
	runner := requireExecutable(t, "NORN_EFFECT_RUNNER_BINARY")
	pgDump := requireExecutable(t, "NORN_TEST_PG_DUMP")
	service := os.Getenv("NORN_TEST_PG_SERVICE")
	if service == "" {
		t.Fatal("NORN_TEST_PG_SERVICE is required")
	}
	root := filepath.Join("/sys/fs/cgroup", "norn-snapshot-integration-"+strings.ReplaceAll(t.Name(), "/", "-"))
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create delegated cgroup root %s: %v", root, err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(root, "cgroup.kill"), []byte("1\n"), 0o200)
		_ = os.Remove(root)
	})
	key := bytes.Repeat([]byte("snapshot-cgroup-key-"), 2)
	backend, err := NewCgroupBackend(root, runner, fileSHA256(t, runner), key)
	if err != nil {
		t.Fatalf("construct real cgroup-v2 backend: %v", err)
	}
	cgroup, ok := backend.(*cgroupBackend)
	if !ok {
		t.Fatalf("backend type = %T, want cgroup backend", backend)
	}
	manager, err := NewManager(t.TempDir(), key, backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetSnapshotArtifactBudget(2 * MaxSnapshotArtifactBytes); err != nil {
		t.Fatal(err)
	}

	// A real image-provided and checksum-pinned pg_dump produces an artifact.
	// Success is accepted only after cgroup containment is empty, and the
	// helper must remove private connection files before terminal publication.
	success := launchLinuxSnapshot(t, manager, pgDump, fileSHA256(t, pgDump), service, "real-pg-dump")
	observation := awaitSnapshot(t, manager, success.reservation, success.identity, effect.SupervisorSucceeded)
	if observation.Evidence.Reference == "" {
		t.Fatal("successful snapshot returned no cgroup evidence reference")
	}
	manifest, err := manager.QuerySnapshot(context.Background(), success.reservation, success.identity)
	if err != nil || !manifest.ContainmentProven || manifest.Artifact.Bytes <= 0 {
		t.Fatalf("query successful snapshot = %+v, %v", manifest, err)
	}
	var copied bytes.Buffer
	if _, err := manager.CopySnapshotArtifact(context.Background(), success.reservation, success.identity, &copied); err != nil || copied.Len() == 0 {
		t.Fatalf("copy successful snapshot = %d bytes, %v", copied.Len(), err)
	}
	assertCgroupEmpty(t, cgroup.cgroupPath(success.identity.RuntimeInstanceID))
	assertNoSnapshotCredentials(t, success.directory)

	// The wrapper is pinned too and ultimately execs real pg_dump. It pauses
	// first so cgroup.kill terminates the helper while its authenticated running
	// status and private credentials exist. ObserveSnapshot must return unknown
	// and scrub credentials; an empty cgroup cannot become terminal evidence.
	slow := filepath.Join(t.TempDir(), "slow-pg_dump")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 30\nexec "+shellQuote(pgDump)+" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	dead := launchLinuxSnapshot(t, manager, slow, fileSHA256(t, slow), service, "helper-death")
	awaitPrivateCredentials(t, dead.directory)
	if err := os.WriteFile(filepath.Join(cgroup.cgroupPath(dead.identity.RuntimeInstanceID), "cgroup.kill"), []byte("1\n"), 0o200); err != nil {
		t.Fatalf("kill helper cgroup: %v", err)
	}
	assertCgroupEmpty(t, cgroup.cgroupPath(dead.identity.RuntimeInstanceID))
	deathObservation, err := manager.ObserveSnapshot(context.Background(), dead.reservation, dead.identity)
	if err != nil {
		t.Fatalf("observe killed helper: %v", err)
	}
	if deathObservation.Phase != effect.SupervisorUnknown {
		t.Fatalf("killed helper observation = %+v, want unknown", deathObservation)
	}
	assertNoSnapshotCredentials(t, dead.directory)
}

type linuxSnapshot struct {
	reservation effect.Reservation
	identity    effect.ExecutionIdentity
	directory   string
}

func launchLinuxSnapshot(t *testing.T, manager *Manager, binary, digest, service, name string) linuxSnapshot {
	t.Helper()
	material := SnapshotLaunchMaterial{PGDumpPath: binary, PGDumpSHA256: digest, ServiceName: service, ServiceFile: []byte("[" + service + "]\nhost=/tmp/norn-snapshot-pg\nport=55432\nuser=postgres\ndbname=postgres\nsslmode=disable\n"), Password: "test-only-password", Subject: "app:integration/db:postgres@generation:1", Timeout: time.Minute}
	payload, err := manager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "linux-integration", Resource: "app/integration/snapshot", OperationClaim: effect.OperationClaim{OperationID: name, OwnerID: "test", Generation: 1}, Stage: SnapshotStage, Supervisor: "norn-effect-runner", SupervisorExecutionID: name, LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	identity, err := manager.LaunchSnapshot(context.Background(), reservation, material)
	if err != nil {
		t.Fatalf("launch %s: %v", name, err)
	}
	return linuxSnapshot{reservation: reservation, identity: identity, directory: filepath.Join(manager.root, sha256DirectoryName(reservation.SupervisorExecutionID))}
}

func awaitSnapshot(t *testing.T, manager *Manager, reservation effect.Reservation, identity effect.ExecutionIdentity, want effect.SupervisorPhase) effect.Observation {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		observation, err := manager.ObserveSnapshot(context.Background(), reservation, identity)
		if err != nil {
			t.Fatalf("observe snapshot: %v", err)
		}
		if observation.Phase == want {
			return observation
		}
		if observation.Phase != effect.SupervisorRunning && observation.Phase != effect.SupervisorUnknown {
			t.Fatalf("snapshot phase = %s, want %s", observation.Phase, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("snapshot did not reach %s", want)
	return effect.Observation{}
}

func awaitPrivateCredentials(t *testing.T, directory string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(directory)
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".snapshot-") {
				continue
			}
			private := filepath.Join(directory, entry.Name())
			if _, serviceErr := os.Stat(filepath.Join(private, "service.conf")); serviceErr == nil {
				if _, passErr := os.Stat(filepath.Join(private, "passfile")); passErr == nil {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper did not create private connection files")
}

func assertNoSnapshotCredentials(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".snapshot-") {
			continue
		}
		for _, name := range []string{"service.conf", "passfile"} {
			if _, err := os.Lstat(filepath.Join(directory, entry.Name(), name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("private credential %s remained: %v", name, err)
			}
		}
	}
}

func assertCgroupEmpty(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		populated, err := cgroupPopulated(path)
		if err == nil && !populated {
			procs, readErr := os.ReadFile(filepath.Join(path, "cgroup.procs"))
			if readErr == nil && strings.TrimSpace(string(procs)) == "" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("cgroup %s remained populated", path)
}

func requireExecutable(t *testing.T, variable string) string {
	t.Helper()
	path := os.Getenv(variable)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s must name an executable regular file: %v", variable, err)
	}
	return path
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
