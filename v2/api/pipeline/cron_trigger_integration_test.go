package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

type cronTriggerNomadFake struct {
	forceCalls int
	forceErr   error
	evalID     string
	parent     *nomad.PeriodicJobInfo
}

func (f *cronTriggerNomadFake) PeriodicJobSchedule(string) (*nomad.PeriodicJobInfo, error) {
	return f.parent, nil
}
func (f *cronTriggerNomadFake) PeriodicForce(string) (string, error) {
	f.forceCalls++
	return f.evalID, f.forceErr
}
func (f *cronTriggerNomadFake) PeriodicForceEvaluation(_ context.Context, evalID, parent string) (string, error) {
	if evalID != f.evalID {
		return "", errors.New("unexpected evaluation")
	}
	return parent + "/periodic-child", nil
}

func cronTriggerFixture(t *testing.T, fake *cronTriggerNomadFake) (*Pipeline, *store.DB, *model.Operation, store.OperationClaim) {
	t.Helper()
	p, db, request := acceptancePipelineFixture(t)
	dir := t.TempDir()
	appDir := filepath.Join(dir, "widget")
	if err := os.MkdirAll(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte("name: widget\ndeploy: true\nprocesses:\n  nightly:\n    command: echo ok\n    schedule: '0 2 * * *'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p.AppsDir = dir
	spec, err := p.findSpec("widget")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	parent := &nomad.PeriodicJobInfo{JobID: "widget-nightly", Schedule: "0 2 * * *", TimeZone: model.ResolveProcessTimezone(spec, spec.Processes["nightly"]), Version: 1, ModifyIndex: 2}
	fake.parent = parent
	es, err := store.NewPGEffectStore(db)
	if err != nil {
		t.Fatal(err)
	}
	p.CronTriggerEffects = &CronTriggerEffects{store: es, client: fake}
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: "app.cron-trigger", App: "widget", SagaID: uuid.NewString(), Ref: "nightly", Status: model.OperationQueued, StartedAt: now, NextAttemptAt: now, MaxAttempts: 3, Payload: map[string]interface{}{"process": "nightly", "jobId": "widget-nightly", "schedule": parent.Schedule, "timezone": parent.TimeZone, "specDigest": digest, "version": "1", "modifyIndex": "2"}}
	request.Key = "cron-trigger-test"
	accepted, err := p.QueueOperation(context.Background(), op, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(context.Background(), "cron-worker-1", time.Minute, []string{"app.cron-trigger"})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	return p, db, claimed, claim
}

func TestCronTriggerLostResponseNeverRedispatchesAfterClaimRecovery(t *testing.T) {
	fake := &cronTriggerNomadFake{forceErr: errors.New("connection closed")}
	p, db, op, claim := cronTriggerFixture(t, fake)
	ctx := context.Background()
	first := p.executeCronTrigger(ctx, op, claim)
	if first == nil || !effect.IsDeferred(first.deferred) || fake.forceCalls != 1 {
		t.Fatalf("first=%+v forceCalls=%d", first, fake.forceCalls)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	secondOp, secondClaim, err := db.ClaimNextOperation(ctx, "cron-worker-2", time.Minute, []string{"app.cron-trigger"})
	if err != nil || secondOp == nil || secondClaim.Generation() <= claim.Generation() {
		t.Fatalf("successor=%+v err=%v", secondOp, err)
	}
	second := p.executeCronTrigger(ctx, secondOp, secondClaim)
	if second == nil || !effect.IsDeferred(second.deferred) || fake.forceCalls != 1 {
		t.Fatalf("recovery=%+v forceCalls=%d", second, fake.forceCalls)
	}
	terminal, err := db.DeferOrFailCronPauseClaimedOperation(ctx, secondClaim, nil, "ambiguous Force", time.Now(), map[string]interface{}{"externalEffectRecoveryPending": true})
	if err != nil || terminal {
		t.Fatalf("defer=%v terminal=%v", err, terminal)
	}
	thirdOp, thirdClaim, err := db.ClaimNextOperation(ctx, "cron-worker-3", time.Minute, []string{"app.cron-trigger"})
	if err != nil || thirdOp == nil {
		t.Fatalf("third claim=%+v err=%v", thirdOp, err)
	}
	third := p.executeCronTrigger(ctx, thirdOp, thirdClaim)
	if third == nil || !effect.IsDeferred(third.deferred) || fake.forceCalls != 1 {
		t.Fatalf("third=%+v forceCalls=%d", third, fake.forceCalls)
	}
	terminal, err = db.DeferOrFailCronPauseClaimedOperation(ctx, thirdClaim, nil, "ambiguous Force", time.Now(), map[string]interface{}{"externalEffectRecoveryPending": true})
	if err != nil || !terminal {
		t.Fatalf("terminal=%v err=%v", terminal, err)
	}
	receipt, err := db.GetOperation(ctx, op.ID)
	if err != nil || receipt.Status != model.OperationFailed || receipt.Metadata["manualRecoveryRequired"] != true {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	var archiveIntents int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM evidence_archive_intents WHERE operation_id=$1`, op.ID).Scan(&archiveIntents); err != nil || archiveIntents != 1 {
		t.Fatalf("manual receipt archive intents=%d err=%v", archiveIntents, err)
	}
}

func TestCronTriggerAcknowledgedEvaluationCompletesOnRecovery(t *testing.T) {
	fake := &cronTriggerNomadFake{evalID: uuid.NewString()}
	p, db, op, claim := cronTriggerFixture(t, fake)
	result := p.executeCronTrigger(context.Background(), op, claim)
	if result == nil || result.Status != model.OperationSucceeded || fake.forceCalls != 1 {
		t.Fatalf("result=%+v calls=%d", result, fake.forceCalls)
	}
	recovered := p.executeCronTrigger(context.Background(), op, claim)
	if recovered == nil || recovered.Status != model.OperationSucceeded || fake.forceCalls != 1 {
		t.Fatalf("recovered=%+v calls=%d", recovered, fake.forceCalls)
	}
	var lifecycle string
	if err := db.Pool.QueryRow(context.Background(), `SELECT lifecycle FROM operation_effects WHERE operation_id=$1`, op.ID).Scan(&lifecycle); err != nil || lifecycle != "completed" {
		t.Fatalf("effect lifecycle=%q err=%v", lifecycle, err)
	}
}
