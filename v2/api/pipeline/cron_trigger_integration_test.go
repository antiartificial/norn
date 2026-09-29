package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	terminal, err := db.DeferOrFailCronPauseClaimedOperation(ctx, secondClaim, nil, "ambiguous Force", time.Now().Add(-time.Second), map[string]interface{}{"externalEffectRecoveryPending": true})
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

func failedAmbiguousCronTrigger(t *testing.T, fake *cronTriggerNomadFake) (*Pipeline, *store.DB, *model.Operation, string) {
	t.Helper()
	p, db, op, claim := cronTriggerFixture(t, fake)
	ctx := context.Background()
	for attempt := 0; attempt < 3; attempt++ {
		if result := p.executeCronTrigger(ctx, op, claim); result == nil || !effect.IsDeferred(result.deferred) {
			t.Fatalf("attempt %d result=%+v", attempt, result)
		}
		terminal, err := db.DeferOrFailCronPauseClaimedOperation(ctx, claim, nil, "ambiguous Force", time.Now().Add(-time.Second), map[string]interface{}{"externalEffectRecoveryPending": true})
		if err != nil || terminal != (attempt == 2) {
			t.Fatalf("attempt %d terminal=%v err=%v", attempt, terminal, err)
		}
		if terminal {
			break
		}
		op, claim, err = db.ClaimNextOperation(ctx, "cron-recovery-worker", time.Minute, []string{"app.cron-trigger"})
		if err != nil || op == nil {
			t.Fatalf("claim recovery=%+v err=%v", op, err)
		}
	}
	var effectID string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM operation_effects WHERE operation_id=$1`, op.ID).Scan(&effectID); err != nil {
		t.Fatal(err)
	}
	return p, db, op, effectID
}

func TestCronTriggerOperatorReconciliationLinksExactFailedEffect(t *testing.T) {
	fake := &cronTriggerNomadFake{forceErr: errors.New("lost response"), evalID: uuid.NewString()}
	p, db, source, effectID := failedAmbiguousCronTrigger(t, fake)
	ctx := context.Background()
	authority, err := p.OperationStore.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := EnqueueRequest{Authority: authority, Actor: store.OperationActor{Issuer: authority + "/test", Subject: "operator"}, Key: "reconcile-cron", Audit: store.AcceptanceAuditContext{Source: "integration-test"}}
	before, err := db.GetOperation(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.QueueCronTriggerReconciliation(ctx, source.App, source.ID, uuid.NewString(), fake.evalID, request); !errors.Is(err, store.ErrCronTriggerReconciliationUnavailable) {
		t.Fatalf("wrong effect accepted: %v", err)
	}
	if _, err := p.QueueCronTriggerReconciliation(ctx, "other-app", source.ID, effectID, fake.evalID, request); !errors.Is(err, store.ErrCronTriggerReconciliationUnavailable) {
		t.Fatalf("wrong app accepted: %v", err)
	}
	if _, err := p.QueueCronTriggerReconciliation(ctx, source.App, source.ID, effectID, uuid.NewString(), request); err == nil {
		t.Fatal("forged evaluation accepted")
	}
	accepted, err := p.QueueCronTriggerReconciliation(ctx, source.App, source.ID, effectID, fake.evalID, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "correction-worker", time.Minute, []string{"app.cron-trigger-reconcile"})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("correction claim=%+v err=%v", claimed, err)
	}
	if err := db.FinishClaimedOperation(ctx, claim, model.OperationSucceeded, "forged generic completion", nil); err == nil {
		t.Fatal("generic completion forged correction success")
	}
	stale, err := store.NewOperationClaim(claim.OperationID(), claim.OwnerID(), claim.Generation()+1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteCronTriggerReconciliation(ctx, stale, source.App, source.ID, effectID, source.App+"-nightly", fake.evalID, source.App+"-nightly/periodic-child", time.Now().UTC()); err == nil {
		t.Fatal("stale correction claim completed original effect")
	}
	result, err := p.ExecuteOperation(ctx, claimed, claim)
	if err != nil || result == nil || !result.Finished() || result.Status != model.OperationSucceeded {
		t.Fatalf("correction result=%+v err=%v", result, err)
	}
	original, err := db.GetOperation(ctx, source.ID)
	if err != nil || original.Status != model.OperationFailed || original.Metadata["manualRecoveryRequired"] != true {
		t.Fatalf("original receipt=%+v err=%v", original, err)
	}
	if original.Message != before.Message || original.LastError != before.LastError || !original.UpdatedAt.Equal(before.UpdatedAt) || !original.FinishedAt.Equal(*before.FinishedAt) || !reflect.DeepEqual(original.Metadata, before.Metadata) {
		t.Fatalf("original failed receipt was rewritten before=%+v after=%+v", before, original)
	}
	var lifecycle, runtimeID string
	if err := db.Pool.QueryRow(ctx, `SELECT lifecycle,runtime_instance_id FROM operation_effects WHERE id=$1`, effectID).Scan(&lifecycle, &runtimeID); err != nil || lifecycle != "completed" || runtimeID != fake.evalID {
		t.Fatalf("effect=%s runtime=%s err=%v", lifecycle, runtimeID, err)
	}
	var correctionIntents int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM evidence_archive_intents WHERE operation_id=$1`, accepted.Operation.ID).Scan(&correctionIntents); err != nil || correctionIntents != 1 {
		t.Fatalf("correction archive intent=%d err=%v", correctionIntents, err)
	}
	var sourceIntentID, correctionIntentID string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM evidence_archive_intents WHERE operation_id=$1`, source.ID).Scan(&sourceIntentID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM evidence_archive_intents WHERE operation_id=$1`, accepted.Operation.ID).Scan(&correctionIntentID); err != nil {
		t.Fatal(err)
	}
	checkHolds := func(wantHold bool) {
		t.Helper()
		intent, err := db.EvidenceIntent(ctx, sourceIntentID)
		if err != nil {
			t.Fatal(err)
		}
		holds, err := db.EvidenceHolds(ctx, intent, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, hold := range holds {
			if hold == "manual-recovery" || hold == "unresolved-effect" {
				found = true
			}
		}
		if found != wantHold {
			t.Fatalf("recovery hold=%v want=%v all=%v", found, wantHold, holds)
		}
	}
	checkHolds(true)
	// Model the archiver's verified state to exercise the retention predicate.
	if _, err := db.Pool.Exec(ctx, `UPDATE evidence_archive_intents SET state='verified',object_key='test-correction-archive',object_sha256=$2,
		object_bytes=1,verified_at=now() WHERE id=$1`, correctionIntentID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	checkHolds(false)
	request.Key = "second-correction"
	if _, err := p.QueueCronTriggerReconciliation(ctx, source.App, source.ID, effectID, fake.evalID, request); !errors.Is(err, store.ErrCronTriggerReconciliationUnavailable) {
		t.Fatalf("repeat correction accepted: %v", err)
	}
	// A later, independently signed trigger may be ambiguous too. The same
	// evaluation cannot release its reservation: it was already credited above.
	now := time.Now().UTC()
	next := model.Operation{ID: uuid.NewString(), Kind: "app.cron-trigger", App: source.App, SagaID: uuid.NewString(), Ref: source.Ref,
		Status: model.OperationQueued, StartedAt: now, NextAttemptAt: now, MaxAttempts: 3, Payload: source.Payload}
	request.Key = "second-source"
	if _, err := p.QueueOperation(ctx, next, request); err != nil {
		t.Fatal(err)
	}
	secondSource, secondClaim, err := db.ClaimNextOperation(ctx, "second-source-worker", time.Minute, []string{"app.cron-trigger"})
	if err != nil || secondSource == nil || secondSource.ID != next.ID {
		t.Fatalf("second source claim=%+v err=%v", secondSource, err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if result := p.executeCronTrigger(ctx, secondSource, secondClaim); result == nil || !effect.IsDeferred(result.deferred) {
			t.Fatalf("second source attempt %d result=%+v", attempt, result)
		}
		terminal, err := db.DeferOrFailCronPauseClaimedOperation(ctx, secondClaim, nil, "ambiguous Force", time.Now().Add(-time.Second), map[string]interface{}{"externalEffectRecoveryPending": true})
		if err != nil || terminal != (attempt == 2) {
			t.Fatalf("second source defer=%v err=%v", terminal, err)
		}
		if terminal {
			break
		}
		secondSource, secondClaim, err = db.ClaimNextOperation(ctx, "second-source-worker", time.Minute, []string{"app.cron-trigger"})
		if err != nil || secondSource == nil {
			t.Fatalf("second source retry=%+v err=%v", secondSource, err)
		}
	}
	var secondEffectID string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM operation_effects WHERE operation_id=$1`, next.ID).Scan(&secondEffectID); err != nil {
		t.Fatal(err)
	}
	request.Key = "duplicate-evaluation"
	secondCorrection, err := p.QueueCronTriggerReconciliation(ctx, source.App, next.ID, secondEffectID, fake.evalID, request)
	if err != nil {
		t.Fatal(err)
	}
	secondCorrectionOp, secondCorrectionClaim, err := db.ClaimNextOperation(ctx, "second-correction-worker", time.Minute, []string{"app.cron-trigger-reconcile"})
	if err != nil || secondCorrectionOp == nil || secondCorrectionOp.ID != secondCorrection.Operation.ID {
		t.Fatalf("second correction claim=%+v err=%v", secondCorrectionOp, err)
	}
	refused := p.executeCronTriggerReconciliation(ctx, secondCorrectionOp, secondCorrectionClaim)
	if refused == nil || refused.Status != model.OperationFailed || refused.Finished() {
		t.Fatalf("duplicate eval correction=%+v", refused)
	}
	var secondEffectLifecycle string
	if err := db.Pool.QueryRow(ctx, `SELECT lifecycle FROM operation_effects WHERE id=$1`, secondEffectID).Scan(&secondEffectLifecycle); err != nil || secondEffectLifecycle != "reserved" {
		t.Fatalf("second effect lifecycle=%s err=%v", secondEffectLifecycle, err)
	}
	if fake.forceCalls != 2 {
		t.Fatalf("Force calls=%d", fake.forceCalls)
	}
}
