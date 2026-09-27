package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/effect"
	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

var _ DeploymentJobEffectStore = (*etcdstore.V3DeploymentEffectReservations)(nil)

type deploymentStepEffects struct {
	record     effect.Record
	created    bool
	attempted  bool
	reserves   int
	marks      int
	launched   int
	completed  int
	completion effect.Completion
}

func (f *deploymentStepEffects) Reserve(_ context.Context, r effect.Reservation) (effect.ReservationResult, error) {
	f.reserves++
	if f.record.Token.EffectID == "" {
		f.record = effect.Record{Token: effect.Token{EffectID: "effect-1", Generation: 1}, Reservation: r, Lifecycle: effect.LifecycleReserved}
	}
	created := f.created
	f.created = false
	return effect.ReservationResult{Record: f.record, Created: created}, nil
}
func (f *deploymentStepEffects) MarkSubmitAttempt(context.Context, effect.Token) (bool, error) {
	f.marks++
	if f.attempted {
		return false, nil
	}
	f.attempted = true
	return true, nil
}
func (f *deploymentStepEffects) SubmitAttempted(context.Context, effect.Token) (bool, error) {
	return f.attempted, nil
}
func (f *deploymentStepEffects) MarkLaunched(_ context.Context, _ effect.Token, identity effect.ExecutionIdentity) error {
	f.launched++
	f.record.Lifecycle = effect.LifecycleLaunched
	f.record.Execution = identity
	return nil
}
func (f *deploymentStepEffects) Complete(_ context.Context, _ effect.Token, completion effect.Completion) error {
	f.completed++
	f.completion = completion
	return nil
}

type deploymentStepRemote struct {
	inputChecks  int
	inputError   error
	submits      int
	lookups      int
	lastSubmit   nomad.CASDeploymentJobRequest
	lastLookup   nomad.CASDeploymentJobRequest
	lastHealth   nomad.CASDeploymentJobRequest
	state        nomad.DeploymentJobObservationState
	err          error
	health       nomad.DeploymentJobHealthObservation
	healthChecks int
}

func (f *deploymentStepRemote) CheckManagedJobInputs(_ context.Context, _ string, _ nomad.ManagedJobInputRequirements, _ map[string]string) error {
	f.inputChecks++
	return f.inputError
}

func (f *deploymentStepRemote) RegisterDeploymentJobCAS(_ context.Context, request nomad.CASDeploymentJobRequest) (string, error) {
	f.submits++
	f.lastSubmit = request
	return "", f.err
}
func (f *deploymentStepRemote) LookupDeploymentJobRevision(_ context.Context, request nomad.CASDeploymentJobRequest) (nomad.DeploymentJobObservation, error) {
	f.lookups++
	f.lastLookup = request
	return nomad.DeploymentJobObservation{State: f.state, JobModifyIndex: 12, Version: 1}, nil
}
func (f *deploymentStepRemote) ObserveDeploymentJobHealth(_ context.Context, request nomad.CASDeploymentJobRequest) (nomad.DeploymentJobHealthObservation, error) {
	f.healthChecks++
	f.lastHealth = request
	return f.health, f.err
}

func deploymentStepFixture(t *testing.T) (effect.Reservation, *nomadapi.Job) {
	t.Helper()
	const image = "registry.example.test/demo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	job, err := nomad.Translate(&model.InfraSpec{App: "demo", Processes: map[string]model.Process{"web": {Port: 8080}}}, image, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := nomad.DeploymentJobEffectInput{App: "demo", DeploymentID: "deployment-1", Region: "west", NomadRegion: "global", ImageTag: image,
		SpecDigest: "sha256:" + strings.Repeat("b", 64)}
	job.Meta[nomad.DeploymentIDMeta] = input.DeploymentID
	job.Meta[nomad.SpecDigestMeta] = input.SpecDigest
	job.Meta[nomad.DeploymentOperationIDMeta] = "operation-1"
	job.Meta[nomad.DeploymentExecutionIDMeta] = "nomad-deployment-1"
	input.JobDigest, err = nomad.DigestDeploymentJob(job)
	if err != nil {
		t.Fatal(err)
	}
	job.Meta[nomad.DeploymentJobDigestMeta] = input.JobDigest
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r := effect.Reservation{Authority: "authority", Resource: "app/demo/deploy/west",
		OperationClaim: effect.OperationClaim{OperationID: "operation-1", OwnerID: "worker", Generation: 1},
		Stage:          "app.deploy.nomad.submit", Supervisor: "nomad-deployment", SupervisorExecutionID: "nomad-deployment-1", LaunchPayload: payload}
	r.InputDigest, err = effect.ComputeInputDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	return r, job
}

func TestEnsureDeploymentJobEffectSubmitsOnceThenObserves(t *testing.T) {
	r, job := deploymentStepFixture(t)
	effects := &deploymentStepEffects{created: true}
	remote := &deploymentStepRemote{state: nomad.DeploymentJobFound}
	decision, err := EnsureDeploymentJobEffect(context.Background(), effects, remote, r, job)
	if err != nil || decision.State != DeploymentJobEffectObserved || !decision.Attempted || remote.submits != 1 || effects.marks != 1 || effects.launched != 1 {
		t.Fatalf("first step=%+v submits=%d marks=%d launched=%d err=%v", decision, remote.submits, effects.marks, effects.launched, err)
	}
	decision, err = EnsureDeploymentJobEffect(context.Background(), effects, remote, r, job)
	if err != nil || decision.State != DeploymentJobEffectObserved || remote.submits != 1 || effects.marks != 1 || effects.launched != 1 {
		t.Fatalf("replay=%+v submits=%d marks=%d launched=%d err=%v", decision, remote.submits, effects.marks, effects.launched, err)
	}
}

func TestManagedDeploymentEffectUsesRevisionJobThroughCompletion(t *testing.T) {
	r, job := deploymentStepFixture(t)
	var input nomad.DeploymentJobEffectInput
	if err := json.Unmarshal(r.LaunchPayload, &input); err != nil {
		t.Fatal(err)
	}
	var err error
	input.JobID, err = nomad.ManagedDeploymentJobID(input.App, input.Region, input.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	job.ID = &input.JobID
	input.ManagedInputs = &nomad.ManagedJobInputRequirements{JobID: input.JobID, VariablePath: nomad.DatabaseVariablePath(input.JobID)}
	input.ExpectedJobModifyIndex = 0
	input.JobDigest, err = nomad.DigestDeploymentJob(job)
	if err != nil {
		t.Fatal(err)
	}
	job.Meta[nomad.DeploymentJobDigestMeta] = input.JobDigest
	r.LaunchPayload, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r.InputDigest, err = effect.ComputeInputDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	effects := &deploymentStepEffects{created: true}
	remote := &deploymentStepRemote{state: nomad.DeploymentJobFound,
		health: nomad.DeploymentJobHealthObservation{State: nomad.DeploymentJobHealthReady, JobVersion: 1, JobModifyIndex: 12, AllocationIDs: []string{"alloc-1"}}}
	decision, err := EnsureDeploymentJobEffect(context.Background(), effects, remote, r, job)
	if err != nil || decision.State != DeploymentJobEffectObserved || effects.launched != 1 ||
		remote.inputChecks != 1 ||
		remote.lastSubmit.EffectiveJobID() != input.JobID || remote.lastLookup.EffectiveJobID() != input.JobID ||
		remote.lastSubmit.PlacementRegion != input.Region || remote.lastSubmit.ExpectedJobModifyIndex != 0 {
		t.Fatalf("managed revision launch=%+v submit=%+v lookup=%+v err=%v", decision, remote.lastSubmit, remote.lastLookup, err)
	}
	completed, err := CompleteDeploymentJobEffect(context.Background(), effects, remote, effects.record)
	if err != nil || completed.State != DeploymentJobEffectObserved || effects.completed != 1 || remote.lastHealth.EffectiveJobID() != input.JobID {
		t.Fatalf("managed revision completion=%+v health=%+v err=%v", completed, remote.lastHealth, err)
	}
	if !strings.Contains(effects.completion.Verification.RuntimeInstanceID, input.JobID) {
		t.Fatal("completed effect lost revision job provenance")
	}
}

func TestManagedDeploymentRefusesMissingInputsBeforeReserve(t *testing.T) {
	r, job := deploymentStepFixture(t)
	var input nomad.DeploymentJobEffectInput
	if err := json.Unmarshal(r.LaunchPayload, &input); err != nil {
		t.Fatal(err)
	}
	var err error
	input.JobID, err = nomad.ManagedDeploymentJobID(input.App, input.Region, input.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	job.ID = &input.JobID
	input.ManagedInputs = &nomad.ManagedJobInputRequirements{JobID: input.JobID, VariablePath: nomad.DatabaseVariablePath(input.JobID), RequiredKeys: []string{"API_TOKEN"}}
	input.JobDigest, err = nomad.DigestDeploymentJob(job)
	if err != nil {
		t.Fatal(err)
	}
	job.Meta[nomad.DeploymentJobDigestMeta] = input.JobDigest
	r.LaunchPayload, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r.InputDigest, err = effect.ComputeInputDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	effects := &deploymentStepEffects{created: true}
	remote := &deploymentStepRemote{inputError: errors.New("inputs unavailable")}
	if _, err := EnsureDeploymentJobEffect(context.Background(), effects, remote, r, job); err == nil || effects.reserves != 0 || effects.marks != 0 || remote.submits != 0 || remote.inputChecks != 1 {
		t.Fatalf("missing inputs reached effect or Nomad: reserves=%d marks=%d submits=%d checks=%d err=%v", effects.reserves, effects.marks, remote.submits, remote.inputChecks, err)
	}
}

func TestEnsureDeploymentJobEffectNeverResubmitsAmbiguous404(t *testing.T) {
	r, job := deploymentStepFixture(t)
	effects := &deploymentStepEffects{created: true}
	remote := &deploymentStepRemote{state: nomad.DeploymentJobNotFound, err: errors.New("private Nomad response")}
	decision, err := EnsureDeploymentJobEffect(context.Background(), effects, remote, r, job)
	if err != nil || decision.State != DeploymentJobEffectUnresolved || !decision.Attempted || remote.submits != 1 || effects.launched != 0 {
		t.Fatalf("ambiguous first step=%+v submits=%d launched=%d err=%v", decision, remote.submits, effects.launched, err)
	}
	decision, err = EnsureDeploymentJobEffect(context.Background(), effects, remote, r, job)
	if err != nil || decision.State != DeploymentJobEffectUnresolved || remote.submits != 1 || effects.marks != 1 {
		t.Fatalf("ambiguous replay=%+v submits=%d marks=%d err=%v", decision, remote.submits, effects.marks, err)
	}
}

func TestEnsureDeploymentJobEffectRejectsChangedImageBeforeReserve(t *testing.T) {
	r, job := deploymentStepFixture(t)
	job.TaskGroups[0].Tasks[0].Config["image"] = "changed"
	effects := &deploymentStepEffects{created: true}
	remote := &deploymentStepRemote{state: nomad.DeploymentJobFound}
	if _, err := EnsureDeploymentJobEffect(context.Background(), effects, remote, r, job); err == nil || effects.reserves != 0 || remote.submits != 0 {
		t.Fatalf("changed image reached reserve or Nomad: reserves=%d submits=%d err=%v", effects.reserves, remote.submits, err)
	}
}

func TestObserveDeploymentJobEffectDoesNotSubmitDuringRecovery(t *testing.T) {
	r, _ := deploymentStepFixture(t)
	effects := &deploymentStepEffects{record: effect.Record{Token: effect.Token{EffectID: "effect-1", Generation: 1}, Reservation: r, Lifecycle: effect.LifecycleReserved}, attempted: true}
	remote := &deploymentStepRemote{state: nomad.DeploymentJobNotFound}
	decision, err := ObserveDeploymentJobEffect(context.Background(), effects, remote, effects.record)
	if err != nil || decision.State != DeploymentJobEffectUnresolved || remote.submits != 0 || effects.launched != 0 {
		t.Fatalf("recovery=%+v submits=%d launched=%d err=%v", decision, remote.submits, effects.launched, err)
	}
	remote.state = nomad.DeploymentJobFound
	decision, err = ObserveDeploymentJobEffect(context.Background(), effects, remote, effects.record)
	if err != nil || decision.State != DeploymentJobEffectObserved || remote.submits != 0 || effects.launched != 1 {
		t.Fatalf("readback recovery=%+v submits=%d launched=%d err=%v", decision, remote.submits, effects.launched, err)
	}
}

func TestCompleteDeploymentJobEffectRequiresExactReadyHealth(t *testing.T) {
	r, _ := deploymentStepFixture(t)
	record := effect.Record{Token: effect.Token{EffectID: "effect-1", Generation: 1}, Reservation: r, Lifecycle: effect.LifecycleLaunched,
		Execution: effect.ExecutionIdentity{Supervisor: r.Supervisor, SupervisorExecutionID: r.SupervisorExecutionID,
			RuntimeInstanceID: "nomad-job:global:demo:12"}}
	effects := &deploymentStepEffects{record: record, attempted: true}
	remote := &deploymentStepRemote{health: nomad.DeploymentJobHealthObservation{State: nomad.DeploymentJobHealthPending, JobModifyIndex: 12}}
	if decision, err := CompleteDeploymentJobEffect(context.Background(), effects, remote, record); err != nil || decision.State != DeploymentJobEffectUnresolved || effects.completed != 0 {
		t.Fatalf("pending health completed effect: decision=%+v completed=%d err=%v", decision, effects.completed, err)
	}
	remote.err = errors.New("Nomad observation unavailable")
	remote.health.State = nomad.DeploymentJobHealthReady
	remote.health.AllocationIDs = []string{"alloc-1"}
	if decision, err := CompleteDeploymentJobEffect(context.Background(), effects, remote, record); err != nil || decision.State != DeploymentJobEffectUnresolved || effects.completed != 0 {
		t.Fatalf("indeterminate health completed effect: decision=%+v completed=%d err=%v", decision, effects.completed, err)
	}
	remote.err = nil
	remote.health = nomad.DeploymentJobHealthObservation{State: nomad.DeploymentJobHealthReady, JobModifyIndex: 13, AllocationIDs: []string{"alloc-1"}}
	if decision, err := CompleteDeploymentJobEffect(context.Background(), effects, remote, record); err != nil || decision.State != DeploymentJobEffectUnresolved || effects.completed != 0 {
		t.Fatalf("changed revision completed effect: decision=%+v completed=%d err=%v", decision, effects.completed, err)
	}
	remote.health.JobModifyIndex = 12
	remote.health.JobVersion = 1
	decision, err := CompleteDeploymentJobEffect(context.Background(), effects, remote, record)
	if err != nil || decision.State != DeploymentJobEffectObserved || effects.completed != 1 ||
		effects.completion.Outcome != effect.OutcomeSucceeded ||
		effects.completion.Verification.InputDigest != r.InputDigest ||
		effects.completion.Verification.RuntimeInstanceID != record.Execution.RuntimeInstanceID ||
		effects.completion.Verification.ResultDigest == "" {
		t.Fatalf("ready health did not bind completion: decision=%+v completion=%+v err=%v", decision, effects.completion, err)
	}
}

func TestCompleteDeploymentJobEffectRequiresAttemptAndLaunch(t *testing.T) {
	r, _ := deploymentStepFixture(t)
	record := effect.Record{Token: effect.Token{EffectID: "effect-1", Generation: 1}, Reservation: r, Lifecycle: effect.LifecycleLaunched,
		Execution: effect.ExecutionIdentity{Supervisor: r.Supervisor, SupervisorExecutionID: r.SupervisorExecutionID,
			RuntimeInstanceID: "nomad-job:global:demo:12"}}
	effects := &deploymentStepEffects{record: record}
	remote := &deploymentStepRemote{health: nomad.DeploymentJobHealthObservation{State: nomad.DeploymentJobHealthReady, JobModifyIndex: 12, AllocationIDs: []string{"alloc-1"}}}
	if _, err := CompleteDeploymentJobEffect(context.Background(), effects, remote, record); err != nil || effects.completed != 0 || remote.healthChecks != 0 {
		t.Fatalf("unattempted effect reached health: checks=%d completed=%d err=%v", remote.healthChecks, effects.completed, err)
	}
	effects.attempted = true
	record.Lifecycle = effect.LifecycleReserved
	if _, err := CompleteDeploymentJobEffect(context.Background(), effects, remote, record); err == nil || effects.completed != 0 {
		t.Fatalf("unlaunched effect completed: completed=%d err=%v", effects.completed, err)
	}
}
