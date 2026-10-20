package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"norn/v2/api/effect"
)

type offlineDeleteStartFailure struct{ *backendFake }

func (b offlineDeleteStartFailure) Start(context.Context, BackendExecution, effect.LaunchMaterial) error {
	return os.ErrProcessDone
}

func TestOfflineDeleteRuntimeResolverSurvivesRestart(t *testing.T) {
	manager, backend, reservation, identity := launchedOfflineTerminalRuntime(t)
	planID, attemptID, grantID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := manager.BindOfflineDeleteRuntime(context.Background(), planID, attemptID, grantID, reservation); err != nil {
		t.Fatal(err)
	}
	if err := manager.BindOfflineDeleteRuntime(context.Background(), planID, attemptID, grantID, reservation); err != nil {
		t.Fatalf("idempotent bind after response loss: %v", err)
	}
	changed := reservation
	changed.Authority = "different-authority"
	changed.InputDigest, _ = effect.ComputeInputDigest(changed)
	if err := manager.BindOfflineDeleteRuntime(context.Background(), planID, attemptID, grantID, changed); err == nil {
		t.Fatal("different reservation was accepted as an idempotent bind")
	}
	restarted, err := NewManager(manager.root, testSigningKey, backend)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := restarted.ResolveOfflineDeleteRuntime(context.Background(), planID, attemptID, grantID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.SupervisorRootID != manager.RootID() || resolved.SupervisorExecutionID != identity.SupervisorExecutionID || resolved.RuntimeInstanceID != identity.RuntimeInstanceID {
		t.Fatalf("resolved wrong runtime: %+v", resolved)
	}
	if _, err := restarted.ResolveOfflineDeleteRuntime(context.Background(), planID, uuid.NewString(), grantID); err == nil {
		t.Fatal("different attempt resolved the runtime")
	}
}

func TestOfflineDeleteRuntimeResolverRejectsStaleOrCrashedRuntime(t *testing.T) {
	manager, backend, reservation, _ := launchedOfflineTerminalRuntime(t)
	planID, attemptID, grantID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := manager.BindOfflineDeleteRuntime(context.Background(), planID, attemptID, grantID, reservation); err != nil {
		t.Fatal(err)
	}
	backend.states[reservation.SupervisorExecutionID] = BackendState{Phase: effect.SupervisorSucceeded, ContainmentProven: true}
	if _, err := manager.ResolveOfflineDeleteRuntime(context.Background(), planID, attemptID, grantID); err == nil {
		t.Fatal("terminal backend state resolved as running")
	}

	crashBackend := offlineDeleteStartFailure{newBackendFake()}
	crashManager := testManager(t, t.TempDir(), crashBackend)
	material := testMaterial()
	payload, err := crashManager.BuildTestDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	crashReservation := effect.Reservation{Authority: "authority", Resource: "fleet/work-staging/delete", OperationClaim: effect.OperationClaim{OperationID: "operation-crash", OwnerID: "worker", Generation: 1}, Stage: "offline.delete", Supervisor: "norn-effect-runner", SupervisorExecutionID: uuid.NewString(), LaunchPayload: payload}
	crashReservation.InputDigest, err = effect.ComputeInputDigest(crashReservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := crashManager.Prepare(context.Background(), crashReservation); err != nil {
		t.Fatal(err)
	}
	if _, err := crashManager.Launch(context.Background(), crashReservation, material); err == nil {
		t.Fatal("crash fixture launched")
	}
	if err := crashManager.BindOfflineDeleteRuntime(context.Background(), uuid.NewString(), uuid.NewString(), uuid.NewString(), crashReservation); err == nil {
		t.Fatal("prepared crash window bound as launched runtime")
	}
}

func TestOfflineDeleteRuntimeResolverRejectsTamperedBinding(t *testing.T) {
	manager, _, reservation, _ := launchedOfflineTerminalRuntime(t)
	planID, attemptID, grantID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := manager.BindOfflineDeleteRuntime(context.Background(), planID, attemptID, grantID, reservation); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(manager.root, offlineDeleteRuntimeBindingName(planID, attemptID, grantID))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-2] ^= 1
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ResolveOfflineDeleteRuntime(context.Background(), planID, attemptID, grantID); err == nil {
		t.Fatal("tampered runtime binding resolved")
	}
}

func TestOfflineDeleteRuntimeBindIgnoresPrivateCrashResidue(t *testing.T) {
	manager, _, reservation, _ := launchedOfflineTerminalRuntime(t)
	planID, attemptID, grantID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	residue, err := os.CreateTemp(manager.root, ".offline-delete-runtime-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := residue.Write([]byte(`{"partial":`)); err != nil {
		t.Fatal(err)
	}
	if err := residue.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.BindOfflineDeleteRuntime(context.Background(), planID, attemptID, grantID, reservation); err != nil {
		t.Fatalf("private crash residue blocked canonical publish: %v", err)
	}
	if _, err := manager.ResolveOfflineDeleteRuntime(context.Background(), planID, attemptID, grantID); err != nil {
		t.Fatalf("published binding unavailable: %v", err)
	}
}
