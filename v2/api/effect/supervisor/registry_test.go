package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"norn/v2/api/effect"
)

// recordingStore is a minimal effect.Store for executor-level recovery tests.
type recordingStore struct {
	mu      sync.Mutex
	records map[string]effect.Record
	next    int
}

func newRecordingStore() *recordingStore {
	return &recordingStore{records: map[string]effect.Record{}}
}

func (s *recordingStore) Reserve(_ context.Context, reservation effect.Reservation) (effect.ReservationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.Reservation.OperationClaim.OperationID == reservation.OperationClaim.OperationID && record.Reservation.InputDigest == reservation.InputDigest && record.Lifecycle != effect.LifecycleResolved {
			return effect.ReservationResult{Record: record}, nil
		}
	}
	s.next++
	record := effect.Record{Token: effect.Token{EffectID: fmt.Sprintf("effect-%d", s.next), Generation: reservation.OperationClaim.Generation}, Reservation: reservation, Lifecycle: effect.LifecycleReserved}
	s.records[record.Token.EffectID] = record
	return effect.ReservationResult{Record: record, Created: true}, nil
}

func (s *recordingStore) MarkLaunched(_ context.Context, token effect.Token, identity effect.ExecutionIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[token.EffectID]
	record.Lifecycle, record.Execution = effect.LifecycleLaunched, identity
	s.records[token.EffectID] = record
	return nil
}

func (s *recordingStore) Complete(_ context.Context, token effect.Token, completion effect.Completion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[token.EffectID]
	record.Lifecycle, record.Completion = effect.LifecycleCompleted, &completion
	s.records[token.EffectID] = record
	return nil
}

func (s *recordingStore) Resolve(_ context.Context, token effect.Token, _ effect.Resolution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[token.EffectID]
	record.Lifecycle = effect.LifecycleResolved
	s.records[token.EffectID] = record
	return nil
}

func (s *recordingStore) only(t *testing.T) effect.Record {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) != 1 {
		t.Fatalf("records = %d", len(s.records))
	}
	for _, record := range s.records {
		return record
	}
	return effect.Record{}
}

func testExecutor(t *testing.T, manager *Manager, store effect.Store) *effect.Executor {
	t.Helper()
	verifier, err := NewVerifier(manager)
	if err != nil {
		t.Fatal(err)
	}
	return &effect.Executor{Store: store, Supervisor: manager, Verifier: verifier}
}

func executionDirectory(manager *Manager, executionID string) string {
	return filepath.Join(manager.root, sha256DirectoryName(executionID))
}

func TestConcurrentRootInitializationYieldsOneRegistry(t *testing.T) {
	root := t.TempDir()
	const workers = 8
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var start sync.WaitGroup
	start.Add(1)
	for index := 0; index < workers; index++ {
		go func() {
			start.Wait()
			manager, err := NewManager(root, testSigningKey, newBackendFake())
			if err != nil {
				errs <- err
				return
			}
			ids <- manager.rootID
		}()
	}
	start.Done()
	var first string
	for index := 0; index < workers; index++ {
		select {
		case err := <-errs:
			t.Fatalf("concurrent initialization failed: %v", err)
		case id := <-ids:
			if first == "" {
				first = id
			} else if id != first {
				t.Fatalf("concurrent initialization created two roots: %s %s", first, id)
			}
		}
	}
}

func TestRegistryLossOrTamperingFailsClosed(t *testing.T) {
	prepared := func(t *testing.T) string {
		root := t.TempDir()
		manager := testManager(t, root, newBackendFake())
		testReservation(t, manager, testMaterial())
		return root
	}
	for name, damage := range map[string]func(t *testing.T, root string){
		"deleted":   func(t *testing.T, root string) { _ = os.Remove(filepath.Join(root, "registry.json")) },
		"truncated": func(t *testing.T, root string) { _ = os.Truncate(filepath.Join(root, "registry.json"), 10) },
		"edited": func(t *testing.T, root string) {
			path := filepath.Join(root, "registry.json")
			data, _ := os.ReadFile(path)
			_ = os.WriteFile(path, []byte(strings.Replace(string(data), "execution-1", "execution-2", 1)), 0o600)
		},
		"symlinked": func(t *testing.T, root string) {
			path := filepath.Join(root, "registry.json")
			moved := filepath.Join(t.TempDir(), "registry.json")
			_ = os.Rename(path, moved)
			_ = os.Symlink(moved, path)
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := prepared(t)
			damage(t, root)
			if _, err := NewManager(root, testSigningKey, newBackendFake()); err == nil {
				t.Fatal("damaged registry was accepted or silently recreated")
			}
		})
	}
	t.Run("other key", func(t *testing.T) {
		root := prepared(t)
		if _, err := NewManager(root, []byte(strings.Repeat("z", 32)), newBackendFake()); err == nil {
			t.Fatal("registry authenticated under another key")
		}
	})
}

func TestExecuteReplayAfterLaunchedJournalLossStaysPending(t *testing.T) {
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	store := newRecordingStore()
	executor := testExecutor(t, manager, store)
	reservation := testReservation(t, manager, testMaterial())
	if _, err := executor.Execute(context.Background(), effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: testMaterial()}); !errors.Is(err, effect.ErrEffectPending) {
		t.Fatalf("running launch = %v, want pending", err)
	}
	launched := store.only(t)
	if launched.Lifecycle != effect.LifecycleLaunched || backend.starts != 1 {
		t.Fatalf("launch record=%+v starts=%d", launched, backend.starts)
	}
	directory := executionDirectory(manager, reservation.SupervisorExecutionID)
	if err := os.Remove(filepath.Join(directory, "journal.json")); err != nil {
		t.Fatal(err)
	}
	reservation.OperationClaim.Generation = 2
	for attempt := 0; attempt < 2; attempt++ {
		_, err := executor.Execute(context.Background(), effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: testMaterial()})
		if !errors.Is(err, effect.ErrEffectPending) {
			t.Fatalf("replay after journal loss = %v, want pending", err)
		}
	}
	if _, err := executor.Recover(context.Background(), store.only(t)); !errors.Is(err, effect.ErrEffectPending) {
		t.Fatalf("recovery after journal loss = %v, want pending", err)
	}
	if record := store.only(t); record.Lifecycle != effect.LifecycleLaunched || backend.starts != 1 {
		t.Fatalf("journal loss changed outcome: record=%+v starts=%d", record, backend.starts)
	}
	if _, err := os.Stat(filepath.Join(directory, "journal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a missing launched journal was silently recreated")
	}
}

func TestRolledBackRegistryCannotReRegisterLaunchedExecutionAsNeverLaunched(t *testing.T) {
	root := t.TempDir()
	backend := newBackendFake()
	manager := testManager(t, root, backend)
	store := newRecordingStore()
	executor := testExecutor(t, manager, store)
	before, err := os.ReadFile(filepath.Join(root, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	material := testMaterial()
	reservation := testReservation(t, manager, material)
	if _, err := executor.Execute(context.Background(), effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: material}); !errors.Is(err, effect.ErrEffectPending) {
		t.Fatalf("launch = %v", err)
	}
	directory := executionDirectory(manager, reservation.SupervisorExecutionID)
	if err := os.Remove(filepath.Join(directory, "journal.json")); err != nil {
		t.Fatal(err)
	}
	// Roll the authenticated root registry back to before registration.
	if err := os.WriteFile(filepath.Join(root, "registry.json"), before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), reservation); err == nil {
		t.Fatal("launched execution was re-registered as fresh after registry rollback")
	}
	identity := effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID}
	if observation, err := manager.Query(context.Background(), reservation, identity); err == nil {
		t.Fatalf("query after rollback returned %+v", observation)
	}
	reservation.OperationClaim.Generation = 2
	if _, err := executor.Execute(context.Background(), effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: material}); !errors.Is(err, effect.ErrEffectPending) {
		t.Fatalf("replay after rollback = %v, want pending", err)
	}
	if record := store.only(t); record.Lifecycle != effect.LifecycleLaunched || backend.starts != 1 {
		t.Fatalf("rollback changed outcome: record=%+v starts=%d", record, backend.starts)
	}
}

func TestRegistryRecordsLaunchRuntimeAndRejectsForeignJournal(t *testing.T) {
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	material := testMaterial()
	first := testReservation(t, manager, material)
	identity, err := manager.Launch(context.Background(), first, material)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := manager.registryEntry(first.SupervisorExecutionID)
	if err != nil || entry == nil || entry.RuntimeInstanceID != identity.RuntimeInstanceID {
		t.Fatalf("registry entry=%+v err=%v", entry, err)
	}
	if again, err := manager.Launch(context.Background(), first, material); err != nil || again != identity || backend.starts != 1 {
		t.Fatalf("repeat launch identity=%+v err=%v starts=%d", again, err, backend.starts)
	}
	// A journal copied from another launched execution does not satisfy this
	// execution's registered runtime.
	second := first
	second.SupervisorExecutionID = "execution-2"
	if err := manager.Prepare(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Launch(context.Background(), second, material); err != nil {
		t.Fatal(err)
	}
	firstJournal, err := os.ReadFile(filepath.Join(executionDirectory(manager, first.SupervisorExecutionID), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(executionDirectory(manager, second.SupervisorExecutionID), "journal.json"), firstJournal, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Query(context.Background(), second, effect.ExecutionIdentity{Supervisor: second.Supervisor, SupervisorExecutionID: second.SupervisorExecutionID}); err == nil {
		t.Fatal("foreign journal accepted for a registered launch")
	}
}

func TestContainedFailureCompletesAndIsReusedWithoutRelaunch(t *testing.T) {
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	store := newRecordingStore()
	executor := testExecutor(t, manager, store)
	material := testMaterial()
	reservation := testReservation(t, manager, material)
	if _, err := executor.Execute(context.Background(), effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: material}); !errors.Is(err, effect.ErrEffectPending) {
		t.Fatalf("launch = %v", err)
	}
	exit := 2
	backend.mu.Lock()
	backend.states[reservation.SupervisorExecutionID] = BackendState{Phase: effect.SupervisorFailed, ExitCode: &exit, Output: []byte("FAIL"), ContainmentProven: true, EvidenceReference: "cgroup-v2/test"}
	backend.mu.Unlock()
	reservation.OperationClaim.Generation = 2
	result, err := executor.Execute(context.Background(), effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: material})
	if err != nil || result.Outcome != effect.OutcomeFailed || result.ExitCode == nil || *result.ExitCode != 2 || string(result.Output) != "FAIL" {
		t.Fatalf("contained failure result=%+v err=%v", result, err)
	}
	if record := store.only(t); record.Lifecycle != effect.LifecycleCompleted || record.Completion.Verification.Decision != effect.VerificationFailed {
		t.Fatalf("failure record=%+v", record)
	}
	reservation.OperationClaim.Generation = 3
	replayed, err := executor.Execute(context.Background(), effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: material})
	if err != nil || !replayed.Reused || replayed.Outcome != effect.OutcomeFailed || string(replayed.Output) != "FAIL" || backend.starts != 1 {
		t.Fatalf("replay=%+v err=%v starts=%d", replayed, err, backend.starts)
	}

	// An uncontained failure (descendants still running) is not final.
	other := testReservation(t, manager, material)
	other.SupervisorExecutionID, other.OperationClaim.OperationID = "execution-uncontained", "operation-2"
	if err := manager.Prepare(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	identity, err := manager.Launch(context.Background(), other, material)
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.states[other.SupervisorExecutionID] = BackendState{Phase: effect.SupervisorFailed, ExitCode: &exit, ContainmentProven: false, EvidenceReference: "cgroup-v2/uncontained"}
	backend.mu.Unlock()
	observation, err := manager.Query(context.Background(), other, identity)
	if err != nil || observation.Phase != effect.SupervisorUnknown {
		t.Fatalf("uncontained failure observation=%+v err=%v", observation, err)
	}
}
