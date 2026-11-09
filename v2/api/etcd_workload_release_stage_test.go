package main

import (
	"context"
	"errors"
	"testing"

	"norn/v2/api/effect"
	"norn/v2/api/etcdstore"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
	workerruntime "norn/v2/api/worker"
)

type workloadReleaseStageNomadFake struct {
	stageErr      error
	afterStage    func()
	registerErr   error
	observation   nomad.DeploymentJobObservation
	stageCalls    int
	registerCalls int
	lookupCalls   int
	stagedSecrets map[string]string
	registered    nomad.CASDeploymentJobRequest
	lookedUp      nomad.CASDeploymentJobRequest
}

func (f *workloadReleaseStageNomadFake) StageManagedJobSecretInputs(_ string, _ nomad.ManagedJobInputRequirements, values map[string]string) error {
	f.stageCalls++
	f.stagedSecrets = make(map[string]string, len(values))
	for key, value := range values {
		f.stagedSecrets[key] = value
	}
	if f.afterStage != nil {
		f.afterStage()
	}
	return f.stageErr
}

func (f *workloadReleaseStageNomadFake) RegisterDeploymentJobCAS(_ context.Context, request nomad.CASDeploymentJobRequest) (string, error) {
	f.registerCalls++
	f.registered = request
	return "", f.registerErr
}

func (f *workloadReleaseStageNomadFake) LookupDeploymentJobRevision(_ context.Context, request nomad.CASDeploymentJobRequest) (nomad.DeploymentJobObservation, error) {
	f.lookupCalls++
	f.lookedUp = request
	return f.observation, nil
}

type workloadReleaseStageStoreFake struct {
	allowed       bool
	markerErr     error
	attempts      int
	bindings      int
	binding       pipeline.WorkloadReleaseStagedTarget
	lookupRequest nomad.CASDeploymentJobRequest
}

func (f *workloadReleaseStageStoreFake) MarkWorkloadReleaseStageTargetAttempt(context.Context, store.OperationClaim, store.AppOperationLock, string, string) (bool, error) {
	f.attempts++
	if f.markerErr != nil {
		return false, f.markerErr
	}
	allowed := f.allowed
	f.allowed = false
	return allowed, nil
}

func TestWorkloadReleaseStageClaimLossAfterPrivateInputWritePreventsAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	scheduler := &workloadReleaseStageNomadFake{afterStage: cancel}
	operations := &workloadReleaseStageStoreFake{allowed: true}
	err := stageAndBindWorkloadReleaseTarget(ctx, scheduler, operations,
		mustWorkloadReleaseTestClaim(t), workloadReleaseTestLock{ctx: ctx}, "stage-effect", "runtime-1",
		pipeline.WorkloadReleaseIntent{ID: "release-1", App: "web", Region: "nyc3", TargetReplicas: 1},
		&workerruntime.WorkloadReleaseTargetInputs{
			JobPlan: workerruntime.WorkloadReleaseTargetJobPlan{Request: nomad.CASDeploymentJobRequest{JobID: "target-job"}, Inputs: &nomad.ManagedJobInputRequirements{}},
			Secrets: map[string]string{"API_TOKEN": "plaintext"},
		})
	if !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("claim-loss error=%v, want ownership lost", err)
	}
	if scheduler.stageCalls != 1 || scheduler.registerCalls != 0 || operations.attempts != 0 || operations.bindings != 0 {
		t.Fatalf("claim loss advanced target: stage=%d register=%d marker=%d bind=%d",
			scheduler.stageCalls, scheduler.registerCalls, operations.attempts, operations.bindings)
	}
}

func TestWorkloadReleaseStageMarkerFailurePreventsCreate(t *testing.T) {
	wantErr := errors.New("marker transaction unavailable")
	scheduler := &workloadReleaseStageNomadFake{}
	operations := &workloadReleaseStageStoreFake{allowed: true, markerErr: wantErr}
	inputs := &workerruntime.WorkloadReleaseTargetInputs{
		JobPlan: workerruntime.WorkloadReleaseTargetJobPlan{Request: nomad.CASDeploymentJobRequest{JobID: "target-job"}, Inputs: &nomad.ManagedJobInputRequirements{}},
		Secrets: map[string]string{"API_TOKEN": "plaintext"},
	}
	err := stageAndBindWorkloadReleaseTarget(context.Background(), scheduler, operations,
		mustWorkloadReleaseTestClaim(t), workloadReleaseTestLock{ctx: context.Background()}, "stage-effect", "runtime-1",
		pipeline.WorkloadReleaseIntent{ID: "release-1", App: "web", Region: "nyc3", TargetReplicas: 1}, inputs)
	if !errors.Is(err, wantErr) {
		t.Fatalf("marker error=%v, want marker transaction failure", err)
	}
	if scheduler.stageCalls != 1 || scheduler.registerCalls != 0 || operations.attempts != 1 || operations.bindings != 0 {
		t.Fatalf("marker failure advanced target: stage=%d register=%d marker=%d bind=%d",
			scheduler.stageCalls, scheduler.registerCalls, operations.attempts, operations.bindings)
	}
	if len(inputs.Secrets) != 0 {
		t.Fatalf("plaintext inputs were not cleared after marker failure: %#v", inputs.Secrets)
	}
}

func (f *workloadReleaseStageStoreFake) BindClaimedWorkloadReleaseTarget(ctx context.Context, _ store.OperationClaim, _ store.AppOperationLock, _ string,
	observer interface {
		LookupDeploymentJobRevision(context.Context, nomad.CASDeploymentJobRequest) (nomad.DeploymentJobObservation, error)
	}) (pipeline.WorkloadReleaseStagedTarget, error) {
	f.bindings++
	observation, err := observer.LookupDeploymentJobRevision(ctx, f.lookupRequest)
	if err != nil {
		return pipeline.WorkloadReleaseStagedTarget{}, err
	}
	if observation.State != nomad.DeploymentJobFound {
		return pipeline.WorkloadReleaseStagedTarget{}, etcdstore.ErrWorkloadReleaseTargetNotFound
	}
	f.binding = pipeline.WorkloadReleaseStagedTarget{TargetJobVersion: observation.Version,
		TargetJobModifyIndex: observation.JobModifyIndex, Observation: observation}
	return f.binding, nil
}

func TestWorkloadReleaseStageDoesNotCreateWhenPrivateInputsFail(t *testing.T) {
	wantErr := errors.New("variable write failed")
	scheduler := &workloadReleaseStageNomadFake{stageErr: wantErr}
	operations := &workloadReleaseStageStoreFake{allowed: true}
	secretValues := map[string]string{"API_TOKEN": "plaintext"}
	inputs := &workerruntime.WorkloadReleaseTargetInputs{
		JobPlan: workerruntime.WorkloadReleaseTargetJobPlan{
			Request: nomad.CASDeploymentJobRequest{JobID: "target-job"},
			Inputs:  &nomad.ManagedJobInputRequirements{},
		},
		Secrets: secretValues,
	}
	err := stageAndBindWorkloadReleaseTarget(context.Background(), scheduler, operations,
		mustWorkloadReleaseTestClaim(t), workloadReleaseTestLock{ctx: context.Background()}, "stage-effect", "runtime-1",
		pipeline.WorkloadReleaseIntent{ID: "release-1", App: "web", Region: "nyc3", TargetReplicas: 1}, inputs)
	var pending *effect.PendingError
	if !errors.As(err, &pending) || pending.Cause != wantErr {
		t.Fatalf("stage error=%v, want pending with cause", err)
	}
	if scheduler.stageCalls != 1 || scheduler.registerCalls != 0 || operations.attempts != 0 || operations.bindings != 0 {
		t.Fatalf("failed private staging advanced: stage=%d register=%d marker=%d bind=%d",
			scheduler.stageCalls, scheduler.registerCalls, operations.attempts, operations.bindings)
	}
	if len(secretValues) != 0 {
		t.Fatalf("plaintext inputs were not cleared after staging failure: %#v", secretValues)
	}
}

func TestWorkloadReleaseStageLostCreateResponseAdoptsExactTargetWithoutResubmitting(t *testing.T) {
	missing := etcdstore.ErrWorkloadReleaseTargetNotFound
	scheduler := &workloadReleaseStageNomadFake{registerErr: errors.New("response lost"),
		observation: nomad.DeploymentJobObservation{State: nomad.DeploymentJobNotFound}}
	claim := mustWorkloadReleaseTestClaim(t)
	lock := workloadReleaseTestLock{ctx: context.Background()}
	intent := pipeline.WorkloadReleaseIntent{ID: "release-1", App: "web", Region: "nyc3", NomadRegion: "global", TargetReplicas: 1}
	request := nomad.CASDeploymentJobRequest{JobID: "target-job", ExpectedJobModifyIndex: 0, DeploymentID: "target-deployment"}
	operations := &workloadReleaseStageStoreFake{allowed: true, lookupRequest: request}

	run := func() error {
		return stageAndBindWorkloadReleaseTarget(context.Background(), scheduler, operations, claim, lock, "stage-effect", "runtime-1",
			intent, &workerruntime.WorkloadReleaseTargetInputs{
				JobPlan: workerruntime.WorkloadReleaseTargetJobPlan{Request: request, Inputs: &nomad.ManagedJobInputRequirements{}},
				Secrets: map[string]string{"API_TOKEN": "plaintext"},
			})
	}
	err := run()
	var pending *effect.PendingError
	if !errors.As(err, &pending) || pending.Cause != missing {
		t.Fatalf("initial readback error=%v, want conservative pending result", err)
	}
	scheduler.observation = nomad.DeploymentJobObservation{State: nomad.DeploymentJobFound, Version: 7, JobModifyIndex: 42}
	if err := run(); err != nil {
		t.Fatalf("retry did not adopt the now-visible exact target: %v", err)
	}
	if scheduler.stageCalls != 2 || scheduler.registerCalls != 1 || operations.attempts != 2 ||
		operations.bindings != 2 || scheduler.lookupCalls != 2 || operations.binding.TargetJobVersion != 7 ||
		operations.binding.TargetJobModifyIndex != 42 {
		t.Fatalf("recovery did not bind the exact observation once: stage=%d register=%d marker=%d bind=%d lookup=%d binding=%+v",
			scheduler.stageCalls, scheduler.registerCalls, operations.attempts, operations.bindings, scheduler.lookupCalls, operations.binding)
	}
	if scheduler.registered != request || scheduler.lookedUp != request {
		t.Fatalf("registration/readback did not use the same pinned create-only request: registered=%+v lookedUp=%+v want=%+v",
			scheduler.registered, scheduler.lookedUp, request)
	}
}

func mustWorkloadReleaseTestClaim(t *testing.T) store.OperationClaim {
	t.Helper()
	claim, err := store.NewOperationClaim("operation-release-1", "worker-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}
