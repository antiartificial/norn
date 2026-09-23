package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

func insertFleetAttemptDispatch(t *testing.T, db *DB, planID string) (rawNonce, nonceHash string) {
	t.Helper()
	rawNonce = strings.Repeat("f", 64)
	digest := sha256.Sum256([]byte(rawNonce))
	nonceHash = hex.EncodeToString(digest[:])
	if _, err := db.CreateFleetGitHubDispatch(context.Background(), FleetGitHubDispatch{
		PlanID: planID, PlanRunID: 6, PlanSHA256: strings.Repeat("b", 64), ApprovedHeadSHA: strings.Repeat("c", 40),
		FleetEnvironment: "staging/nyc3", DispatchNonce: rawNonce, DispatchNonceSHA256: nonceHash,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishFleetGitHubDispatch(context.Background(), planID, nonceHash, 7, "https://github.com/acme/fleet/actions/runs/7"); err != nil {
		t.Fatal(err)
	}
	return rawNonce, nonceHash
}

func newFleetRunnerAttemptAcceptance(t *testing.T, operationStore *PGOperationStore, planID, key, nonceHash, runID, intent string, resume bool, predecessorID string, timeout int) OperationAcceptance {
	t.Helper()
	authority, err := operationStore.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	attemptID := uuid.NewString()
	runnerID := "github:acme/fleet:" + runID + ":1"
	workflowURL := "https://github.com/acme/fleet/actions/runs/" + runID
	admission := &FleetRunnerAttemptAdmission{
		PlanID: planID, AttemptID: attemptID, ExpectedPredecessorID: predecessorID,
		RunnerAttemptID: runnerID, CommitSHA: strings.Repeat("c", 40), PlanSHA256: strings.Repeat("b", 64), WorkflowURL: workflowURL,
		DispatchNonceSHA256: nonceHash, SourceDispatchRunID: "7", Resume: resume, HeartbeatTimeoutSeconds: timeout,
		WorkloadIntent: intent, WorkloadRunID: runID, WorkloadSHA: strings.Repeat("c", 40),
	}
	payload := map[string]interface{}{
		"attemptId": attemptID, "planId": planID, "runnerAttemptId": runnerID,
		"commitSha": admission.CommitSHA, "planSha256": admission.PlanSHA256, "workflowUrl": workflowURL,
		"dispatchNonceSha256": nonceHash, "sourceDispatchRunId": "7", "resume": resume, "heartbeatTimeoutSeconds": timeout,
	}
	acceptance := OperationAcceptance{
		Identity:  OperationRequestIdentity{Authority: authority, Actor: OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "1:2:" + runID + ":1"}, Kind: "fleet.runner-attempt", Resource: planID, Key: key},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "fleet.runner-attempt", Ref: planID, Status: model.OperationSucceeded, Source: "fleet-runner", Risk: "bounded protected runner lease", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: payload, Metadata: map[string]interface{}{}},
		Audit:     AcceptanceAuditContext{Source: "github-actions"},
		Semantics: map[string]interface{}{
			"action": "fleet.runner-attempt", "planId": planID, "runnerAttemptId": runnerID,
			"commitSha": admission.CommitSHA, "planSha256": admission.PlanSHA256, "workflowUrl": workflowURL,
			"dispatchNonceSha256": nonceHash, "sourceDispatchRunId": "7", "resume": resume, "heartbeatTimeoutSeconds": timeout,
			"workload": map[string]interface{}{"intent": intent, "runId": runID, "sha": admission.WorkloadSHA},
		},
		FleetRunnerAttempt: admission,
	}
	acceptance.Fingerprint, err = CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	return acceptance
}

func TestFleetRunnerAttemptAcceptanceReplaysGeneratedIdentityAndKeepsNoncePrivate(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
	rawNonce, nonceHash := insertFleetAttemptDispatch(t, dbs[0], plan.ID)
	firstRequest := newFleetRunnerAttemptAcceptance(t, stores[0], plan.ID, "runner-replay", nonceHash, "7", "apply", false, "", 120)
	first, err := stores[0].Accept(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	if first.FleetRunnerAttempt == nil || first.FleetRunnerAttempt.Attempt != 1 || first.FleetRunnerAttempt.RootAttemptID != first.FleetRunnerAttempt.ID {
		t.Fatalf("invalid first runner attempt: %#v", first.FleetRunnerAttempt)
	}
	retry := newFleetRunnerAttemptAcceptance(t, stores[0], plan.ID, "runner-replay", nonceHash, "7", "apply", false, "", 120)
	replayed, err := stores[0].Accept(context.Background(), retry)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Operation.ID != first.Operation.ID || replayed.FleetRunnerAttempt == nil || replayed.FleetRunnerAttempt.ID != first.FleetRunnerAttempt.ID {
		t.Fatalf("runner replay changed accepted identity: first=%#v replay=%#v", first.FleetRunnerAttempt, replayed.FleetRunnerAttempt)
	}
	if strings.Contains(string(first.Intent.RequestCanonicalBytes), rawNonce) || strings.Contains(string(first.Intent.CanonicalBytes), rawNonce) {
		t.Fatal("raw dispatch nonce entered signed acceptance evidence")
	}
	var count int
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT count(*) FROM fleet_runner_attempts WHERE plan_id=$1`, plan.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("runner replay created %d attempts", count)
	}
}

func TestFleetRunnerAttemptAcceptanceRestartsDestructiveRecoveryAtPrechange(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	plan := insertFleetAcceptancePlan(t, dbs[0], "replace")
	_, nonceHash := insertFleetAttemptDispatch(t, dbs[0], plan.ID)
	initialRequest := newFleetRunnerAttemptAcceptance(t, stores[0], plan.ID, "destructive-initial", nonceHash, "7", "apply", false, "", 120)
	initial, err := stores[0].Accept(context.Background(), initialRequest)
	if err != nil {
		t.Fatal(err)
	}
	if initial.FleetRunnerAttempt.CurrentPhase != "prechange_verified" {
		t.Fatalf("destructive initial phase=%q", initial.FleetRunnerAttempt.CurrentPhase)
	}
	recoveryRequest := newFleetRunnerAttemptAcceptance(t, stores[0], plan.ID, "destructive-recovery", nonceHash, "8", "recover", true, initial.FleetRunnerAttempt.ID, 120)
	recovery, err := stores[0].Accept(context.Background(), recoveryRequest)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.FleetRunnerAttempt.CurrentPhase != "prechange_verified" {
		t.Fatalf("destructive recovery phase=%q", recovery.FleetRunnerAttempt.CurrentPhase)
	}
}

func TestFleetRunnerAttemptAcceptanceFingerprintsResumeTimeoutAndRejectsTypedRawNonce(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
	rawNonce, nonceHash := insertFleetAttemptDispatch(t, dbs[0], plan.ID)
	base := newFleetRunnerAttemptAcceptance(t, stores[0], plan.ID, "runner-fingerprint", nonceHash, "7", "apply", false, "", 120)
	changedTimeout := base
	changedTimeout.FleetRunnerAttempt = cloneFleetRunnerAttemptAdmission(base.FleetRunnerAttempt)
	changedTimeout.FleetRunnerAttempt.HeartbeatTimeoutSeconds = 121
	changedTimeout.Operation.Payload = cloneStringInterfaceMap(base.Operation.Payload)
	changedTimeout.Operation.Payload["heartbeatTimeoutSeconds"] = 121
	changedTimeout.Semantics = cloneStringInterfaceMap(base.Semantics)
	changedTimeout.Semantics["heartbeatTimeoutSeconds"] = 121
	changedTimeout.Fingerprint, _ = CanonicalOperationRequestFingerprint(changedTimeout)
	if sameFingerprint(base.Fingerprint, changedTimeout.Fingerprint) {
		t.Fatal("heartbeat timeout did not affect runner acceptance fingerprint")
	}
	changedResume := base
	changedResume.FleetRunnerAttempt = cloneFleetRunnerAttemptAdmission(base.FleetRunnerAttempt)
	changedResume.FleetRunnerAttempt.Resume = true
	changedResume.Semantics = cloneStringInterfaceMap(base.Semantics)
	changedResume.Semantics["resume"] = true
	changedResume.Fingerprint, _ = CanonicalOperationRequestFingerprint(changedResume)
	if sameFingerprint(base.Fingerprint, changedResume.Fingerprint) {
		t.Fatal("resume did not affect runner acceptance fingerprint")
	}
	raw := base
	raw.Identity.Key = "runner-raw-nonce"
	raw.Operation.ID = uuid.NewString()
	raw.FleetRunnerAttempt = cloneFleetRunnerAttemptAdmission(base.FleetRunnerAttempt)
	raw.FleetRunnerAttempt.AttemptID = uuid.NewString()
	raw.Operation.Payload = cloneStringInterfaceMap(base.Operation.Payload)
	raw.Operation.Payload["attemptId"] = raw.FleetRunnerAttempt.AttemptID
	raw.Semantics = map[string]interface{}{"request": fleet.RunnerAttemptCreateRequest{DispatchNonce: rawNonce}}
	raw.Fingerprint, _ = CanonicalOperationRequestFingerprint(raw)
	if _, err := stores[0].Accept(context.Background(), raw); !errors.Is(err, ErrAcceptanceInvalid) {
		t.Fatalf("typed raw nonce semantics accepted: %v", err)
	}
}

func TestFleetRunnerAttemptRecoveryRollbackAndConcurrentSuccessor(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 2)
	plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
	_, nonceHash := insertFleetAttemptDispatch(t, dbs[0], plan.ID)
	initialRequest := newFleetRunnerAttemptAcceptance(t, stores[0], plan.ID, "runner-initial", nonceHash, "7", "apply", false, "", 120)
	initial, err := stores[0].Accept(context.Background(), initialRequest)
	if err != nil {
		t.Fatal(err)
	}
	predecessor := initial.FleetRunnerAttempt

	failed := newFleetRunnerAttemptAcceptance(t, stores[0], plan.ID, "runner-failed-recovery", nonceHash, "8", "recover", true, predecessor.ID, 180)
	stores[0].test.beforeIntent = func() error { return errors.New("forced runner intent failure") }
	if _, err := stores[0].Accept(context.Background(), failed); err == nil {
		t.Fatal("forced runner intent failure accepted successor")
	}
	stores[0].test.beforeIntent = nil
	var status string
	var revision int64
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT status,revision FROM fleet_runner_attempts WHERE id=$1`, predecessor.ID).Scan(&status, &revision); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || revision != predecessor.Revision {
		t.Fatalf("rolled-back recovery mutated predecessor status=%q revision=%d", status, revision)
	}

	requests := []OperationAcceptance{
		newFleetRunnerAttemptAcceptance(t, stores[0], plan.ID, "runner-recovery-a", nonceHash, "8", "recover", true, predecessor.ID, 180),
		newFleetRunnerAttemptAcceptance(t, stores[1], plan.ID, "runner-recovery-b", nonceHash, "9", "recover", true, predecessor.ID, 180),
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index := range requests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, err := stores[index].Accept(context.Background(), requests[index])
			results <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrFleetRunnerAttemptAdmission) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent recovery error: %v", err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("concurrent recoveries accepted=%d rejected=%d", accepted, rejected)
	}
	var attempts, identities int
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT count(*) FROM fleet_runner_attempts WHERE plan_id=$1`, plan.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_request_identities WHERE kind='fleet.runner-attempt' AND resource=$1`, plan.ID).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || identities != 2 {
		t.Fatalf("concurrent recovery rows attempts=%d identities=%d", attempts, identities)
	}
}

func TestFleetRunnerAttemptResolveRejectsLineageTampering(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
	_, nonceHash := insertFleetAttemptDispatch(t, dbs[0], plan.ID)
	initialRequest := newFleetRunnerAttemptAcceptance(t, stores[0], plan.ID, "lineage-root", nonceHash, "7", "apply", false, "", 120)
	initial, err := stores[0].Accept(context.Background(), initialRequest)
	if err != nil {
		t.Fatal(err)
	}
	recoveryRequest := newFleetRunnerAttemptAcceptance(t, stores[0], plan.ID, "lineage-recovery", nonceHash, "8", "recover", true, initial.FleetRunnerAttempt.ID, 120)
	recovery, err := stores[0].Accept(context.Background(), recoveryRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Resolve(context.Background(), recoveryRequest.Identity, recoveryRequest.Fingerprint); err != nil {
		t.Fatalf("pristine accepted runner lineage did not resolve: %v", err)
	}
	attempt := recovery.FleetRunnerAttempt
	for _, test := range []struct {
		name   string
		column string
		value  interface{}
	}{
		{name: "attempt-number", column: "attempt", value: 9},
		{name: "root", column: "root_attempt_id", value: uuid.NewString()},
		{name: "retry", column: "retry_of", value: uuid.NewString()},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := `UPDATE fleet_runner_attempts SET ` + test.column + `=$2 WHERE id=$1`
			if _, err := dbs[0].Pool.Exec(context.Background(), query, attempt.ID, test.value); err != nil {
				t.Fatal(err)
			}
			if _, err := stores[0].Resolve(context.Background(), recoveryRequest.Identity, recoveryRequest.Fingerprint); !errors.Is(err, ErrAcceptanceSignature) {
				t.Fatalf("tampered %s resolved: %v", test.name, err)
			}
			if _, err := dbs[0].Pool.Exec(context.Background(), `UPDATE fleet_runner_attempts SET attempt=$2,root_attempt_id=$3,retry_of=$4 WHERE id=$1`, attempt.ID, attempt.Attempt, attempt.RootAttemptID, attempt.RetryOf); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := dbs[0].Pool.Exec(context.Background(), `DELETE FROM fleet_runner_attempts WHERE id=$1`, initial.FleetRunnerAttempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Resolve(context.Background(), recoveryRequest.Identity, recoveryRequest.Fingerprint); !errors.Is(err, ErrAcceptanceSignature) {
		t.Fatalf("missing lineage ancestor resolved: %v", err)
	}
}

func TestFleetRunnerAttemptReadsProjectExpiryWithoutMutation(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
	_, nonceHash := insertFleetAttemptDispatch(t, dbs[0], plan.ID)
	request := newFleetRunnerAttemptAcceptance(t, stores[0], plan.ID, "runner-expiry-read", nonceHash, "7", "apply", false, "", 120)
	accepted, err := stores[0].Accept(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	attempt := accepted.FleetRunnerAttempt
	if _, err := dbs[0].Pool.Exec(context.Background(), `UPDATE fleet_runner_attempts SET heartbeat_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	read, err := dbs[0].GetFleetRunnerAttempt(context.Background(), plan.ID, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := dbs[0].ListFleetRunnerAttempts(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Status != "abandoned" || len(listed) != 1 || listed[0].Status != "abandoned" || !strings.Contains(read.LastError, "termination unproven") {
		t.Fatalf("expired read projection get=%#v list=%#v", read, listed)
	}
	var storedStatus string
	var storedRevision int64
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT status,revision FROM fleet_runner_attempts WHERE id=$1`, attempt.ID).Scan(&storedStatus, &storedRevision); err != nil {
		t.Fatal(err)
	}
	if storedStatus != "queued" || storedRevision != attempt.Revision {
		t.Fatalf("read path mutated expired attempt status=%q revision=%d", storedStatus, storedRevision)
	}
}

func TestFinishFleetGitHubDispatchIsImmutableOrIdentical(t *testing.T) {
	_, dbs := acceptanceIntegrationStores(t, 1)
	plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
	_, nonceHash := insertFleetAttemptDispatch(t, dbs[0], plan.ID)
	if _, err := dbs[0].FinishFleetGitHubDispatch(context.Background(), plan.ID, nonceHash, 0, ""); err == nil {
		t.Fatal("invalid dispatch completion was accepted")
	}
	if _, err := dbs[0].FinishFleetGitHubDispatch(context.Background(), plan.ID, nonceHash, 7, "https://github.com/acme/fleet/actions/runs/7"); err != nil {
		t.Fatalf("identical dispatch completion did not replay: %v", err)
	}
	if _, err := dbs[0].FinishFleetGitHubDispatch(context.Background(), plan.ID, nonceHash, 8, "https://github.com/acme/fleet/actions/runs/8"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("dispatch run binding was rewritten: %v", err)
	}
	current, err := dbs[0].GetFleetGitHubDispatch(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.RunID != 7 || current.WorkflowURL != "https://github.com/acme/fleet/actions/runs/7" {
		t.Fatalf("dispatch binding changed: %#v", current)
	}
}

func cloneFleetRunnerAttemptAdmission(input *FleetRunnerAttemptAdmission) *FleetRunnerAttemptAdmission {
	copy := *input
	return &copy
}

func cloneStringInterfaceMap(input map[string]interface{}) map[string]interface{} {
	copy := make(map[string]interface{}, len(input))
	for key, value := range input {
		copy[key] = value
	}
	return copy
}
