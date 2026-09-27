package worker

import (
	"context"
	"errors"
	"sync"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

type functionJobAttemptsFake struct {
	mu        sync.Mutex
	recorded  bool
	attempted bool
	target    string
	digest    string
}

func (f *functionJobAttemptsFake) RecordFunctionInvocationEffectStage(_ context.Context, _ store.OperationClaim, stage store.FunctionInvocationEffectAttemptStage, target, digest string) (store.FunctionInvocationEffectAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if stage != store.FunctionInvocationJobAttempt || (f.recorded && (f.target != target || f.digest != digest)) {
		return store.FunctionInvocationEffectAttempt{}, store.ErrFunctionInvocationEffectConflict
	}
	f.recorded, f.target, f.digest = true, target, digest
	return store.FunctionInvocationEffectAttempt{Target: target, InputDigest: digest, Attempted: f.attempted}, nil
}

func (f *functionJobAttemptsFake) MarkFunctionInvocationEffectAttempt(_ context.Context, _ store.OperationClaim, stage store.FunctionInvocationEffectAttemptStage, target, digest string) (store.FunctionInvocationEffectAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if stage != store.FunctionInvocationJobAttempt || !f.recorded || f.target != target || f.digest != digest {
		return store.FunctionInvocationEffectAttempt{}, store.ErrFunctionInvocationEffectConflict
	}
	markedNow := !f.attempted
	f.attempted = true
	return store.FunctionInvocationEffectAttempt{Target: target, InputDigest: digest, Attempted: true, MarkedNow: markedNow}, nil
}

type functionJobRemoteFake struct {
	mu          sync.Mutex
	observation nomad.FunctionInvocationJobObservation
	creates     int
	createError error
}

func (f *functionJobRemoteFake) LookupFunctionInvocationJob(_ context.Context, _ string, _ nomad.FunctionInvocationJobIdentity) (nomad.FunctionInvocationJobObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observation.State == "" {
		return nomad.FunctionInvocationJobObservation{State: nomad.FunctionInvocationJobNotFound}, nil
	}
	return f.observation, nil
}

func (f *functionJobRemoteFake) CreateFunctionInvocationJob(_ context.Context, _ string, job *nomadapi.Job, digest nomad.FunctionInvocationJobDigest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if f.observation.State == "" {
		version := uint64(0)
		f.observation = nomad.FunctionInvocationJobObservation{
			State: nomad.FunctionInvocationJobFound, JobID: *job.ID,
			OwnerMarker: job.Meta["norn.function-invocation.owner"], JobSpecDigest: digest.Digest,
			JobVersion: &version, ModifyIndex: 17, EvaluationIDs: []string{"eval-1"}, HistoryComplete: true,
		}
	}
	return f.createError
}

func TestEnsureFunctionInvocationJobRecoversLostResponseAndNeverResubmits(t *testing.T) {
	identity, job, err := BuildFunctionInvocationJobPlan(testFunctionJobPlanInput(), "./function", 250, 192)
	if err != nil {
		t.Fatal(err)
	}
	attempts := &functionJobAttemptsFake{}
	remote := &functionJobRemoteFake{createError: errors.New("lost response")}
	claim := functionVariableStepClaim(t, identity)
	for i := 0; i < 2; i++ {
		decision, err := EnsureFunctionInvocationJob(context.Background(), attempts, remote, claim, "global", identity, job)
		if err != nil || decision.Action != FunctionJobRecovered {
			t.Fatalf("run %d: decision=%+v err=%v", i, decision, err)
		}
	}
	if remote.creates != 1 {
		t.Fatalf("creates=%d, want one", remote.creates)
	}
	remote.mu.Lock()
	remote.observation = nomad.FunctionInvocationJobObservation{}
	remote.mu.Unlock()
	decision, err := EnsureFunctionInvocationJob(context.Background(), attempts, remote, claim, "global", identity, job)
	if err != nil || decision.Action != FunctionJobUnresolved || remote.creates != 1 {
		t.Fatalf("post-attempt absence: decision=%+v err=%v creates=%d", decision, err, remote.creates)
	}
}

func TestEnsureFunctionInvocationJobRejectsMismatchedPlanBeforeEffect(t *testing.T) {
	identity, job, err := BuildFunctionInvocationJobPlan(testFunctionJobPlanInput(), "./function", 250, 192)
	if err != nil {
		t.Fatal(err)
	}
	job.TaskGroups[0].Tasks[0].Config["args"] = []string{"-c", "./different"}
	attempts, remote := &functionJobAttemptsFake{}, &functionJobRemoteFake{}
	_, err = EnsureFunctionInvocationJob(context.Background(), attempts, remote, functionVariableStepClaim(t, identity), "global", identity, job)
	if err == nil || attempts.recorded || remote.creates != 0 {
		t.Fatalf("mismatched plan reached effect: err=%v recorded=%t creates=%d", err, attempts.recorded, remote.creates)
	}
}

func TestEnsureFunctionInvocationJobConcurrentCallersSubmitOnce(t *testing.T) {
	identity, job, err := BuildFunctionInvocationJobPlan(testFunctionJobPlanInput(), "./function", 250, 192)
	if err != nil {
		t.Fatal(err)
	}
	attempts, remote := &functionJobAttemptsFake{}, &functionJobRemoteFake{}
	claim := functionVariableStepClaim(t, identity)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			decision, err := EnsureFunctionInvocationJob(context.Background(), attempts, remote, claim, "global", identity, job)
			if err == nil && decision.Action != FunctionJobRecovered && decision.Action != FunctionJobUnresolved {
				err = errors.New("unexpected function job decision")
			}
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if remote.creates != 1 {
		t.Fatalf("remote creates=%d, want one", remote.creates)
	}
}
