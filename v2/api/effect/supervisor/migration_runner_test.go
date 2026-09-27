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
