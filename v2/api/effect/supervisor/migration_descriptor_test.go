package supervisor

import (
	"bytes"
	"context"
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

func TestManagerMigrationLaunchIsDurableAndPrivate(t *testing.T) {
	backend := newBackendFake()
	root := t.TempDir()
	manager := testManager(t, root, backend)
	command := "printf done >/dev/null"
	commandDigest := sha256.Sum256([]byte(command))
	intent := testMigrationIntent()
	intent.CommandSHA256 = hex.EncodeToString(commandDigest[:])
	payload, err := manager.BuildMigrationDescriptor(intent)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "authority", Resource: "app/demo/migrate",
		OperationClaim: effect.OperationClaim{OperationID: "operation", OwnerID: "worker", Generation: 1},
		Stage:          MigrationStage, Supervisor: "migration-runner", SupervisorExecutionID: "migration-private", LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	material := MigrationLaunchMaterial{Command: command, Directory: t.TempDir(),
		Environment:  []string{"PGPASSFILE={{private-file:passfile}}"},
		PrivateFiles: []MigrationPrivateFile{{Name: "passfile", Contents: []byte("private-canary")}}}
	wrong := material
	wrong.Command = "printf changed >/dev/null"
	if _, err := manager.LaunchMigration(context.Background(), reservation, wrong); err == nil {
		t.Fatal("changed command crossed accepted intent")
	}
	backend.snapshotStartErr = errors.New("acknowledgement lost")
	if _, err := manager.LaunchMigration(context.Background(), reservation, material); err == nil {
		t.Fatal("ambiguous migration start was accepted")
	}
	backend.snapshotStartErr = nil
	identity, err := manager.LaunchMigration(context.Background(), reservation, material)
	if err != nil || identity.RuntimeInstanceID == "" || backend.starts != 0 {
		t.Fatalf("ambiguous retry relaunched migration: identity=%+v starts=%d err=%v", identity, backend.starts, err)
	}
	if _, err := manager.Query(context.Background(), reservation, identity); err == nil {
		t.Fatal("generic query accepted migration evidence")
	}
	if _, err := manager.Revoke(context.Background(), reservation, identity); err == nil {
		t.Fatal("generic revoke accepted migration descriptor")
	}
	exitCode := 0
	backend.states[reservation.SupervisorExecutionID] = BackendState{Phase: effect.SupervisorSucceeded,
		ExitCode: &exitCode, ContainmentProven: true, EvidenceReference: "contained/" + identity.RuntimeInstanceID}
	observation, err := manager.ObserveMigration(context.Background(), reservation, identity)
	if err != nil || observation.Phase != effect.SupervisorSucceeded {
		t.Fatalf("migration observation=%+v err=%v", observation, err)
	}
	verifier, err := NewVerifier(manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), effect.Record{Reservation: reservation, Execution: identity}, observation); err == nil {
		t.Fatal("generic verifier approved migration command exit without database postcondition")
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(data, []byte(command)) || bytes.Contains(data, []byte("private-canary")) {
			t.Errorf("migration private material persisted in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func testMigrationIntent() MigrationIntent {
	return MigrationIntent{
		SourceSHA256: strings.Repeat("a", 64), CommandSHA256: strings.Repeat("b", 64),
		TargetSHA256: strings.Repeat("c", 64), TargetBindingID: "main-db",
		TargetGeneration: 7, PostconditionSHA256: strings.Repeat("d", 64),
		TimeoutMillis: time.Hour.Milliseconds(),
	}
}

func TestMigrationDescriptorIsSecretFreeAndBoundToAcceptedIntent(t *testing.T) {
	manager := testManager(t, t.TempDir(), newBackendFake())
	intent := testMigrationIntent()
	payload, err := manager.BuildMigrationDescriptor(intent)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("password")) || bytes.Contains(payload, []byte("DATABASE_URL")) ||
		bytes.Contains(payload, []byte("postgresql://")) {
		t.Fatalf("migration descriptor contains private launch material: %s", payload)
	}
	if _, err := manager.verifyMigrationDescriptor(payload); err != nil {
		t.Fatal(err)
	}
	var descriptor MigrationDescriptor
	if err := json.Unmarshal(payload, &descriptor); err != nil {
		t.Fatal(err)
	}
	descriptor.TargetGeneration++
	tampered, _ := json.Marshal(descriptor)
	if _, err := manager.verifyMigrationDescriptor(tampered); err == nil {
		t.Fatal("changed target generation reused an immutable migration descriptor")
	}
	descriptor.TargetGeneration--
	descriptor.CommandSHA256 = strings.Repeat("e", 64)
	tampered, _ = json.Marshal(descriptor)
	if _, err := manager.verifyMigrationDescriptor(tampered); err == nil {
		t.Fatal("changed migration command reused an immutable descriptor")
	}
	// Preparation records only the secret-free reservation. It must not
	// accidentally activate the build.test runner.
	reservation := effect.Reservation{Authority: "authority", Resource: "app/demo/migrate",
		OperationClaim: effect.OperationClaim{OperationID: "operation", OwnerID: "worker", Generation: 1},
		Stage:          MigrationStage, Supervisor: "norn-effect-runner", SupervisorExecutionID: "migration-1", LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), reservation); err != nil {
		t.Fatalf("prepare immutable migration reservation: %v", err)
	}
	if _, err := manager.Launch(context.Background(), reservation, effect.LaunchMaterial{}); err == nil {
		t.Fatal("migration descriptor was launched by the generic command runner")
	}
	if got := manager.backend.(*backendFake).starts; got != 0 {
		t.Fatalf("generic backend started %d migrations", got)
	}
}

func TestMigrationDescriptorRejectsIncompleteIntent(t *testing.T) {
	manager := testManager(t, t.TempDir(), newBackendFake())
	for _, mutate := range []func(*MigrationIntent){
		func(i *MigrationIntent) { i.SourceSHA256 = "" },
		func(i *MigrationIntent) { i.CommandSHA256 = "bad" },
		func(i *MigrationIntent) { i.TargetSHA256 = "" },
		func(i *MigrationIntent) { i.TargetBindingID = "postgresql://private" },
		func(i *MigrationIntent) { i.TargetGeneration = 0 },
		func(i *MigrationIntent) { i.PostconditionSHA256 = "" },
		func(i *MigrationIntent) { i.TimeoutMillis = MaxMigrationTimeout.Milliseconds() + 1 },
	} {
		intent := testMigrationIntent()
		mutate(&intent)
		if _, err := manager.BuildMigrationDescriptor(intent); err == nil {
			t.Fatalf("accepted incomplete migration intent: %+v", intent)
		}
	}
}
