//go:build linux

package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// TestLinuxCgroupMigrationDescendantTimeout proves the migration timeout owns
// the whole command cgroup after the shell leader has exited.
func TestLinuxCgroupMigrationDescendantTimeout(t *testing.T) {
	if os.Getenv("NORN_REAL_CGROUP_TEST") != "1" {
		t.Skip("set NORN_REAL_CGROUP_TEST=1 in the privileged Linux container harness")
	}
	root := filepath.Join("/sys/fs/cgroup", "norn-migration-timeout-integration")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create migration cgroup root: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(root) })
	for _, test := range []struct {
		name         string
		command      string
		timeout      time.Duration
		wantTimedOut bool
	}{
		{name: "descendant completes", command: "sleep 0.1 &", timeout: 2 * time.Second},
		{name: "descendant outlives timeout", command: "sleep 30 &", timeout: 500 * time.Millisecond, wantTimedOut: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-"))
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.WriteFile(filepath.Join(path, "cgroup.kill"), []byte("1\n"), 0o200)
				_ = os.Remove(path)
			})
			terminator, err := openCgroupTerminator(path)
			if err != nil {
				t.Fatal(err)
			}
			material := effect.LaunchMaterial{Argv: []string{"sh", "-c", test.command},
				Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin"}, Timeout: test.timeout}
			timedOut, runErr := runContained(material, io.Discard, &migrationCgroupTerminator{terminator}, realSchedule, nil)
			if timedOut != test.wantTimedOut || (runErr != nil && !timedOut) {
				t.Fatalf("runContained timeout=%v err=%v, want timeout=%v", timedOut, runErr, test.wantTimedOut)
			}
			assertCgroupEmpty(t, path)
		})
	}
}

// The helper owns the timeout guard. Killing only that helper must not leave
// its command able to write forever, or turn an unknown result into success.
func TestLinuxCgroupMigrationHelperDeath(t *testing.T) {
	if os.Getenv("NORN_REAL_CGROUP_TEST") != "1" {
		t.Skip("set NORN_REAL_CGROUP_TEST=1 in the privileged Linux container harness")
	}
	runner := requireExecutable(t, "NORN_EFFECT_RUNNER_BINARY")
	root := filepath.Join("/sys/fs/cgroup", "norn-migration-helper-death")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(root, "cgroup.kill"), []byte("1\n"), 0o200)
		_ = os.Remove(root)
	})
	key := bytes.Repeat([]byte("migration-helper-death-key-"), 2)
	backend, err := NewCgroupBackend(root, runner, fileSHA256(t, runner), key)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(t.TempDir(), key, backend)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "started")
	command := "printf begun > " + shellQuote(marker) + "; sleep 30"
	psql := os.Getenv("NORN_TEST_PSQL")
	if psql != "" {
		requireExecutable(t, "NORN_TEST_PSQL")
		setup := exec.Command(psql, "-X", "-v", "ON_ERROR_STOP=1", "-h", "/tmp/norn-snapshot-pg", "-p", "55432", "-U", "postgres", "-d", "postgres", "-c", "CREATE TABLE IF NOT EXISTS norn_migration_crash_probe (id integer PRIMARY KEY); TRUNCATE norn_migration_crash_probe")
		if output, err := setup.CombinedOutput(); err != nil {
			t.Fatalf("prepare disposable migration database: %v: %s", err, output)
		}
		command = shellQuote(psql) + " -X -v ON_ERROR_STOP=1 -h /tmp/norn-snapshot-pg -p 55432 -U postgres -d postgres -c " +
			shellQuote("INSERT INTO norn_migration_crash_probe (id) VALUES (1)") +
			" >/dev/null && printf begun > " + shellQuote(marker) + " && sleep 30 && " + shellQuote(psql) +
			" -X -v ON_ERROR_STOP=1 -h /tmp/norn-snapshot-pg -p 55432 -U postgres -d postgres -c " +
			shellQuote("INSERT INTO norn_migration_crash_probe (id) VALUES (2)") + " >/dev/null"
	}
	commandSHA := sha256.Sum256([]byte(command))
	intent := testMigrationIntent()
	intent.CommandSHA256 = hex.EncodeToString(commandSHA[:])
	intent.TimeoutMillis = 30_000
	payload, err := manager.BuildMigrationDescriptor(intent)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "linux-integration", Resource: "app/integration/migrate",
		OperationClaim: effect.OperationClaim{OperationID: "helper-death", OwnerID: "test", Generation: 1},
		Stage:          MigrationStage, Supervisor: "norn-effect-runner", SupervisorExecutionID: "migration-helper-death", LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	identity, err := manager.LaunchMigration(context.Background(), reservation, MigrationLaunchMaterial{Command: command,
		Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin", "PGPASSFILE={{private-file:passfile}}"},
		PrivateFiles: []MigrationPrivateFile{{Name: "passfile", Contents: []byte("test-only-private")}}})
	if err != nil {
		t.Fatal(err)
	}
	cgroup := backend.(*cgroupBackend).cgroupPath(identity.RuntimeInstanceID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("migration command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	active, err := manager.ObserveMigration(context.Background(), reservation, identity)
	if err != nil || active.Phase != effect.SupervisorRunning {
		t.Fatalf("live helper observation=%+v err=%v", active, err)
	}
	if populated, err := cgroupPopulated(filepath.Join(cgroup, "command")); err != nil || !populated {
		t.Fatalf("live helper command containment=%v err=%v", populated, err)
	}
	procs, err := os.ReadFile(filepath.Join(cgroup, "cgroup.procs"))
	if err != nil || len(strings.Fields(string(procs))) != 1 {
		t.Fatalf("helper cgroup processes=%q err=%v", procs, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(procs)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		observation, err := manager.ObserveMigration(context.Background(), reservation, identity)
		if err != nil {
			t.Fatal(err)
		}
		if observation.Phase == effect.SupervisorUnknown {
			break
		}
		if observation.Phase != effect.SupervisorRunning || time.Now().After(deadline) {
			t.Fatalf("orphan migration observation=%+v", observation)
		}
		time.Sleep(20 * time.Millisecond)
	}
	assertCgroupEmpty(t, cgroup)
	if psql != "" {
		query := exec.Command(psql, "-X", "-At", "-h", "/tmp/norn-snapshot-pg", "-p", "55432", "-U", "postgres", "-d", "postgres", "-c", "SELECT string_agg(id::text, ',' ORDER BY id) FROM norn_migration_crash_probe")
		output, err := query.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != "1" {
			t.Fatalf("committed write was lost or later write survived: rows=%q err=%v", output, err)
		}
	}
	checker := &migrationCheckerFake{result: MigrationPostconditionResult{TargetSHA256: intent.TargetSHA256,
		PostconditionSHA256: intent.PostconditionSHA256, Satisfied: true}}
	verifier, err := NewMigrationVerifier(manager, checker)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := manager.ObserveMigration(context.Background(), reservation, identity)
	if err != nil || observation.Phase != effect.SupervisorUnknown {
		t.Fatalf("contained interrupted migration observation=%+v err=%v", observation, err)
	}
	if _, err := verifier.Verify(context.Background(), effect.Record{Reservation: reservation, Execution: identity}, observation); err == nil || checker.checks != 0 {
		t.Fatalf("interrupted migration reached postcondition checker: checks=%d err=%v", checker.checks, err)
	}
	privateDirs, err := filepath.Glob(filepath.Join(manager.root, sha256DirectoryName(reservation.SupervisorExecutionID), ".migration-*"))
	if err != nil || len(privateDirs) != 0 {
		t.Fatalf("orphan migration private files remain: %v, %v", privateDirs, err)
	}
}

// A real API process can disappear after the database write commits but
// before it records the runner's completion. Its successor must read the
// original execution journal and observe that runner without launching SQL
// a second time.
func TestLinuxCgroupMigrationAPIProcessExit(t *testing.T) {
	if os.Getenv("NORN_REAL_CGROUP_TEST") != "1" {
		t.Skip("set NORN_REAL_CGROUP_TEST=1 in the privileged Linux container harness")
	}
	runner := requireExecutable(t, "NORN_EFFECT_RUNNER_BINARY")
	psql := requireExecutable(t, "NORN_TEST_PSQL")
	root := filepath.Join("/sys/fs/cgroup", "norn-migration-api-exit")
	journal := filepath.Join(os.TempDir(), "norn-migration-api-exit-journal")
	marker := filepath.Join(os.TempDir(), "norn-migration-api-exit-committed")
	identityPath := filepath.Join(os.TempDir(), "norn-migration-api-exit-identity")
	key := bytes.Repeat([]byte("migration-api-exit-key-"), 2)
	command := shellQuote(psql) + " -X -v ON_ERROR_STOP=1 -h /tmp/norn-snapshot-pg -p 55432 -U postgres -d postgres -c " +
		shellQuote("INSERT INTO norn_migration_api_exit_probe (id) VALUES (1)") +
		" >/dev/null && printf committed > " + shellQuote(marker) + " && sleep 3"
	open := func() (*Manager, effect.Reservation) {
		backend, err := NewCgroupBackend(root, runner, fileSHA256(t, runner), key)
		if err != nil {
			t.Fatal(err)
		}
		manager, err := NewManager(journal, key, backend)
		if err != nil {
			t.Fatal(err)
		}
		intent := testMigrationIntent()
		digest := sha256.Sum256([]byte(command))
		intent.CommandSHA256 = hex.EncodeToString(digest[:])
		intent.TimeoutMillis = 30_000
		payload, err := manager.BuildMigrationDescriptor(intent)
		if err != nil {
			t.Fatal(err)
		}
		reservation := effect.Reservation{Authority: "linux-integration", Resource: "app/integration/migrate",
			OperationClaim: effect.OperationClaim{OperationID: "api-exit", OwnerID: "test", Generation: 1},
			Stage:          MigrationStage, Supervisor: "norn-effect-runner", SupervisorExecutionID: "migration-api-exit", LaunchPayload: payload}
		reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
		if err != nil {
			t.Fatal(err)
		}
		return manager, reservation
	}
	material := MigrationLaunchMaterial{Command: command, Directory: "/tmp", Environment: []string{"PATH=/usr/bin:/bin"}}
	if os.Getenv("NORN_MIGRATION_API_EXIT_CHILD") == "1" {
		manager, reservation := open()
		if err := manager.Prepare(context.Background(), reservation); err != nil {
			t.Fatal(err)
		}
		identity, err := manager.LaunchMigration(context.Background(), reservation, material)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(identityPath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(marker); err == nil {
				os.Exit(47)
			}
			if time.Now().After(deadline) {
				t.Fatal("migration did not commit before API exit")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(root, "cgroup.kill"), []byte("1\n"), 0o200)
		_ = os.Remove(root)
	})
	setup := exec.Command(psql, "-X", "-v", "ON_ERROR_STOP=1", "-h", "/tmp/norn-snapshot-pg", "-p", "55432", "-U", "postgres", "-d", "postgres", "-c", "CREATE TABLE norn_migration_api_exit_probe (id integer PRIMARY KEY)")
	if output, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("prepare disposable migration database: %v: %s", err, output)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestLinuxCgroupMigrationAPIProcessExit$")
	child.Env = append(os.Environ(), "NORN_MIGRATION_API_EXIT_CHILD=1")
	if output, err := child.CombinedOutput(); err == nil {
		t.Fatalf("API process returned without exiting: %s", output)
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 47 {
			t.Fatalf("API process exit = %v: %s", err, output)
		}
	}
	encoded, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	var original effect.ExecutionIdentity
	if err := json.Unmarshal(encoded, &original); err != nil {
		t.Fatal(err)
	}
	cgroupDigest := sha256.Sum256([]byte(original.RuntimeInstanceID))
	commandCgroup := filepath.Join(root, hex.EncodeToString(cgroupDigest[:]), "command")
	if populated, err := cgroupPopulated(commandCgroup); err != nil || !populated {
		t.Fatalf("runner did not survive API process exit: populated=%v err=%v", populated, err)
	}
	manager, reservation := open()
	resumed, err := manager.LaunchMigration(context.Background(), reservation, material)
	if err != nil || resumed.RuntimeInstanceID != original.RuntimeInstanceID {
		t.Fatalf("successor launch = %+v, original=%+v, err=%v", resumed, original, err)
	}
	deadline := time.Now().Add(8 * time.Second)
	var observation effect.Observation
	for {
		observation, err = manager.ObserveMigration(context.Background(), reservation, resumed)
		if err != nil {
			t.Fatal(err)
		}
		if observation.Phase == effect.SupervisorSucceeded {
			break
		}
		if observation.Phase != effect.SupervisorRunning || time.Now().After(deadline) {
			t.Fatalf("successor observation = %+v", observation)
		}
		time.Sleep(20 * time.Millisecond)
	}
	query := exec.Command(psql, "-X", "-At", "-h", "/tmp/norn-snapshot-pg", "-p", "55432", "-U", "postgres", "-d", "postgres", "-c", "SELECT count(*) FROM norn_migration_api_exit_probe")
	if output, err := query.CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "1" {
		t.Fatalf("migration write count = %q, err=%v", output, err)
	}
	intent := testMigrationIntent()
	checker := &migrationCheckerFake{result: MigrationPostconditionResult{TargetSHA256: intent.TargetSHA256,
		PostconditionSHA256: intent.PostconditionSHA256, Satisfied: true}}
	verifier, err := NewMigrationVerifier(manager, checker)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := verifier.Verify(context.Background(), effect.Record{Reservation: reservation}, observation)
	if err != nil || verification.Decision != effect.VerificationSucceeded || verification.RuntimeInstanceID != original.RuntimeInstanceID || checker.checks != 1 {
		t.Fatalf("successor verification = %+v, checks=%d, err=%v", verification, checker.checks, err)
	}
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
