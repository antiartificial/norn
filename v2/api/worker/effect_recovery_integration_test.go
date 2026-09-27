package worker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/effect"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// scriptedBackend stands in for the Linux cgroup backend (unavailable on this
// platform). The real supervisor Manager, verifier, PostgreSQL effect store,
// pipeline and worker run unchanged around it; the test controls only what
// the contained execution reports.
type scriptedBackend struct {
	mu     sync.Mutex
	starts map[string]int
	states map[string]supervisor.BackendState
}

func newScriptedBackend() *scriptedBackend {
	return &scriptedBackend{starts: map[string]int{}, states: map[string]supervisor.BackendState{}}
}

func (b *scriptedBackend) Start(_ context.Context, execution supervisor.BackendExecution, material effect.LaunchMaterial) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := os.Stat(material.Directory); err != nil {
		return err
	}
	b.starts[execution.SupervisorExecutionID]++
	b.states[execution.SupervisorExecutionID] = supervisor.BackendState{Phase: effect.SupervisorRunning, EvidenceReference: "scripted/" + execution.RuntimeInstanceID}
	return nil
}

func (b *scriptedBackend) Observe(_ context.Context, execution supervisor.BackendExecution) (supervisor.BackendState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.states[execution.SupervisorExecutionID]
	if !ok {
		return supervisor.BackendState{Phase: effect.SupervisorUnknown, EvidenceReference: "scripted/missing"}, nil
	}
	return state, nil
}

func (b *scriptedBackend) Revoke(context.Context, supervisor.BackendExecution) (supervisor.BackendState, error) {
	return supervisor.BackendState{Phase: effect.SupervisorUnknown, EvidenceReference: "scripted/no-revoke"}, nil
}

func (b *scriptedBackend) RetrieveResult(_ context.Context, execution supervisor.BackendExecution, _ string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.states[execution.SupervisorExecutionID].Output...), nil
}

func (b *scriptedBackend) finish(executionID string, phase effect.SupervisorPhase, exit int, output string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.states[executionID] = supervisor.BackendState{Phase: phase, ExitCode: &exit, Output: []byte(output), ContainmentProven: true, EvidenceReference: "scripted/" + executionID}
}

func (b *scriptedBackend) totalStarts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := 0
	for _, count := range b.starts {
		total += count
	}
	return total
}

type effectFixture struct {
	t        *testing.T
	db       *store.DB
	app      string
	appDir   string
	backend  *scriptedBackend
	worker   *OperationWorker
	pipeline *pipeline.Pipeline
	builds   *atomic.Int32
}

type sourceMode int

const (
	sourceGitClean sourceMode = iota
	sourceGitDirty
	sourceNonGit
)

type fixtureOptions struct {
	source sourceMode
	// ordinaryBuild removes build.image so the pipeline runs Docker build
	// through the injected, counted build-command boundary.
	ordinaryBuild bool
}

func newEffectFixture(t *testing.T) *effectFixture {
	t.Helper()
	return newEffectFixtureWith(t, fixtureOptions{})
}

func newEffectFixtureWith(t *testing.T, options fixtureOptions) *effectFixture {
	t.Helper()
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for a pinned local source fixture")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "norn_effect_worker_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		admin.Close()
	})
	db := &store.DB{Pool: pool}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}

	app := "effect-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, app)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	image := "  image: registry.example.test/" + app + ":fixture\n"
	if options.ordinaryBuild {
		image = ""
	}
	spec := "name: " + app + "\ndeploy: true\nprocesses:\n  worker:\n    command: ./worker\nbuild:\n" + image + "  test: \"exit 99\"\n"
	for name, contents := range map[string]string{"infraspec.yaml": spec, "Dockerfile": "FROM scratch\n", "notes.txt": "baseline\n"} {
		if err := os.WriteFile(filepath.Join(appDir, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if options.source != sourceNonGit {
		for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=norn-test", "-c", "user.email=norn-test@example.invalid", "commit", "-q", "-m", "fixture"}} {
			command := exec.Command("git", append([]string{"-C", appDir}, args...)...)
			command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, output)
			}
		}
	}
	if options.source == sourceGitDirty {
		if err := os.WriteFile(filepath.Join(appDir, "notes.txt"), []byte("uncommitted\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	builds := &atomic.Int32{}

	ws := hub.New(nil)
	go ws.Run()
	backend := newScriptedBackend()
	manager, err := supervisor.NewManager(t.TempDir(), []byte("effect-worker-integration-key-0123456789"), backend)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := supervisor.NewVerifier(manager)
	if err != nil {
		t.Fatal(err)
	}
	effectStore, err := store.NewPGEffectStore(db)
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{
		DB: db, WS: ws, SagaStore: saga.NewPostgresStore(pool), AppsDir: appsDir, NetworkMode: "local",
		BuildTestEffects: &pipeline.BuildTestEffects{
			Executor: &effect.Executor{Store: effectStore, Supervisor: manager, Verifier: verifier},
			Store:    effectStore, Supervisor: "norn-effect-runner", Descriptor: manager.BuildTestDescriptor,
			Environment: []string{"PATH=/usr/bin:/bin"}, Timeout: time.Minute,
		},
		RunBuildCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "docker" || len(args) == 0 || (args[0] != "build" && args[0] != "buildx") {
				return nil, fmt.Errorf("unexpected build command %s %v", name, args)
			}
			builds.Add(1)
			return []byte("built\n"), nil
		},
	}
	worker := &OperationWorker{db: db, pipeline: p, id: "effect-worker", kinds: []string{"app.preflight", "app.deploy"}, lease: time.Minute, poll: time.Second}
	return &effectFixture{t: t, db: db, app: app, appDir: appDir, backend: backend, worker: worker, pipeline: p, builds: builds}
}

func (f *effectFixture) checkpointStages(operationID string) []string {
	f.t.Helper()
	rows, err := f.db.Pool.Query(context.Background(), `SELECT stage FROM operation_checkpoints WHERE operation_id=$1 ORDER BY stage`, operationID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var stages []string
	for rows.Next() {
		var stage string
		if err := rows.Scan(&stage); err != nil {
			f.t.Fatal(err)
		}
		stages = append(stages, stage)
	}
	return stages
}

func (f *effectFixture) queue(kind string, deployment bool) *model.Operation {
	f.t.Helper()
	now := time.Now().UTC()
	op := &model.Operation{ID: uuid.NewString(), Kind: kind, App: f.app, SagaID: uuid.NewString(), Ref: "HEAD", Status: model.OperationQueued,
		Source: "effect-integration", MaxAttempts: 3, StartedAt: now, NextAttemptAt: now, Payload: map[string]interface{}{"app": f.app}, Metadata: map[string]interface{}{}}
	if deployment {
		d := &model.Deployment{ID: uuid.NewString(), App: f.app, CommitSHA: "HEAD", SagaID: op.SagaID, Status: model.StatusQueued, SourceRef: "HEAD", StartedAt: now}
		op.Payload["deploymentId"] = d.ID
		if err := f.db.InsertDeploymentOperation(context.Background(), d, nil, op); err != nil {
			f.t.Fatal(err)
		}
		return op
	}
	if err := f.db.InsertOperation(context.Background(), op); err != nil {
		f.t.Fatal(err)
	}
	return op
}

// claim makes exactly the named operation due and runs one worker pass.
func (f *effectFixture) claim(op *model.Operation) {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at = CASE WHEN id=$1 THEN now() - interval '1 second' ELSE now() + interval '1 hour' END WHERE status='queued'`, op.ID); err != nil {
		f.t.Fatal(err)
	}
	if err := f.worker.runOnce(ctx); err != nil {
		f.t.Fatal(err)
	}
}

func (f *effectFixture) operation(id string) *model.Operation {
	f.t.Helper()
	op, err := f.db.GetOperation(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

func (f *effectFixture) sagaCount(sagaID, action string) int {
	f.t.Helper()
	var count int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM saga_events WHERE saga_id=$1 AND action=$2`, sagaID, action).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func (f *effectFixture) effect(operationID string) (executionID, lifecycle string) {
	f.t.Helper()
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT supervisor_execution_id, lifecycle FROM operation_effects WHERE operation_id=$1 AND lifecycle <> 'resolved'`, operationID).Scan(&executionID, &lifecycle); err != nil {
		f.t.Fatalf("effect for %s: %v", operationID, err)
	}
	return executionID, lifecycle
}

func (f *effectFixture) requirePending(op *model.Operation) {
	f.t.Helper()
	current := f.operation(op.ID)
	if current.Status != model.OperationQueued || current.Metadata["externalEffectRecoveryPending"] != true || current.Attempts != 0 {
		f.t.Fatalf("operation %s after pending claim = status %s attempts %d metadata %v message %q", op.ID, current.Status, current.Attempts, current.Metadata, current.Message)
	}
}

func TestSupervisedBuildTestRecoversExactExecutionAcrossClaimsWithOnePublication(t *testing.T) {
	f := newEffectFixture(t)
	a := f.queue("app.preflight", false)

	// Claim 1: launch, observe it still running, defer without terminalizing.
	f.claim(a)
	f.requirePending(a)
	aExecution, lifecycle := f.effect(a.ID)
	if lifecycle != "launched" || f.backend.totalStarts() != 1 {
		t.Fatalf("claim 1 effect lifecycle=%s starts=%d", lifecycle, f.backend.totalStarts())
	}
	if f.sagaCount(a.SagaID, "preflight.step.pending") != 1 || f.sagaCount(a.SagaID, "preflight.failed") != 0 || f.sagaCount(a.SagaID, "preflight.complete") != 0 {
		t.Fatal("pending effect published a terminal saga event")
	}

	// A conflicting operation on the same app resource cannot bypass the gate.
	b := f.queue("app.preflight", false)
	f.claim(b)
	f.requirePending(b)
	if f.backend.totalStarts() != 1 {
		t.Fatalf("blocked operation launched: starts=%d", f.backend.totalStarts())
	}
	var bEffects int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_effects WHERE operation_id=$1`, b.ID).Scan(&bEffects); err != nil || bEffects != 0 {
		t.Fatalf("blocked operation reserved an effect: %d %v", bEffects, err)
	}

	// The original execution finishes. B, blocked by it, recovers that exact
	// execution from trusted evidence, then becomes the single successor.
	f.backend.finish(aExecution, effect.SupervisorSucceeded, 0, "ok\n")
	f.claim(b)
	f.requirePending(b)
	if _, lifecycle := f.effect(a.ID); lifecycle != "completed" {
		t.Fatalf("successor did not complete the blocking effect: %s", lifecycle)
	}
	bExecution, lifecycle := f.effect(b.ID)
	if lifecycle != "launched" || f.backend.totalStarts() != 2 {
		t.Fatalf("successor effect lifecycle=%s starts=%d", lifecycle, f.backend.totalStarts())
	}

	// Claim 2 of A reuses its completed result without relaunching.
	f.claim(a)
	if current := f.operation(a.ID); current.Status != model.OperationSucceeded {
		t.Fatalf("A after recovery = %s %q", current.Status, current.Message)
	}
	f.backend.finish(bExecution, effect.SupervisorSucceeded, 0, "ok\n")
	f.claim(b)
	if current := f.operation(b.ID); current.Status != model.OperationSucceeded {
		t.Fatalf("B after recovery = %s %q", current.Status, current.Message)
	}
	for _, op := range []*model.Operation{a, b} {
		if f.sagaCount(op.SagaID, "preflight.complete") != 1 || f.sagaCount(op.SagaID, "preflight.failed") != 0 {
			t.Fatalf("operation %s terminal publications complete=%d failed=%d", op.ID, f.sagaCount(op.SagaID, "preflight.complete"), f.sagaCount(op.SagaID, "preflight.failed"))
		}
	}
	if starts := f.backend.totalStarts(); starts != 2 {
		t.Fatalf("external executions = %d, want exactly one per operation", starts)
	}
	f.backend.mu.Lock()
	for id, count := range f.backend.starts {
		if count != 1 {
			t.Errorf("execution %s launched %d times", id, count)
		}
	}
	f.backend.mu.Unlock()
}

func TestSupervisedBuildTestSurvivesOwnerCrashAndLeaseRecovery(t *testing.T) {
	f := newEffectFixture(t)
	op := f.queue("app.preflight", false)
	ctx := context.Background()
	claimed, claim, err := f.db.ClaimNextOperation(ctx, "crashing-worker", time.Minute, []string{"app.preflight"})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	// The owner launches the effect and then dies without deferring.
	if _, err := f.pipeline.ExecuteOperation(ctx, claimed, claim); !effect.IsDeferred(err) {
		t.Fatalf("running effect result = %v, want deferred", err)
	}
	execution, lifecycle := f.effect(op.ID)
	if lifecycle != "launched" {
		t.Fatalf("lifecycle = %s", lifecycle)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE operations SET locked_until = now() - interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if current := f.operation(op.ID); current.Status != model.OperationQueued {
		t.Fatalf("expired owner recovery = %s %q", current.Status, current.Message)
	}
	// A stale owner cannot reserve, launch or terminalize under its old claim.
	stale, err := f.pipeline.ExecuteOperation(ctx, claimed, claim)
	if err == nil && (stale == nil || stale.Status == model.OperationSucceeded) {
		t.Fatalf("stale claim execution = %+v", stale)
	}
	if f.backend.totalStarts() != 1 {
		t.Fatalf("stale claim launched: starts=%d", f.backend.totalStarts())
	}
	if err := f.db.FinishClaimedOperation(ctx, claim, model.OperationFailed, "stale", nil); err == nil {
		t.Fatal("stale claim terminalized the recovered operation")
	}
	f.backend.finish(execution, effect.SupervisorSucceeded, 0, "ok\n")
	f.claim(op)
	if current := f.operation(op.ID); current.Status != model.OperationSucceeded {
		t.Fatalf("after crash recovery = %s %q", current.Status, current.Message)
	}
	if f.backend.totalStarts() != 1 || f.sagaCount(op.SagaID, "preflight.complete") != 1 {
		t.Fatalf("starts=%d completions=%d", f.backend.totalStarts(), f.sagaCount(op.SagaID, "preflight.complete"))
	}
}

func TestSupervisedDeployTestPendingDoesNotFailDeploymentAndFinalFailurePublishesOnce(t *testing.T) {
	f := newEffectFixture(t)
	op := f.queue("app.deploy", true)
	deploymentID := op.Payload["deploymentId"].(string)

	f.claim(op)
	f.requirePending(op)
	deployment, err := f.db.GetDeployment(context.Background(), deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Status == model.StatusFailed {
		t.Fatal("pending effect marked the deployment failed")
	}
	steps, err := f.db.ListDeploymentSteps(context.Background(), deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Step == "test" && step.Status == model.DeploymentStepFailed {
			t.Fatal("pending effect marked the test step failed")
		}
	}
	if f.sagaCount(op.SagaID, "deploy.failed") != 0 || f.sagaCount(op.SagaID, "deploy.step.pending") != 1 {
		t.Fatal("pending deploy effect published a terminal event")
	}

	// Worker-lease recovery must not requeue it either: the claim was deferred.
	if err := f.db.RecoverExpiredOperations(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.requirePending(op)

	execution, _ := f.effect(op.ID)
	f.backend.finish(execution, effect.SupervisorFailed, 1, "FAIL: TestSomething\n")
	f.claim(op)
	current := f.operation(op.ID)
	if current.Status != model.OperationFailed || !strings.Contains(current.Message, "tests failed (exit 1)") || !strings.Contains(current.Message, "FAIL: TestSomething") {
		t.Fatalf("deploy after contained test failure = %s %q", current.Status, current.Message)
	}
	if _, lifecycle := f.effect(op.ID); lifecycle != "completed" {
		t.Fatalf("final failure lifecycle = %s", lifecycle)
	}
	deployment, err = f.db.GetDeployment(context.Background(), deploymentID)
	if err != nil || deployment.Status != model.StatusFailed {
		t.Fatalf("deployment after final failure = %+v, %v", deployment, err)
	}
	if f.sagaCount(op.SagaID, "deploy.failed") != 1 || f.backend.totalStarts() != 1 {
		t.Fatalf("deploy.failed publications=%d starts=%d", f.sagaCount(op.SagaID, "deploy.failed"), f.backend.totalStarts())
	}
}
