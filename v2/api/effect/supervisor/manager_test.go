package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"norn/v2/api/effect"
)

var testSigningKey = []byte("0123456789abcdef0123456789abcdef")

type backendFake struct {
	mu           sync.Mutex
	states       map[string]BackendState
	starts       int
	startEntered chan struct{}
	startRelease chan struct{}
}

func newBackendFake() *backendFake {
	return &backendFake{states: map[string]BackendState{}}
}

func (b *backendFake) Start(_ context.Context, execution BackendExecution, _ effect.LaunchMaterial) error {
	if b.startEntered != nil {
		close(b.startEntered)
		<-b.startRelease
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.starts++
	if _, ok := b.states[execution.SupervisorExecutionID]; !ok {
		b.states[execution.SupervisorExecutionID] = BackendState{Phase: effect.SupervisorRunning, EvidenceReference: "running/" + execution.RuntimeInstanceID}
	}
	return nil
}

func (b *backendFake) Observe(_ context.Context, execution BackendExecution) (BackendState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.states[execution.SupervisorExecutionID]
	if !ok {
		return BackendState{Phase: effect.SupervisorNotFound, ContainmentProven: true, EvidenceReference: "absent/" + execution.SupervisorExecutionID}, nil
	}
	return state, nil
}

func (b *backendFake) Revoke(_ context.Context, execution BackendExecution) (BackendState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.states, execution.SupervisorExecutionID)
	return BackendState{Phase: effect.SupervisorStopped, ContainmentProven: true, EvidenceReference: "stopped/" + execution.RuntimeInstanceID}, nil
}

func (b *backendFake) RetrieveResult(_ context.Context, execution BackendExecution, _ string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.states[execution.SupervisorExecutionID]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), state.Output...), nil
}

func testManager(t *testing.T, root string, backend Backend) *Manager {
	t.Helper()
	manager, err := NewManager(root, testSigningKey, backend)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func testMaterial() effect.LaunchMaterial {
	return effect.LaunchMaterial{
		Argv:        []string{"sh", "-c", "go test ./... --token=$SECRET"},
		Directory:   "/private/working/tree",
		Environment: []string{"PATH=/usr/bin:/bin", "SECRET=low-entropy-value"},
		Subject:     "git:" + strings.Repeat("a", 40),
		Timeout:     time.Minute,
	}
}

func testReservation(t *testing.T, manager *Manager, material effect.LaunchMaterial) effect.Reservation {
	t.Helper()
	payload, err := manager.BuildTestDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{
		Authority: "authority", Resource: "app/demo/test",
		OperationClaim: effect.OperationClaim{OperationID: "operation", OwnerID: "worker", Generation: 1},
		Stage:          "build.test", Supervisor: "norn-effect-runner", SupervisorExecutionID: "execution-1",
		LaunchPayload: payload,
	}
	digest, err := effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	reservation.InputDigest = digest
	if err := manager.Prepare(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	return reservation
}

func TestDescriptorHidesAndAuthenticatesLaunchMaterial(t *testing.T) {
	manager := testManager(t, t.TempDir(), newBackendFake())
	material := testMaterial()
	payload, err := manager.BuildTestDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte(material.Argv[2]), []byte(material.Directory), []byte("low-entropy-value")} {
		if bytes.Contains(payload, secret) {
			t.Fatalf("persisted descriptor exposed launch material %q: %s", secret, payload)
		}
	}
	reservation := testReservation(t, manager, material)
	changed := material
	changed.Environment = append([]string(nil), material.Environment...)
	changed.Environment[1] = "SECRET=different-value"
	if _, err := manager.Launch(context.Background(), reservation, changed); err == nil {
		t.Fatal("changed environment value reused an immutable descriptor")
	}
	changed = material
	changed.Argv = append([]string(nil), material.Argv...)
	changed.Argv[2] = "go test ./changed"
	if _, err := manager.Launch(context.Background(), reservation, changed); err == nil {
		t.Fatal("changed command reused an immutable descriptor")
	}
}

func TestMissingJournalCanBeTombstonedOnlyInOriginalSupervisorRoot(t *testing.T) {
	root := t.TempDir()
	backend := newBackendFake()
	manager := testManager(t, root, backend)
	material := testMaterial()
	reservation := testReservation(t, manager, material)
	identity := effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID}

	observation, err := manager.Query(context.Background(), reservation, identity)
	if err != nil || observation.Phase != effect.SupervisorNotFound || observation.Identity.RuntimeInstanceID != "" {
		t.Fatalf("missing prepared journal observation=%+v err=%v", observation, err)
	}
	record := effect.Record{Reservation: reservation, Lifecycle: effect.LifecycleReserved}
	verifier, _ := NewVerifier(manager)
	if _, err := verifier.Verify(context.Background(), record, observation); err != nil {
		t.Fatalf("authenticated namespace absence did not verify: %v", err)
	}
	revoked, err := manager.Revoke(context.Background(), reservation, identity)
	if err != nil || revoked.Phase != effect.SupervisorNotFound {
		t.Fatalf("revoke missing execution observation=%+v err=%v", revoked, err)
	}
	if _, err := manager.Launch(context.Background(), reservation, material); err == nil {
		t.Fatal("late launch crossed a durable pre-launch tombstone")
	}

	otherRoot := t.TempDir()
	otherManager := testManager(t, otherRoot, backend)
	if _, err := otherManager.Query(context.Background(), reservation, identity); err == nil {
		t.Fatal("replacement state root inferred never-launched from lost history")
	}
}

func TestMissingRegisteredExecutionJournalRemainsUnknown(t *testing.T) {
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	material := testMaterial()
	reservation := testReservation(t, manager, material)
	identity, err := manager.Launch(context.Background(), reservation, material)
	if err != nil {
		t.Fatal(err)
	}
	directory := manager.root + string(os.PathSeparator) + sha256DirectoryName(reservation.SupervisorExecutionID)
	if err := os.Remove(directory + string(os.PathSeparator) + "journal.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Query(context.Background(), reservation, identity); err == nil {
		t.Fatal("missing per-execution history was misclassified as never launched")
	}
	if _, err := manager.Revoke(context.Background(), reservation, identity); err == nil {
		t.Fatal("missing per-execution history was resolved by a new tombstone")
	}
}

func TestTerminalEvidenceRequiresWholeContainmentAndStoppedIsNotRepeatSafe(t *testing.T) {
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	material := testMaterial()
	reservation := testReservation(t, manager, material)
	identity, err := manager.Launch(context.Background(), reservation, material)
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.states[reservation.SupervisorExecutionID] = BackendState{
		Phase: effect.SupervisorSucceeded, Output: []byte("ok"), ContainmentProven: false, EvidenceReference: "journal/success",
	}
	backend.mu.Unlock()
	observation, err := manager.Query(context.Background(), reservation, identity)
	if err != nil || observation.Phase != effect.SupervisorUnknown {
		t.Fatalf("uncontained terminal observation=%+v err=%v", observation, err)
	}

	backend.mu.Lock()
	backend.states[reservation.SupervisorExecutionID] = BackendState{
		Phase: effect.SupervisorStopped, ContainmentProven: true, EvidenceReference: "journal/stopped",
	}
	backend.mu.Unlock()
	observation, err = manager.Query(context.Background(), reservation, identity)
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := NewVerifier(manager)
	record := effect.Record{Reservation: reservation, Lifecycle: effect.LifecycleLaunched, Execution: identity}
	if _, err := verifier.Verify(context.Background(), record, observation); err == nil {
		t.Fatal("contained stop was incorrectly treated as proof that external writes are repeat-safe")
	}
}

func TestLaunchAndTombstoneAreSerializedPerExecution(t *testing.T) {
	backend := newBackendFake()
	backend.startEntered = make(chan struct{})
	backend.startRelease = make(chan struct{})
	manager := testManager(t, t.TempDir(), backend)
	material := testMaterial()
	reservation := testReservation(t, manager, material)
	identity := effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID}
	launchDone := make(chan error, 1)
	go func() {
		_, err := manager.Launch(context.Background(), reservation, material)
		launchDone <- err
	}()
	<-backend.startEntered
	revokeDone := make(chan error, 1)
	go func() {
		_, err := manager.Revoke(context.Background(), reservation, identity)
		revokeDone <- err
	}()
	select {
	case err := <-revokeDone:
		t.Fatalf("revoke crossed a paused launch lock: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(backend.startRelease)
	if err := <-launchDone; err != nil {
		t.Fatal(err)
	}
	if err := <-revokeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Launch(context.Background(), reservation, material); err == nil {
		t.Fatal("launch succeeded after serialized tombstone")
	}
}

func TestJournalAuthenticationRejectsTampering(t *testing.T) {
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	material := testMaterial()
	reservation := testReservation(t, manager, material)
	identity, err := manager.Launch(context.Background(), reservation, material)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256DirectoryName(reservation.SupervisorExecutionID)
	path := manager.root + string(os.PathSeparator) + digest + string(os.PathSeparator) + "journal.json"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	record := envelope["record"].(map[string]any)
	record["inputDigest"] = effect.DigestInput([]byte("tampered"))
	data, _ = json.Marshal(envelope)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Query(context.Background(), reservation, identity); err == nil {
		t.Fatal("tampered journal was trusted")
	}
}

func sha256DirectoryName(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
