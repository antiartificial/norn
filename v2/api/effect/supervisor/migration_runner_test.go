package supervisor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
)

const migrationSecretCanary = "NORN_MIGRATION_PRIVATE_URL_CANARY_37e6"

func migrationHelperRequest(t *testing.T, command string) (migrationRunnerRequest, []byte) {
	t.Helper()
	manager := testManager(t, t.TempDir(), newBackendFake())
	intent := testMigrationIntent()
	commandHash := sha256.Sum256([]byte(command))
	intent.CommandSHA256 = hex.EncodeToString(commandHash[:])
	payload, err := manager.BuildMigrationDescriptor(intent)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor MigrationDescriptor
	if err := json.Unmarshal(payload, &descriptor); err != nil {
		t.Fatal(err)
	}
	stateDirectory := t.TempDir()
	runtimeID := "migration-runtime-" + filepath.Base(stateDirectory)
	key := runnerStatusKey(testSigningKey, runtimeID)
	return migrationRunnerRequest{
		Protocol:   MigrationProtocolV1,
		Execution:  BackendExecution{SupervisorExecutionID: "migration-execution", RuntimeInstanceID: runtimeID, StateDirectory: stateDirectory},
		Descriptor: descriptor, Command: command, Directory: t.TempDir(),
		Environment: []string{"PATH=/usr/bin:/bin", "DATABASE_URL=" + migrationSecretCanary},
		StatusKey:   hex.EncodeToString(key),
	}, key
}

func runMigrationRequest(t *testing.T, request migrationRunnerRequest) error {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return RunHelper(bytes.NewReader(encoded))
}

func TestMigrationHelperDiscardsPrivateOutputAndPersistsOnlyStatus(t *testing.T) {
	request, key := migrationHelperRequest(t, `printf '%s\n' "$DATABASE_URL"`)
	if err := runMigrationRequest(t, request); err != nil {
		t.Fatal(err)
	}
	descriptor, _ := json.Marshal(request.Descriptor)
	digest := sha256.Sum256(descriptor)
	status, err := readMigrationStatus(request.Execution.StateDirectory, key, request.Execution.RuntimeInstanceID, hex.EncodeToString(digest[:]))
	if err != nil || status.Phase != effect.SupervisorSucceeded || status.ExitCode == nil || *status.ExitCode != 0 {
		t.Fatalf("migration status = %+v, %v", status, err)
	}
	entries, err := os.ReadDir(request.Execution.StateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "migration-status.json" {
		t.Fatalf("migration helper retained unexpected files: %v", entries)
	}
	data, err := os.ReadFile(filepath.Join(request.Execution.StateDirectory, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(migrationSecretCanary)) || bytes.Contains(data, []byte(request.Command)) ||
		bytes.Contains(data, []byte("DATABASE_URL")) {
		t.Fatal("migration helper persisted command, environment or output")
	}
	data[len(data)-2] ^= 1
	if err := os.WriteFile(filepath.Join(request.Execution.StateDirectory, entries[0].Name()), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readMigrationStatus(request.Execution.StateDirectory, key, request.Execution.RuntimeInstanceID, hex.EncodeToString(digest[:])); err == nil {
		t.Fatal("tampered migration status was trusted")
	}
}

func TestMigrationHelperRejectsChangedCommandBeforeExecution(t *testing.T) {
	request, _ := migrationHelperRequest(t, "true")
	marker := filepath.Join(request.Directory, "unexpected")
	request.Command = "touch " + marker
	if err := runMigrationRequest(t, request); err == nil {
		t.Fatal("changed migration command was accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unaccepted migration command ran: %v", err)
	}
	if _, err := os.Stat(filepath.Join(request.Execution.StateDirectory, "migration-status.json")); !os.IsNotExist(err) {
		t.Fatalf("unaccepted migration wrote status: %v", err)
	}
}

func TestMigrationHelperFailureDoesNotClaimRepeatSafety(t *testing.T) {
	request, key := migrationHelperRequest(t, `printf '%s\n' "$DATABASE_URL"; touch partial-write; exit 7`)
	if err := runMigrationRequest(t, request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(request.Directory, "partial-write")); err != nil {
		t.Fatal("partial migration write was not exercised: ", err)
	}
	descriptor, _ := json.Marshal(request.Descriptor)
	digest := sha256.Sum256(descriptor)
	status, err := readMigrationStatus(request.Execution.StateDirectory, key, request.Execution.RuntimeInstanceID, hex.EncodeToString(digest[:]))
	if err != nil || status.Phase != effect.SupervisorFailed || status.ExitCode == nil || *status.ExitCode != 7 {
		t.Fatalf("failed migration status = %+v, %v", status, err)
	}
	data, err := os.ReadFile(filepath.Join(request.Execution.StateDirectory, "migration-status.json"))
	if err != nil || strings.Contains(string(data), migrationSecretCanary) {
		t.Fatal("failed migration persisted private output")
	}
}

func TestMigrationHelperOwnsAndScrubsConnectionFiles(t *testing.T) {
	request, key := migrationHelperRequest(t, `test -f "$PGSERVICEFILE" && test -f "$DATABASE_URL_FILE" && passfile=$(sed -n 's/^passfile=//p' "$PGSERVICEFILE") && test -f "$passfile" && cat "$passfile" "$DATABASE_URL_FILE"`)
	request.PrivateFiles = []migrationPrivateFile{
		{Name: "pg_service.conf", Contents: []byte("[norn]\npassfile={{private-file:passfile}}\n"), Template: true},
		{Name: "passfile", Contents: []byte(migrationSecretCanary)},
		{Name: "connection.url", Contents: []byte("postgresql://" + migrationSecretCanary + "@localhost/app")},
	}
	request.Environment = append(request.Environment,
		"PGSERVICEFILE={{private-file:pg_service.conf}}", "DATABASE_URL_FILE={{private-file:connection.url}}")
	if err := runMigrationRequest(t, request); err != nil {
		t.Fatal(err)
	}
	descriptor, _ := json.Marshal(request.Descriptor)
	digest := sha256.Sum256(descriptor)
	status, err := readMigrationStatus(request.Execution.StateDirectory, key, request.Execution.RuntimeInstanceID, hex.EncodeToString(digest[:]))
	if err != nil || status.Phase != effect.SupervisorSucceeded {
		t.Fatalf("private file migration status = %+v, %v", status, err)
	}
	entries, err := os.ReadDir(request.Execution.StateDirectory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "migration-status.json" {
		t.Fatalf("runner retained private connection material: %v, %v", entries, err)
	}
	data, _ := os.ReadFile(filepath.Join(request.Execution.StateDirectory, "migration-status.json"))
	if bytes.Contains(data, []byte(migrationSecretCanary)) {
		t.Fatal("runner status retained private connection value")
	}
}

func TestMigrationHelperRefusesAPIManagedConnectionFilePath(t *testing.T) {
	for _, name := range []string{"PGSERVICEFILE", "DATABASE_URL_FILE", "CUSTOM_DB_FILE"} {
		t.Run(name, func(t *testing.T) {
			request, _ := migrationHelperRequest(t, "touch should-not-run")
			request.Environment = append(request.Environment, name+"=/tmp/api-session/connection")
			if err := runMigrationRequest(t, request); err == nil {
				t.Fatal("API-owned connection file path was accepted")
			}
			if _, err := os.Stat(filepath.Join(request.Directory, "should-not-run")); !os.IsNotExist(err) {
				t.Fatalf("migration ran with an API-owned connection file: %v", err)
			}
		})
	}
}

func TestDeadMigrationHelperScrubsPrivateFilesWithoutInventingTerminalStatus(t *testing.T) {
	request, key := migrationHelperRequest(t, "true")
	request.PrivateFiles = []migrationPrivateFile{{Name: "connection.url", Contents: []byte(migrationSecretCanary)}}
	descriptor, _ := json.Marshal(request.Descriptor)
	digest := sha256.Sum256(descriptor)
	descriptorSHA := hex.EncodeToString(digest[:])
	if err := writeMigrationStatus(request.Execution.StateDirectory, key, migrationRunnerStatus{
		Protocol: MigrationProtocolV1, RuntimeInstanceID: request.Execution.RuntimeInstanceID,
		DescriptorSHA256: descriptorSHA, Phase: effect.SupervisorRunning, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err := prepareMigrationPrivateFiles(request.Execution.StateDirectory, request.PrivateFiles, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := recoverMigrationPrivateMaterial(request.Execution.StateDirectory, key, request.Execution.RuntimeInstanceID, descriptorSHA); err != nil {
		t.Fatal(err)
	}
	if err := recoverMigrationPrivateMaterial(request.Execution.StateDirectory, key, request.Execution.RuntimeInstanceID, descriptorSHA); err != nil {
		t.Fatalf("crash cleanup was not idempotent: %v", err)
	}
	entries, err := os.ReadDir(request.Execution.StateDirectory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "migration-status.json" {
		t.Fatalf("dead helper retained private material: %v, %v", entries, err)
	}
	status, err := readMigrationStatus(request.Execution.StateDirectory, key, request.Execution.RuntimeInstanceID, descriptorSHA)
	if err != nil || status.Phase != effect.SupervisorRunning {
		t.Fatalf("dead helper was misclassified as terminal: %+v, %v", status, err)
	}
}

func TestDeadMigrationHelperRefusesForeignSymlink(t *testing.T) {
	request, key := migrationHelperRequest(t, "true")
	descriptor, _ := json.Marshal(request.Descriptor)
	digest := sha256.Sum256(descriptor)
	descriptorSHA := hex.EncodeToString(digest[:])
	if err := writeMigrationStatus(request.Execution.StateDirectory, key, migrationRunnerStatus{
		Protocol: MigrationProtocolV1, RuntimeInstanceID: request.Execution.RuntimeInstanceID,
		DescriptorSHA256: descriptorSHA, Phase: effect.SupervisorRunning, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err := prepareMigrationPrivateFiles(request.Execution.StateDirectory,
		[]migrationPrivateFile{{Name: "connection.url", Contents: []byte("private")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(request.Execution.StateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	var private string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".migration-") {
			private = filepath.Join(request.Execution.StateDirectory, entry.Name())
		}
	}
	if private == "" {
		t.Fatal("private migration directory is unavailable")
	}
	victim := filepath.Join(t.TempDir(), "foreign")
	if err := os.WriteFile(victim, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(private, "connection.url")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(private, "connection.url")); err != nil {
		t.Fatal(err)
	}
	if err := recoverMigrationPrivateMaterial(request.Execution.StateDirectory, key, request.Execution.RuntimeInstanceID, descriptorSHA); err == nil {
		t.Fatal("foreign symlink was accepted as private material")
	}
	data, err := os.ReadFile(victim)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("foreign file was changed: %q, %v", data, err)
	}
}
