package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
)

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
