package effect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu           sync.Mutex
	nextID       int
	records      map[string]Record
	resources    map[string]string
	reserveCalls int
	completeErr  error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{records: map[string]Record{}, resources: map[string]string{}}
}

func (s *memoryStore) Reserve(_ context.Context, reservation Reservation) (ReservationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserveCalls++
	for id, record := range s.records {
		if record.Reservation.OperationClaim.OperationID == reservation.OperationClaim.OperationID &&
			record.Reservation.Stage == reservation.Stage && record.Reservation.InputDigest == reservation.InputDigest &&
			record.Lifecycle != LifecycleResolved {
			return ReservationResult{Record: record}, nil
		}
		if record.Reservation.Resource == reservation.Resource &&
			record.Lifecycle != LifecycleCompleted && record.Lifecycle != LifecycleResolved {
			return ReservationResult{}, &ResourceBlockedError{Resource: reservation.Resource, BlockingEffectID: id}
		}
	}
	s.nextID++
	token := Token{EffectID: fmt.Sprintf("effect-%d", s.nextID), Generation: int64(s.nextID)}
	record := Record{Token: token, Reservation: reservation, Lifecycle: LifecycleReserved}
	s.records[token.EffectID] = record
	s.resources[reservation.Resource] = token.EffectID
	return ReservationResult{Record: record, Created: true}, nil
}

func (s *memoryStore) MarkLaunched(_ context.Context, token Token, identity ExecutionIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[token.EffectID]
	if !ok || record.Token != token || record.Lifecycle != LifecycleReserved {
		return ErrStaleToken
	}
	record.Lifecycle = LifecycleLaunched
	record.Execution = identity
	s.records[token.EffectID] = record
	return nil
}

func (s *memoryStore) Complete(_ context.Context, token Token, completion Completion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completeErr != nil {
		return s.completeErr
	}
	record, ok := s.records[token.EffectID]
	if !ok || record.Token != token || (record.Lifecycle != LifecycleReserved && record.Lifecycle != LifecycleLaunched) {
		return ErrStaleToken
	}
	record.Lifecycle = LifecycleCompleted
	record.Completion = &completion
	s.records[token.EffectID] = record
	delete(s.resources, record.Reservation.Resource)
	return nil
}

func (s *memoryStore) Resolve(_ context.Context, token Token, _ Resolution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[token.EffectID]
	if !ok || record.Token != token || (record.Lifecycle != LifecycleReserved && record.Lifecycle != LifecycleLaunched) {
		return ErrStaleToken
	}
	record.Lifecycle = LifecycleResolved
	s.records[token.EffectID] = record
	delete(s.resources, record.Reservation.Resource)
	return nil
}

type supervisorExecution struct {
	identity ExecutionIdentity
	phase    SupervisorPhase
	output   []byte
}

type memorySupervisor struct {
	mu            sync.Mutex
	executions    map[string]supervisorExecution
	tombstones    map[string]bool
	launches      map[string]int
	loseResponse  map[string]bool
	queries       map[string]int
	retrievals    map[string]int
	blockLaunch   map[string]chan struct{}
	launchEntered map[string]chan struct{}
}

type privateMemorySupervisor struct {
	*memorySupervisor
	private any
}

func (s *privateMemorySupervisor) LaunchPrivate(ctx context.Context, reservation Reservation, material any) (ExecutionIdentity, error) {
	s.private = material
	return s.Launch(ctx, reservation, LaunchMaterial{})
}

func newMemorySupervisor() *memorySupervisor {
	return &memorySupervisor{
		executions:    map[string]supervisorExecution{},
		tombstones:    map[string]bool{},
		launches:      map[string]int{},
		loseResponse:  map[string]bool{},
		queries:       map[string]int{},
		retrievals:    map[string]int{},
		blockLaunch:   map[string]chan struct{}{},
		launchEntered: map[string]chan struct{}{},
	}
}

func TestExecutorPassesPrivateLaunchMaterialOnlyToPrivateSupervisor(t *testing.T) {
	supervisor := &privateMemorySupervisor{memorySupervisor: newMemorySupervisor()}
	executor := &Executor{Store: newMemoryStore(), Supervisor: supervisor, Verifier: memoryVerifier{}}
	request := ExecuteRequest{Reservation: testReservation(t, "private-launch", "app/demo/snapshot", "private-execution", "snapshot", 1)}
	secret := struct{ Password string }{Password: "not-durable"}
	request.PrivateLaunch = secret
	result, err := executor.Execute(context.Background(), request)
	if err != nil || result.Outcome != OutcomeSucceeded {
		t.Fatalf("private execution = %+v, %v", result, err)
	}
	if got, ok := supervisor.private.(struct{ Password string }); !ok || got != secret {
		t.Fatalf("private launch material = %#v", supervisor.private)
	}
}

func (s *memorySupervisor) Prepare(_ context.Context, _ Reservation) error { return nil }

func (s *memorySupervisor) Launch(_ context.Context, reservation Reservation, _ LaunchMaterial) (ExecutionIdentity, error) {
	s.mu.Lock()
	if entered := s.launchEntered[reservation.SupervisorExecutionID]; entered != nil {
		close(entered)
		delete(s.launchEntered, reservation.SupervisorExecutionID)
	}
	block := s.blockLaunch[reservation.SupervisorExecutionID]
	s.mu.Unlock()
	if block != nil {
		<-block
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tombstones[reservation.SupervisorExecutionID] {
		return ExecutionIdentity{}, fmt.Errorf("supervisor execution id is revoked")
	}
	if execution, ok := s.executions[reservation.SupervisorExecutionID]; ok {
		return execution.identity, nil
	}
	s.launches[reservation.SupervisorExecutionID]++
	identity := ExecutionIdentity{
		Supervisor:            reservation.Supervisor,
		SupervisorExecutionID: reservation.SupervisorExecutionID,
		RuntimeInstanceID:     "runtime-" + reservation.SupervisorExecutionID,
	}
	s.executions[reservation.SupervisorExecutionID] = supervisorExecution{
		identity: identity,
		phase:    SupervisorSucceeded,
		output:   []byte("result-" + reservation.SupervisorExecutionID),
	}
	if s.loseResponse[reservation.SupervisorExecutionID] {
		delete(s.loseResponse, reservation.SupervisorExecutionID)
		return ExecutionIdentity{}, fmt.Errorf("launch response lost")
	}
	return identity, nil
}

func (s *memorySupervisor) Query(_ context.Context, _ Reservation, identity ExecutionIdentity) (Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries[identity.SupervisorExecutionID]++
	execution, ok := s.executions[identity.SupervisorExecutionID]
	if !ok {
		return Observation{
			Identity: identity,
			Phase:    SupervisorNotFound,
			Evidence: RawEvidence{Source: "memory-supervisor", Reference: "query/" + identity.SupervisorExecutionID},
		}, nil
	}
	return Observation{
		Identity: execution.identity,
		Phase:    execution.phase,
		Output:   append([]byte(nil), execution.output...),
		Evidence: RawEvidence{Source: "memory-supervisor", Reference: "query/" + identity.SupervisorExecutionID},
	}, nil
}

func (s *memorySupervisor) Revoke(_ context.Context, _ Reservation, identity ExecutionIdentity) (Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tombstones[identity.SupervisorExecutionID] = true
	delete(s.executions, identity.SupervisorExecutionID)
	return Observation{
		Identity: identity,
		Phase:    SupervisorNotFound,
		Evidence: RawEvidence{Source: "memory-supervisor", Reference: "tombstone/" + identity.SupervisorExecutionID},
	}, nil
}

func (s *memorySupervisor) RetrieveResult(_ context.Context, _ Reservation, identity ExecutionIdentity, _ string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retrievals[identity.SupervisorExecutionID]++
	execution, ok := s.executions[identity.SupervisorExecutionID]
	if !ok {
		return nil, fmt.Errorf("execution result unavailable")
	}
	return append([]byte(nil), execution.output...), nil
}

type memoryVerifier struct {
	allowFailed bool
}

func (v memoryVerifier) Verify(_ context.Context, record Record, observation Observation) (Verification, error) {
	decision := VerificationDecision("")
	switch observation.Phase {
	case SupervisorSucceeded:
		decision = VerificationSucceeded
	case SupervisorFailed:
		if !v.allowFailed {
			return Verification{}, fmt.Errorf("failed command may have external writes")
		}
		decision = VerificationFailedRepeatSafe
	case SupervisorNotFound:
		decision = VerificationNeverLaunched
	case SupervisorStopped:
		decision = VerificationStoppedRepeatSafe
	default:
		return Verification{}, fmt.Errorf("nonterminal observation")
	}
	return Verification{
		Decision:              decision,
		InputDigest:           record.Reservation.InputDigest,
		ResultDigest:          DigestInput(observation.Output),
		ResultReference:       "result/" + observation.Identity.SupervisorExecutionID,
		SupervisorExecutionID: observation.Identity.SupervisorExecutionID,
		RuntimeInstanceID:     observation.Identity.RuntimeInstanceID,
		EvidenceSource:        observation.Evidence.Source,
		EvidenceReference:     observation.Evidence.Reference,
		ObservedAt:            time.Now().UTC(),
	}, nil
}

func testReservation(t *testing.T, operationID, resource, executionID, command string, generation int64) Reservation {
	t.Helper()
	reservation := Reservation{
		Authority:             "control-authority",
		Resource:              resource,
		OperationClaim:        OperationClaim{OperationID: operationID, OwnerID: "worker", Generation: generation},
		Stage:                 "test",
		Supervisor:            "test-supervisor",
		SupervisorExecutionID: executionID,
		LaunchPayload:         json.RawMessage(fmt.Sprintf("{\"command\":%q}", command)),
	}
	digest, err := ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	reservation.InputDigest = digest
	return reservation
}

func TestExecutorCompletesAndRetrievesSameOperationResult(t *testing.T) {
	store := newMemoryStore()
	supervisor := newMemorySupervisor()
	executor := &Executor{Store: store, Supervisor: supervisor, Verifier: memoryVerifier{}}
	reservation := testReservation(t, "operation-1", "app/api/test", "execution-1", "go test ./...", 1)

	first, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: reservation})
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != OutcomeSucceeded || first.Reused || string(first.Output) != "result-execution-1" {
		t.Fatalf("unexpected first result: %+v", first)
	}

	reservation.OperationClaim.Generation = 2
	second, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: reservation})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || string(second.Output) != string(first.Output) || second.ResultDigest != first.ResultDigest {
		t.Fatalf("completed replay did not retrieve the verified result: first=%+v second=%+v", first, second)
	}
	if supervisor.launches["execution-1"] != 1 || supervisor.retrievals["execution-1"] != 1 {
		t.Fatalf("completed replay relaunched or failed to retrieve: launches=%d retrievals=%d", supervisor.launches["execution-1"], supervisor.retrievals["execution-1"])
	}
}

func TestExecutorRejectsChangedPayloadWithCopiedDigestBeforeReserve(t *testing.T) {
	store := newMemoryStore()
	supervisor := newMemorySupervisor()
	executor := &Executor{Store: store, Supervisor: supervisor, Verifier: memoryVerifier{}}
	reservation := testReservation(t, "operation-1", "app/api/test", "execution-1", "safe command", 1)
	reservation.LaunchPayload = json.RawMessage("{\"command\":\"changed command\"}")

	if _, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: reservation}); err == nil {
		t.Fatal("expected copied digest with changed launch payload to fail")
	}
	if store.reserveCalls != 0 || len(supervisor.launches) != 0 {
		t.Fatalf("changed payload reached reservation or launch: reserve=%d launches=%v", store.reserveCalls, supervisor.launches)
	}
}

func TestExecutorQueriesOriginalExecutionAfterAmbiguousLaunch(t *testing.T) {
	store := newMemoryStore()
	supervisor := newMemorySupervisor()
	supervisor.loseResponse["execution-1"] = true
	executor := &Executor{Store: store, Supervisor: supervisor, Verifier: memoryVerifier{}}
	reservation := testReservation(t, "operation-1", "app/api/test", "execution-1", "go test ./...", 1)

	if _, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: reservation}); !errors.Is(err, ErrEffectPending) {
		t.Fatalf("expected ambiguous launch to remain pending, got %v", err)
	}
	reservation.OperationClaim.Generation = 2
	result, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: reservation})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeSucceeded || string(result.Output) != "result-execution-1" {
		t.Fatalf("recovery did not return the original execution result: %+v", result)
	}
	if supervisor.launches["execution-1"] != 1 {
		t.Fatalf("ambiguous launch was duplicated: %d", supervisor.launches["execution-1"])
	}
}

func TestExecutorDoesNotReleaseUnverifiedFailedEffect(t *testing.T) {
	store := newMemoryStore()
	supervisor := newMemorySupervisor()
	reservation := testReservation(t, "operation-1", "app/api/test", "execution-1", "remote-write-test", 1)
	identity := ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: "runtime-execution-1"}
	supervisor.executions["execution-1"] = supervisorExecution{identity: identity, phase: SupervisorFailed, output: []byte("failed")}
	executor := &Executor{Store: store, Supervisor: supervisor, Verifier: memoryVerifier{}}

	_, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: reservation})
	if !errors.Is(err, ErrEffectPending) {
		t.Fatalf("expected unresolved failure, got %v", err)
	}
	record := store.records["effect-1"]
	if record.Lifecycle == LifecycleCompleted || record.Lifecycle == LifecycleResolved {
		t.Fatalf("unverified failure released the resource: %+v", record)
	}
}

func TestExecutorDoesNotAcceptTerminalEvidenceFromDifferentRuntime(t *testing.T) {
	store := newMemoryStore()
	supervisor := newMemorySupervisor()
	executor := &Executor{Store: store, Supervisor: supervisor, Verifier: memoryVerifier{allowFailed: true}}
	reservation := testReservation(t, "operation-1", "app/api/test", "execution-1", "remote-write-test", 1)
	reserved, err := store.Reserve(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	durableIdentity := ExecutionIdentity{
		Supervisor:            reservation.Supervisor,
		SupervisorExecutionID: reservation.SupervisorExecutionID,
		RuntimeInstanceID:     "runtime-original",
	}
	if err := store.MarkLaunched(context.Background(), reserved.Record.Token, durableIdentity); err != nil {
		t.Fatal(err)
	}
	supervisor.executions[reservation.SupervisorExecutionID] = supervisorExecution{
		identity: ExecutionIdentity{
			Supervisor:            reservation.Supervisor,
			SupervisorExecutionID: reservation.SupervisorExecutionID,
			RuntimeInstanceID:     "runtime-replaced",
		},
		phase:  SupervisorFailed,
		output: []byte("failed"),
	}

	if _, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: reservation}); !errors.Is(err, ErrEffectPending) {
		t.Fatalf("expected mismatched runtime evidence to remain pending, got %v", err)
	}
	if got := store.records[reserved.Record.Token.EffectID]; got.Lifecycle != LifecycleLaunched || got.Execution.RuntimeInstanceID != "runtime-original" {
		t.Fatalf("mismatched runtime evidence changed the durable reservation: %+v", got)
	}
}

func TestRevocationPreventsPausedLaunchCrossingReleasedFence(t *testing.T) {
	store := newMemoryStore()
	supervisor := newMemorySupervisor()
	executor := &Executor{Store: store, Supervisor: supervisor, Verifier: memoryVerifier{}}
	first := testReservation(t, "operation-1", "app/api/test", "execution-old", "go test ./...", 1)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	supervisor.launchEntered[first.SupervisorExecutionID] = entered
	supervisor.blockLaunch[first.SupervisorExecutionID] = unblock

	firstDone := make(chan error, 1)
	go func() {
		_, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: first})
		firstDone <- err
	}()
	<-entered

	recovery := first
	recovery.OperationClaim.Generation = 2
	if _, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: recovery}); !errors.Is(err, ErrEffectPending) {
		t.Fatalf("expected recovery to record a tombstoned resolution, got %v", err)
	}
	if !supervisor.tombstones[first.SupervisorExecutionID] {
		t.Fatal("recovery released the resource without tombstoning the execution id")
	}

	successor := testReservation(t, "operation-1", "app/api/test", "execution-successor", "go test ./...", 3)
	if _, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: successor}); err != nil {
		t.Fatalf("verified resolution did not permit a successor: %v", err)
	}

	close(unblock)
	if err := <-firstDone; !errors.Is(err, ErrEffectPending) {
		t.Fatalf("paused stale launch should fail after revocation, got %v", err)
	}
	if supervisor.launches[first.SupervisorExecutionID] != 0 {
		t.Fatalf("revoked stale execution launched: %d", supervisor.launches[first.SupervisorExecutionID])
	}
	if supervisor.launches[successor.SupervisorExecutionID] != 1 {
		t.Fatalf("successor did not launch exactly once: %d", supervisor.launches[successor.SupervisorExecutionID])
	}
}

func TestStaleCompletionCannotReleaseReservation(t *testing.T) {
	store := newMemoryStore()
	store.completeErr = ErrStaleToken
	supervisor := newMemorySupervisor()
	executor := &Executor{Store: store, Supervisor: supervisor, Verifier: memoryVerifier{}}
	reservation := testReservation(t, "operation-1", "app/api/test", "execution-1", "go test ./...", 1)

	_, err := executor.Execute(context.Background(), ExecuteRequest{Reservation: reservation})
	if !errors.Is(err, ErrEffectPending) {
		t.Fatalf("expected stale completion to remain pending, got %v", err)
	}
	if store.records["effect-1"].Lifecycle != LifecycleLaunched {
		t.Fatalf("stale completion released the reservation: %+v", store.records["effect-1"])
	}
}

func TestResourceGateBlocksOnlyMatchingResource(t *testing.T) {
	store := newMemoryStore()
	first := testReservation(t, "operation-1", "app/api/test", "execution-1", "test", 1)
	if _, err := store.Reserve(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	blocked := testReservation(t, "operation-2", "app/api/test", "execution-2", "test", 1)
	if _, err := store.Reserve(context.Background(), blocked); !errors.Is(err, ErrResourceBlocked) {
		t.Fatalf("expected matching resource to be blocked, got %v", err)
	}
	unrelated := testReservation(t, "operation-3", "app/worker/test", "execution-3", "test", 1)
	if _, err := store.Reserve(context.Background(), unrelated); err != nil {
		t.Fatalf("unrelated resource was blocked: %v", err)
	}
}
