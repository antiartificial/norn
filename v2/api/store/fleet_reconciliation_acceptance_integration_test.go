package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

func insertFleetAcceptancePlan(t *testing.T, db *DB, action string) model.Operation {
	t.Helper()
	now := time.Now().UTC()
	finished := now
	operation := model.Operation{
		ID: uuid.NewString(), Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded,
		Source: "control-api", StartedAt: now, FinishedAt: &finished, MaxAttempts: 1,
		Payload: map[string]interface{}{
			"id": uuid.NewString(), "pool": "app", "action": action,
			"digest":  "sha256:" + strings.Repeat("a", 64),
			"current": map[string]interface{}{"desired": 2}, "proposed": map[string]interface{}{"desired": 3},
		}, Metadata: map[string]interface{}{},
	}
	operation.Payload["id"] = operation.ID
	if err := db.InsertCompletedOperation(context.Background(), &operation); err != nil {
		t.Fatal(err)
	}
	return operation
}

func createFleetAcceptanceAttempt(t *testing.T, db *DB, planID, phase string, leaseSeconds int) fleet.RunnerAttempt {
	t.Helper()
	runnerAttemptID := "github-run-" + uuid.NewString()
	tx, err := db.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+planID); err != nil {
		t.Fatal(err)
	}
	var databaseNow time.Time
	if err := tx.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `UPDATE fleet_runner_attempts SET status='abandoned',message='test fixture expiry',revision=revision+1,updated_at=$2,finished_at=$2 WHERE plan_id=$1 AND status IN ('queued','running') AND heartbeat_expires_at <= $2`, planID, databaseNow); err != nil {
		t.Fatal(err)
	}
	var attemptNumber int
	if err := tx.QueryRow(context.Background(), `SELECT COALESCE(MAX(attempt),0)+1 FROM fleet_runner_attempts WHERE plan_id=$1`, planID).Scan(&attemptNumber); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	rootID := id
	retryOf := ""
	if attemptNumber > 1 {
		if err := tx.QueryRow(context.Background(), `SELECT root_attempt_id,id FROM fleet_runner_attempts WHERE plan_id=$1 ORDER BY attempt DESC LIMIT 1`, planID).Scan(&rootID, &retryOf); err != nil {
			t.Fatal(err)
		}
	}
	attempt := fleet.RunnerAttempt{SchemaVersion: fleet.RunnerAttemptSchemaVersion, ID: id, PlanID: planID, Attempt: attemptNumber, RunnerAttemptID: runnerAttemptID,
		CommitSHA: strings.Repeat("c", 40), PlanSHA256: strings.Repeat("b", 64), WorkflowURL: "https://github.com/acme/fleet/actions/runs/" + runnerAttemptID,
		Status: "queued", CurrentPhase: phase, RootAttemptID: rootID, RetryOf: retryOf, HeartbeatTimeoutSeconds: leaseSeconds, Revision: 1,
		HeartbeatAt: databaseNow, HeartbeatExpiresAt: databaseNow.Add(time.Duration(leaseSeconds) * time.Second), StartedAt: databaseNow, UpdatedAt: databaseNow}
	if _, err := tx.Exec(context.Background(), `INSERT INTO fleet_runner_attempts (`+fleetRunnerAttemptColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, attempt.ID, attempt.PlanID, attempt.Attempt, attempt.RunnerAttemptID, attempt.CommitSHA, attempt.PlanSHA256, attempt.WorkflowURL, attempt.Status, attempt.CurrentPhase, attempt.RootAttemptID, attempt.RetryOf, attempt.HeartbeatSequence, attempt.HeartbeatTimeoutSeconds, attempt.Revision, attempt.HeartbeatAt, attempt.HeartbeatExpiresAt, attempt.LastError, attempt.StartedAt, attempt.UpdatedAt, attempt.FinishedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return attempt
}

func newFleetReconciliationAcceptance(t *testing.T, operationStore *PGOperationStore, plan model.Operation, attempt fleet.RunnerAttempt, key, phase, evidenceStatus string, requireActive bool) OperationAcceptance {
	t.Helper()
	authority, err := operationStore.Authority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := fleet.ReconciliationRequest{
		SchemaVersion: fleet.ReconciliationSchemaVersion, Phase: phase, Status: evidenceStatus,
		CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256,
		EvidenceDigest: "sha256:" + strings.Repeat("d", 64), AttemptID: attempt.ID,
	}
	payload, err := jsonMarshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var payloadMap map[string]interface{}
	if err := jsonUnmarshal(payload, &payloadMap); err != nil {
		t.Fatal(err)
	}
	status := model.OperationSucceeded
	if evidenceStatus == "failed" {
		status = model.OperationFailed
	}
	acceptance := OperationAcceptance{
		Identity:            OperationRequestIdentity{Authority: authority, Actor: OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "owner:repo:run:1"}, Kind: "fleet.reconciliation", Resource: plan.ID, Key: key},
		Operation:           model.Operation{ID: uuid.NewString(), Kind: "fleet.reconciliation", Ref: plan.ID, Status: status, Source: "fleet-runner", Risk: "append-only infrastructure reconciliation evidence", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: payloadMap, Metadata: map[string]interface{}{}},
		Audit:               AcceptanceAuditContext{Source: "integration-test"},
		Semantics:           map[string]interface{}{"action": "fleet.reconciliation", "planId": plan.ID, "request": request},
		FleetReconciliation: &FleetReconciliationAdmission{PlanID: plan.ID, AttemptID: attempt.ID, RequireActiveAttempt: requireActive, RunnerAttemptID: attempt.RunnerAttemptID, WorkflowURL: attempt.WorkflowURL},
	}
	acceptance.Fingerprint, err = CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	return acceptance
}

func waitForFleetLockWait(t *testing.T, db *DB, marker, attemptID string, requireStartBeforeExpiry bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var startedBeforeExpiry bool
		err := db.Pool.QueryRow(context.Background(), `
			SELECT a.xact_start < f.heartbeat_expires_at
			FROM pg_stat_activity a
			JOIN fleet_runner_attempts f ON f.id=$2
			WHERE a.datname=current_database()
			  AND a.query LIKE '%' || $1 || '%'
			  AND a.wait_event_type='Lock'
			LIMIT 1`, marker, attemptID).Scan(&startedBeforeExpiry)
		if err == nil && (!requireStartBeforeExpiry || startedBeforeExpiry) {
			return
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("backend did not reach controlled %s lock wait before lease expiry", marker)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForFleetAttemptExpiry(t *testing.T, db *DB, attemptID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var expired bool
		if err := db.Pool.QueryRow(context.Background(), `SELECT clock_timestamp() >= heartbeat_expires_at FROM fleet_runner_attempts WHERE id=$1`, attemptID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("fleet runner attempt lease did not expire")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFleetReconciliationAcceptanceReplaysAfterAttemptAdvanceAndExpiry(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
	attempt := createFleetAcceptanceAttempt(t, dbs[0], plan.ID, "prechange_verified", 60)
	acceptance := newFleetReconciliationAcceptance(t, stores[0], plan, attempt, "replay-after-state-change", "prechange_verified", "succeeded", true)
	first, err := stores[0].Accept(context.Background(), acceptance)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := dbs[0].UpdateFleetRunnerAttempt(context.Background(), plan.ID, attempt.ID, attempt.Revision, "advance", "provider_applying")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(context.Background(), `UPDATE fleet_runner_attempts SET heartbeat_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err := stores[0].Accept(context.Background(), acceptance)
	if err != nil {
		t.Fatalf("accepted evidence did not replay after attempt changed: %v", err)
	}
	if replayed.Operation.ID != first.Operation.ID || !replayed.Replayed {
		t.Fatalf("replay changed operation: first=%q replay=%q replayed=%v", first.Operation.ID, replayed.Operation.ID, replayed.Replayed)
	}
	var status string
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT status FROM fleet_runner_attempts WHERE id=$1`, attempt.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != advanced.Status {
		t.Fatalf("identity replay performed expiry reconciliation: status=%q want=%q", status, advanced.Status)
	}
}

func TestFleetReconciliationAcceptanceRejectsMalformedAndIncompatibleEvidenceAtomically(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
	attempt := createFleetAcceptanceAttempt(t, dbs[0], plan.ID, "prechange_verified", 60)

	missingAdmission := newFleetReconciliationAcceptance(t, stores[0], plan, attempt, "missing-admission", "prechange_verified", "succeeded", true)
	missingAdmission.FleetReconciliation = nil
	missingAdmission.Fingerprint, _ = CanonicalOperationRequestFingerprint(missingAdmission)
	if _, err := stores[0].Accept(context.Background(), missingAdmission); !errors.Is(err, ErrAcceptanceInvalid) {
		t.Fatalf("missing typed admission error=%v", err)
	}
	statusMismatch := newFleetReconciliationAcceptance(t, stores[0], plan, attempt, "status-mismatch", "prechange_verified", "failed", true)
	statusMismatch.Operation.Status = model.OperationSucceeded
	statusMismatch.Fingerprint, _ = CanonicalOperationRequestFingerprint(statusMismatch)
	if _, err := stores[0].Accept(context.Background(), statusMismatch); !errors.Is(err, ErrAcceptanceInvalid) {
		t.Fatalf("status mismatch error=%v", err)
	}
	incompatible := newFleetReconciliationAcceptance(t, stores[0], plan, attempt, "incompatible", "prechange_verified", "succeeded", true)
	incompatible.Operation.Payload["commitSha"] = strings.Repeat("e", 40)
	incompatibleRequest := incompatible.Semantics["request"].(fleet.ReconciliationRequest)
	incompatibleRequest.CommitSHA = strings.Repeat("e", 40)
	incompatible.Semantics["request"] = incompatibleRequest
	incompatible.Fingerprint, _ = CanonicalOperationRequestFingerprint(incompatible)
	if _, err := stores[0].Accept(context.Background(), incompatible); !errors.Is(err, ErrFleetReconciliationAdmission) {
		t.Fatalf("incompatible attempt evidence error=%v", err)
	}
	var leaked int
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_request_identities WHERE request_key IN ('missing-admission','status-mismatch','incompatible')`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("invalid reconciliation leaked %d identities", leaked)
	}
}

func TestFleetReconciliationAcceptanceUsesWallClockAfterLockWaitAndRollsBackIntentFailure(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 2)
	plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
	attempt := createFleetAcceptanceAttempt(t, dbs[0], plan.ID, "prechange_verified", 60)
	if _, err := dbs[0].Pool.Exec(context.Background(), `UPDATE fleet_runner_attempts SET heartbeat_expires_at=clock_timestamp()+interval '500 milliseconds' WHERE id=$1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	lock, err := dbs[0].Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(context.Background(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+plan.ID); err != nil {
		t.Fatal(err)
	}
	acceptance := newFleetReconciliationAcceptance(t, stores[1], plan, attempt, "expires-while-waiting", "prechange_verified", "succeeded", true)
	result := make(chan error, 1)
	go func() { _, err := stores[1].Accept(context.Background(), acceptance); result <- err }()
	waitForFleetLockWait(t, dbs[0], "fleet-reconciliation-admission-lock", attempt.ID, true)
	waitForFleetAttemptExpiry(t, dbs[0], attempt.ID)
	if err := lock.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrFleetReconciliationAdmission) {
		t.Fatalf("expired evidence admitted after lock wait: %v", err)
	}
	var expiredStatus string
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT status FROM fleet_runner_attempts WHERE id=$1`, attempt.ID).Scan(&expiredStatus); err != nil {
		t.Fatal(err)
	}
	if expiredStatus != "queued" {
		t.Fatalf("rejected admission mutated expired attempt status=%q", expiredStatus)
	}

	operatorAttempt := createFleetAcceptanceAttempt(t, dbs[0], plan.ID, "provider_applying", 60)
	rollback := newFleetReconciliationAcceptance(t, stores[0], plan, operatorAttempt, "intent-rollback", "provider_applying", "failed", false)
	stores[0].test.beforeIntent = func() error { return errors.New("forced intent failure") }
	if _, err := stores[0].Accept(context.Background(), rollback); err == nil {
		t.Fatal("forced intent failure accepted reconciliation")
	}
	stores[0].test.beforeIntent = nil
	var identities, operations int
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM operation_request_identities WHERE request_key='intent-rollback'),(SELECT count(*) FROM operations WHERE id=$1)`, rollback.Operation.ID).Scan(&identities, &operations); err != nil {
		t.Fatal(err)
	}
	if identities != 0 || operations != 0 {
		t.Fatalf("intent failure leaked identity=%d operation=%d", identities, operations)
	}
}

func TestFleetReconciliationAcceptanceSerializesWithAttemptCancellation(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 2)
	plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
	attempt := createFleetAcceptanceAttempt(t, dbs[0], plan.ID, "prechange_verified", 60)
	lock, err := dbs[0].Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(context.Background(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(context.Background(), `UPDATE fleet_runner_attempts SET status='canceled', revision=revision+1, finished_at=clock_timestamp() WHERE id=$1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	acceptance := newFleetReconciliationAcceptance(t, stores[1], plan, attempt, "cancel-race", "prechange_verified", "succeeded", true)
	result := make(chan error, 1)
	go func() { _, err := stores[1].Accept(context.Background(), acceptance); result <- err }()
	waitForFleetLockWait(t, dbs[0], "fleet-reconciliation-admission-lock", attempt.ID, false)
	if err := lock.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrFleetReconciliationAdmission) {
		t.Fatalf("canceled attempt admitted evidence: %v", err)
	}
	var identities int
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_request_identities WHERE request_key='cancel-race'`).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if identities != 0 {
		t.Fatalf("canceled admission leaked %d identities", identities)
	}
}

func TestFleetReconciliationAcceptanceUsesCompleteHistory(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
	attempt := createFleetAcceptanceAttempt(t, dbs[0], plan.ID, "provider_applying", 60)
	if _, err := dbs[0].Pool.Exec(context.Background(), `
		INSERT INTO operations (id,kind,ref,status,payload,started_at,updated_at,finished_at)
		VALUES ($1,'fleet.reconciliation',$2,'succeeded',jsonb_build_object(
			'phase','prechange_verified','commitSha',$3::text,'planSha256',$4::text
		),clock_timestamp()-interval '1 hour',clock_timestamp()-interval '1 hour',clock_timestamp()-interval '1 hour')`,
		"incompatible-oldest-"+uuid.NewString(), plan.ID, strings.Repeat("e", 40), attempt.PlanSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(context.Background(), `
		INSERT INTO operations (id,kind,ref,status,payload,started_at,updated_at,finished_at)
		SELECT $1 || '-history-' || n,'fleet.reconciliation',$2,'succeeded',jsonb_build_object(
			'phase','prechange_verified','commitSha',$3::text,'planSha256',$4::text
		),clock_timestamp()+(n * interval '1 millisecond'),clock_timestamp(),clock_timestamp()
		FROM generate_series(1,101) AS n`, uuid.NewString(), plan.ID, attempt.CommitSHA, attempt.PlanSHA256); err != nil {
		t.Fatal(err)
	}
	acceptance := newFleetReconciliationAcceptance(t, stores[0], plan, attempt, "complete-history", "provider_applying", "succeeded", true)
	if _, err := stores[0].Accept(context.Background(), acceptance); !errors.Is(err, ErrFleetReconciliationAdmission) {
		t.Fatalf("old incompatible reconciliation outside the newest 100 was ignored: %v", err)
	}
	var identities int
	if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_request_identities WHERE request_key='complete-history'`).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if identities != 0 {
		t.Fatalf("complete-history rejection leaked %d identities", identities)
	}
}

func TestFleetAttemptUpdateUsesWallClockAfterLockWait(t *testing.T) {
	_, dbs := acceptanceIntegrationStores(t, 2)
	for _, test := range []struct {
		name   string
		action string
		values []interface{}
	}{
		{name: "heartbeat", action: "heartbeat", values: []interface{}{int64(1), "still working"}},
		{name: "advance", action: "advance", values: []interface{}{"provider_applying"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := insertFleetAcceptancePlan(t, dbs[0], "scale")
			attempt := createFleetAcceptanceAttempt(t, dbs[0], plan.ID, "prechange_verified", 60)
			if _, err := dbs[0].Pool.Exec(context.Background(), `UPDATE fleet_runner_attempts SET heartbeat_expires_at=clock_timestamp()+interval '500 milliseconds' WHERE id=$1`, attempt.ID); err != nil {
				t.Fatal(err)
			}
			lock, err := dbs[0].Pool.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback(context.Background())
			if _, err := lock.Exec(context.Background(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+plan.ID); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_, err := dbs[1].UpdateFleetRunnerAttempt(context.Background(), plan.ID, attempt.ID, attempt.Revision, test.action, test.values...)
				result <- err
			}()
			waitForFleetLockWait(t, dbs[0], "fleet-runner-attempt-update-lock", attempt.ID, true)
			waitForFleetAttemptExpiry(t, dbs[0], attempt.ID)
			if err := lock.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("expired attempt accepted %s after lock wait: %v", test.action, err)
			}
			var status string
			var revision int64
			if err := dbs[0].Pool.QueryRow(context.Background(), `SELECT status,revision FROM fleet_runner_attempts WHERE id=$1`, attempt.ID).Scan(&status, &revision); err != nil {
				t.Fatal(err)
			}
			if status != "queued" || revision != attempt.Revision {
				t.Fatalf("rejected %s mutated expired attempt status=%q revision=%d", test.action, status, revision)
			}
		})
	}
}
