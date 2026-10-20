package supervisor

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"norn/v2/api/effect"
)

func launchedOfflineDeleteRuntime(t *testing.T) (*Manager, *backendFake, effect.Reservation, effect.ExecutionIdentity) {
	t.Helper()
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	material := testMaterial()
	reservation := testReservation(t, manager, material)
	identity, err := manager.Launch(context.Background(), reservation, material)
	if err != nil {
		t.Fatal(err)
	}
	return manager, backend, reservation, identity
}

func TestVerifyRuntimeIdentityRequiresDurableRunningRuntime(t *testing.T) {
	manager, backend, reservation, identity := launchedOfflineDeleteRuntime(t)
	if err := manager.verifyRuntimeIdentity(context.Background(), reservation, identity); err != nil {
		t.Fatalf("verify launched identity: %v", err)
	}
	changed := identity
	changed.RuntimeInstanceID = uuid.NewString()
	if err := manager.verifyRuntimeIdentity(context.Background(), reservation, changed); err == nil {
		t.Fatal("forged runtime identity was accepted")
	}
	if err := manager.verifyRuntimeBinding(context.Background(), reservation, uuid.NewString(), identity.SupervisorExecutionID, identity.RuntimeInstanceID); err == nil {
		t.Fatal("foreign supervisor root was accepted")
	}
	if err := manager.verifyRuntimeBinding(context.Background(), reservation, manager.RootID(), identity.SupervisorExecutionID, identity.RuntimeInstanceID); err != nil {
		t.Fatalf("verify exact runtime binding: %v", err)
	}
	backend.mu.Lock()
	backend.states[identity.SupervisorExecutionID] = BackendState{Phase: effect.SupervisorSucceeded, ContainmentProven: true}
	backend.mu.Unlock()
	if err := manager.verifyRuntimeIdentity(context.Background(), reservation, identity); err == nil {
		t.Fatal("terminal runtime was accepted")
	}
}

func TestDispatchOfflineDeleteOnceRecordsIntentBeforeSingle204(t *testing.T) {
	manager, _, reservation, identity := launchedOfflineDeleteRuntime(t)
	commitment := []byte(`{"schemaVersion":"test","exact":true}`)
	admissions, deletes := 0, 0
	admit := func(_ context.Context, got effect.ExecutionIdentity, raw []byte) error {
		admissions++
		if got != identity || string(raw) != string(commitment) {
			t.Fatal("admission did not receive exact identity and commitment")
		}
		return manager.verifyRuntimeIdentity(context.Background(), reservation, got)
	}
	deleter := func(context.Context) (int, error) {
		deletes++
		return 204, nil
	}
	result, err := manager.dispatchOfflineDeleteOnce(context.Background(), reservation, identity, commitment, admit, deleter)
	if err != nil || result.StatusCode != 204 || admissions != 1 || deletes != 1 {
		t.Fatalf("dispatch result=%+v admissions=%d deletes=%d err=%v", result, admissions, deletes, err)
	}
	if _, err := manager.dispatchOfflineDeleteOnce(context.Background(), reservation, identity, commitment, admit, deleter); !errors.Is(err, errOfflineDeleteAlreadyDispatched) {
		t.Fatalf("second dispatch error=%v, want already dispatched", err)
	}
	if admissions != 1 || deletes != 1 {
		t.Fatalf("second attempt admissions=%d deletes=%d", admissions, deletes)
	}
}

func TestDispatchOfflineDeleteLostResponseMarkerPrecedesLaterAuthorityRefusal(t *testing.T) {
	manager, _, reservation, identity := launchedOfflineDeleteRuntime(t)
	lost := errors.New("response lost")
	_, err := manager.dispatchOfflineDeleteOnce(context.Background(), reservation, identity, []byte("commitment"),
		func(context.Context, effect.ExecutionIdentity, []byte) error { return nil },
		func(context.Context) (int, error) { return 0, lost })
	if !errors.Is(err, lost) {
		t.Fatal(err)
	}
	stale := errors.New("authority expired")
	admissions := 0
	_, err = manager.dispatchOfflineDeleteOnce(context.Background(), reservation, identity, []byte("commitment"),
		func(context.Context, effect.ExecutionIdentity, []byte) error { admissions++; return stale },
		func(context.Context) (int, error) { t.Fatal("deleter retried"); return 204, nil })
	if !errors.Is(err, errOfflineDeleteAlreadyDispatched) || admissions != 0 {
		t.Fatalf("retry err=%v admissions=%d", err, admissions)
	}
}

func TestDispatchOfflineDeleteOnceNeverRetriesLostResponse(t *testing.T) {
	manager, _, reservation, identity := launchedOfflineDeleteRuntime(t)
	deletes := 0
	lost := errors.New("response lost")
	_, err := manager.dispatchOfflineDeleteOnce(context.Background(), reservation, identity, []byte("commitment"),
		func(context.Context, effect.ExecutionIdentity, []byte) error { return nil },
		func(context.Context) (int, error) { deletes++; return 0, lost })
	if !errors.Is(err, lost) || deletes != 1 {
		t.Fatalf("first dispatch deletes=%d err=%v", deletes, err)
	}
	_, err = manager.dispatchOfflineDeleteOnce(context.Background(), reservation, identity, []byte("commitment"),
		func(context.Context, effect.ExecutionIdentity, []byte) error { return nil },
		func(context.Context) (int, error) { deletes++; return 204, nil })
	if !errors.Is(err, errOfflineDeleteAlreadyDispatched) || deletes != 1 {
		t.Fatalf("retry deletes=%d err=%v", deletes, err)
	}
}

func TestDispatchOfflineDeleteAdmissionFailureWritesNoIntent(t *testing.T) {
	manager, _, reservation, identity := launchedOfflineDeleteRuntime(t)
	denied := errors.New("stale fence")
	deletes := 0
	_, err := manager.dispatchOfflineDeleteOnce(context.Background(), reservation, identity, []byte("commitment"),
		func(context.Context, effect.ExecutionIdentity, []byte) error { return denied },
		func(context.Context) (int, error) { deletes++; return 204, nil })
	if !errors.Is(err, denied) || deletes != 0 {
		t.Fatalf("denied dispatch deletes=%d err=%v", deletes, err)
	}
	_, err = manager.dispatchOfflineDeleteOnce(context.Background(), reservation, identity, []byte("commitment"),
		func(context.Context, effect.ExecutionIdentity, []byte) error { return nil },
		func(context.Context) (int, error) { deletes++; return 204, nil })
	if err != nil || deletes != 1 {
		t.Fatalf("fresh admission after denial deletes=%d err=%v", deletes, err)
	}
}
