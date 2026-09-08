package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
)

func TestExternalDeploymentNonceIssuanceCapAndExpiryCleanup(t *testing.T) {
	if os.Getenv("NORN_TEST_DATABASE_URL") == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(os.Getenv("NORN_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := ExternalDeploymentNonce{App: "hello-norn-mysql", Environment: "staging", CIRepository: "acme/norn-fleet", CIRunID: "nonce-cap-" + uuid.NewString(), CIRunAttempt: "1", ExpiresAt: time.Now().Add(time.Hour)}
	issue := func(index int) error {
		nonce := base
		nonce.ID, nonce.NonceSHA256 = uuid.NewString(), fmt.Sprintf("%064x", index+1)
		return db.IssueExternalDeploymentNonce(ctx, nonce)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			errs <- issue(value)
		}(index)
	}
	wg.Wait()
	close(errs)
	var issued, capped int
	for err := range errs {
		if err == nil {
			issued++
		} else if errors.Is(err, ErrExternalDeploymentNonceLimit) {
			capped++
		} else {
			t.Fatalf("concurrent nonce issue: %v", err)
		}
	}
	if issued != externalDeploymentNonceMaxOutstandingPerRun || capped != 8-externalDeploymentNonceMaxOutstandingPerRun {
		t.Fatalf("concurrent nonce cap issued=%d capped=%d", issued, capped)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM external_deployment_nonces WHERE ci_run_id=$1`, base.CIRunID)
	})

	expiredConsumed, expiredUnconsumed := uuid.NewString(), uuid.NewString()
	for index, id := range []string{expiredConsumed, expiredUnconsumed} {
		consumedAt := interface{}(nil)
		if index == 0 {
			consumedAt = time.Now().Add(-time.Hour)
		}
		if _, err := db.Pool.Exec(ctx, `INSERT INTO external_deployment_nonces (id, nonce_sha256, app, environment, ci_repository, ci_run_id, ci_run_attempt, expires_at, consumed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, fmt.Sprintf("%064x", index+100), base.App, base.Environment, base.CIRepository, "expired-"+base.CIRunID, base.CIRunAttempt, time.Now().Add(-time.Hour), consumedAt); err != nil {
			t.Fatal(err)
		}
	}
	cleanupTrigger := base
	cleanupTrigger.ID, cleanupTrigger.NonceSHA256, cleanupTrigger.CIRunID = uuid.NewString(), fmt.Sprintf("%064x", 200), "cleanup-"+base.CIRunID
	if err := db.IssueExternalDeploymentNonce(ctx, cleanupTrigger); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM external_deployment_nonces WHERE ci_run_id=$1`, cleanupTrigger.CIRunID)
	})
	var expiredRemaining int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM external_deployment_nonces WHERE id IN ($1,$2)`, expiredConsumed, expiredUnconsumed).Scan(&expiredRemaining); err != nil || expiredRemaining != 0 {
		t.Fatalf("expired consumed/unconsumed cleanup remaining=%d err=%v", expiredRemaining, err)
	}
}

// TestExternalDeploymentAdmissionV4Lifecycle proves the nonce is persisted as
// registering before disclosure, becomes claimable only after registration is
// acknowledged, and commits checkpoint pointers with the terminal receipt.
func TestExternalDeploymentAdmissionV4Lifecycle(t *testing.T) {
	if os.Getenv("NORN_TEST_DATABASE_URL") == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(os.Getenv("NORN_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admissionID, key := uuid.NewString(), "external-v4-"+uuid.NewString()
	logicalDigest := "sha256:" + fmt.Sprintf("%064x", 7)
	started, err := db.BeginExternalDeploymentAdmission(ctx, admissionID, key, logicalDigest, "hello-norn-mysql", "staging", "acme/norn-fleet")
	if err != nil || started.State != ExternalDeploymentAdmissionInitiated {
		t.Fatalf("begin admission = %+v, %v", started, err)
	}
	replay, err := db.BeginExternalDeploymentAdmission(ctx, uuid.NewString(), key, logicalDigest, "hello-norn-mysql", "staging", "acme/norn-fleet")
	if err != nil || replay.ID != admissionID || replay.State != ExternalDeploymentAdmissionInitiated {
		t.Fatalf("exact begin replay = %+v, %v", replay, err)
	}
	if _, err := db.BeginExternalDeploymentAdmission(ctx, uuid.NewString(), key, "sha256:different", "hello-norn-mysql", "staging", "acme/norn-fleet"); !errors.Is(err, ErrExternalDeploymentIdempotencyConflict) {
		t.Fatalf("different logical replay error = %v, want idempotency conflict", err)
	}

	nonce := ExternalDeploymentNonce{
		ID: uuid.NewString(), NonceSHA256: fmt.Sprintf("%064x", 8), App: "hello-norn-mysql", Environment: "staging", CIRepository: "acme/norn-fleet", CIRunID: "v4-" + uuid.NewString(), CIRunAttempt: "1", ExpiresAt: time.Now().Add(time.Hour),
		AdmissionID: admissionID, RegistrationGeneration: 1, RegistrationRef: "nonce-registration-1", IssuerSubject: "repo:acme/norn-fleet", IssuerTokenID: "oidc-jti", RegistrationMetadata: map[string]string{"workflow": "fleet-apply"},
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM operations WHERE metadata->>'idempotencyKey'=$1`, key)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM external_deployment_admissions WHERE id=$1`, admissionID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM external_deployment_nonces WHERE id=$1`, nonce.ID)
	})
	if err := db.IssueExternalDeploymentNonce(ctx, nonce); err != nil {
		t.Fatal(err)
	}
	registration, err := db.GetExternalDeploymentNonceRegistration(ctx, admissionID, nonce.ID)
	if err != nil || registration.State != "registering" || registration.Generation != 1 || registration.RegistrationRef != nonce.RegistrationRef || registration.Revision != 1 || registration.RegisteredAt != nil || registration.RegistrationMetadata["workflow"] != "fleet-apply" {
		t.Fatalf("persisted registration = %+v, %v", registration, err)
	}
	var nonceState, admissionState string
	var generation, revision int64
	if err := db.Pool.QueryRow(ctx, `SELECT n.state, a.state, n.registration_generation, n.revision FROM external_deployment_nonces n JOIN external_deployment_admissions a ON a.nonce_id=n.id WHERE n.id=$1`, nonce.ID).Scan(&nonceState, &admissionState, &generation, &revision); err != nil || nonceState != "registering" || admissionState != string(ExternalDeploymentAdmissionNonceRegistering) || generation != 1 || revision != 1 {
		t.Fatalf("pre-disclosure nonce state=%q admission=%q generation=%d revision=%d err=%v", nonceState, admissionState, generation, revision, err)
	}
	if err := db.MarkExternalDeploymentNonceReady(ctx, admissionID, nonce.ID, 1, nonce.RegistrationRef, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkExternalDeploymentNonceReady(ctx, admissionID, nonce.ID, 1, nonce.RegistrationRef, 1); err != nil {
		t.Fatalf("exact ready replay = %v", err)
	}
	if err := db.ClaimExternalDeploymentAdmissionEvidence(ctx, admissionID, nonce.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimExternalDeploymentAdmissionEvidence(ctx, admissionID, nonce.ID); err != nil {
		t.Fatalf("exact evidence claim replay = %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT n.state, a.state, n.revision FROM external_deployment_nonces n JOIN external_deployment_admissions a ON a.nonce_id=n.id WHERE n.id=$1`, nonce.ID).Scan(&nonceState, &admissionState, &revision); err != nil || nonceState != "claimed" || admissionState != string(ExternalDeploymentAdmissionEvidenceClaimed) || revision != 3 {
		t.Fatalf("claimed nonce state=%q admission=%q revision=%d err=%v", nonceState, admissionState, revision, err)
	}

	terminal := externalAdmissionForTest(nonce, key)
	terminal.RequestDigest = logicalDigest
	terminal.Operation.Metadata["requestDigest"] = logicalDigest
	terminal.AdmissionID, terminal.NonceGeneration = admissionID, 1
	terminal.CheckpointRefs = []ExternalDeploymentCheckpointRef{{Phase: "external_admission", CheckpointID: "admission-1", AttemptID: "attempt-1", EvidenceRef: "checkpoint://admission-1", EvidenceSHA256: fmt.Sprintf("%064x", 9)}}
	result, err := db.AdmitExternalDeployment(ctx, terminal)
	if err != nil || result == nil || result.Replayed {
		t.Fatalf("commit v4 admission result=%+v err=%v", result, err)
	}
	var checkpointID string
	if err := db.Pool.QueryRow(ctx, `SELECT a.state, a.operation_id, c.checkpoint_id FROM external_deployment_admissions a JOIN external_deployment_admission_checkpoints c ON c.admission_id=a.id WHERE a.id=$1`, admissionID).Scan(&admissionState, new(string), &checkpointID); err != nil || admissionState != string(ExternalDeploymentAdmissionCommitted) || checkpointID != "admission-1" {
		t.Fatalf("committed lifecycle state=%q checkpoint=%q err=%v", admissionState, checkpointID, err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT checkpoint_id FROM fleet_runner_checkpoint_refs WHERE admission_id=$1 AND phase='external_admission'`, admissionID).Scan(&checkpointID); err != nil || checkpointID != "admission-1" {
		t.Fatalf("runner checkpoint ledger=%q err=%v", checkpointID, err)
	}
	conflictingReplay := terminal
	conflictingReplay.CheckpointRefs = append([]ExternalDeploymentCheckpointRef(nil), terminal.CheckpointRefs...)
	conflictingReplay.CheckpointRefs[0].EvidenceRef = "checkpoint://conflicting-migration"
	if _, err := db.AdmitExternalDeployment(ctx, conflictingReplay); !errors.Is(err, ErrExternalDeploymentCheckpointConflict) {
		t.Fatalf("conflicting checkpoint replay error=%v, want checkpoint conflict", err)
	}
	if err := db.MarkExternalDeploymentAdmissionCleanupPending(ctx, admissionID); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteExternalDeploymentAdmission(ctx, admissionID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM external_deployment_admissions WHERE id=$1`, admissionID).Scan(&admissionState); err != nil || admissionState != string(ExternalDeploymentAdmissionComplete) {
		t.Fatalf("complete lifecycle state=%q err=%v", admissionState, err)
	}
}

// TestExternalDeploymentAdmissionAtomicReplayAndRace proves the three failure
// boundaries that matter for the external bridge: a failed terminal write does
// not burn the nonce, a successful exact replay returns the same receipt, and
// concurrent same-key callers cannot create a second deployment. It is opt-in
// because it exercises real PostgreSQL transaction semantics.
func TestExternalDeploymentAdmissionAtomicReplayAndRace(t *testing.T) {
	if os.Getenv("NORN_TEST_DATABASE_URL") == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(os.Getenv("NORN_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	nonce := ExternalDeploymentNonce{ID: uuid.NewString(), NonceSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", App: "hello-norn-mysql", Environment: "staging", CIRepository: "acme/norn-fleet", CIRunID: "101", CIRunAttempt: "1", ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.IssueExternalDeploymentNonce(ctx, nonce); err != nil {
		t.Fatal(err)
	}
	base := externalAdmissionForTest(nonce, "external-replay-"+uuid.NewString())
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM operations WHERE metadata->>'idempotencyKey'=$1`, base.IdempotencyKey)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM deployments WHERE saga_id=$1`, "external-fleet:"+nonce.ID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM external_deployment_nonces WHERE id=$1`, nonce.ID)
	})
	// Deliberately violate the deployment-region primary key after the nonce
	// CAS. The transaction must roll back its nonce consumption as well.
	broken := base
	broken.Regions = append(append([]model.DeploymentRegion(nil), base.Regions...), base.Regions[0])
	if _, err := db.AdmitExternalDeployment(ctx, broken); err == nil {
		t.Fatal("broken terminal write unexpectedly succeeded")
	}
	createdResult, err := db.AdmitExternalDeployment(ctx, base)
	if err != nil {
		t.Fatalf("nonce was burned by rolled-back terminal write: %v", err)
	}
	if createdResult.Replayed || createdResult.Operation.ID != base.Operation.ID {
		t.Fatalf("initial admission result = %+v", createdResult)
	}
	var finishedAt *time.Time
	if err := db.Pool.QueryRow(ctx, `SELECT finished_at FROM deployments WHERE id=$1`, base.Deployment.ID).Scan(&finishedAt); err != nil || finishedAt == nil {
		t.Fatalf("terminal deployment finished_at was not committed: finishedAt=%v err=%v", finishedAt, err)
	}
	var desiredWeight, activeWeight int
	var evalID string
	if err := db.Pool.QueryRow(ctx, `SELECT desired_weight, active_weight, eval_id FROM deployment_regions WHERE deployment_id=$1 AND region=$2`, base.Deployment.ID, base.Regions[0].Region).Scan(&desiredWeight, &activeWeight, &evalID); err != nil || desiredWeight != 100 || activeWeight != 100 || evalID != base.Regions[0].EvalID {
		t.Fatalf("verified regional terminal evidence was not committed: desired=%d active=%d eval=%q err=%v", desiredWeight, activeWeight, evalID, err)
	}
	replay, err := db.AdmitExternalDeployment(ctx, base)
	if err != nil || !replay.Replayed || replay.Operation.ID != base.Operation.ID {
		t.Fatalf("exact replay=%+v err=%v", replay, err)
	}

	racingNonce := nonce
	racingNonce.ID = uuid.NewString()
	racingNonce.NonceSHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := db.IssueExternalDeploymentNonce(ctx, racingNonce); err != nil {
		t.Fatal(err)
	}
	racing := externalAdmissionForTest(racingNonce, "external-race-"+uuid.NewString())
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM operations WHERE metadata->>'idempotencyKey'=$1`, racing.IdempotencyKey)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM deployments WHERE saga_id=$1`, "external-fleet:"+racingNonce.ID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM external_deployment_nonces WHERE id=$1`, racingNonce.ID)
	})
	var wg sync.WaitGroup
	results := make(chan *ExternalDeploymentAdmissionResult, 2)
	errs := make(chan error, 2)
	for index := 0; index < 2; index++ {
		candidate := racing
		candidate.Deployment = cloneExternalDeployment(racing.Deployment)
		candidate.Operation = cloneExternalOperation(racing.Operation)
		if index == 1 {
			candidate.Deployment.ID = uuid.NewString()
			candidate.Operation.ID = uuid.NewString()
		}
		wg.Add(1)
		go func(value ExternalDeploymentAdmission) {
			defer wg.Done()
			result, err := db.AdmitExternalDeployment(ctx, value)
			results <- result
			errs <- err
		}(candidate)
	}
	wg.Wait()
	close(results)
	close(errs)
	var created, replayed int
	for err := range errs {
		if err != nil && !errors.Is(err, ErrExternalDeploymentNonceConsumed) {
			t.Fatalf("race admission failed: %v", err)
		}
	}
	for result := range results {
		if result == nil {
			continue
		}
		if result.Replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != 1 {
		t.Fatalf("race created=%d replayed=%d, want exactly one each", created, replayed)
	}

	// Different nonces may race on the same idempotency key. The losing
	// transaction reaches the database unique index, not the initial lookup,
	// and must still surface the typed idempotency conflict.
	leftNonce := racingNonce
	leftNonce.ID, leftNonce.NonceSHA256 = uuid.NewString(), "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	rightNonce := racingNonce
	rightNonce.ID, rightNonce.NonceSHA256 = uuid.NewString(), "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	for _, issued := range []ExternalDeploymentNonce{leftNonce, rightNonce} {
		if err := db.IssueExternalDeploymentNonce(ctx, issued); err != nil {
			t.Fatal(err)
		}
	}
	conflictKey := "external-conflict-" + uuid.NewString()
	left, right := externalAdmissionForTest(leftNonce, conflictKey), externalAdmissionForTest(rightNonce, conflictKey)
	right.RequestDigest = "different-" + right.RequestDigest
	right.Operation.Metadata["requestDigest"] = right.RequestDigest
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM operations WHERE metadata->>'idempotencyKey'=$1`, conflictKey)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM deployments WHERE saga_id IN ($1,$2)`, "external-fleet:"+leftNonce.ID, "external-fleet:"+rightNonce.ID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM external_deployment_nonces WHERE id IN ($1,$2)`, leftNonce.ID, rightNonce.ID)
	})
	errs = make(chan error, 2)
	for _, value := range []ExternalDeploymentAdmission{left, right} {
		wg.Add(1)
		go func(admission ExternalDeploymentAdmission) {
			defer wg.Done()
			_, err := db.AdmitExternalDeployment(ctx, admission)
			errs <- err
		}(value)
	}
	wg.Wait()
	close(errs)
	var admitted, conflicts int
	for err := range errs {
		if err == nil {
			admitted++
		} else if errors.Is(err, ErrExternalDeploymentIdempotencyConflict) {
			conflicts++
		} else {
			t.Fatalf("different-nonce idempotency race err=%v", err)
		}
	}
	if admitted != 1 || conflicts != 1 {
		t.Fatalf("different-nonce idempotency race admitted=%d conflicts=%d", admitted, conflicts)
	}
}

func externalAdmissionForTest(nonce ExternalDeploymentNonce, key string) ExternalDeploymentAdmission {
	now := time.Now().UTC()
	finished := now
	deploymentID := uuid.NewString()
	operationID := uuid.NewString()
	return ExternalDeploymentAdmission{
		Nonce:          nonce,
		Deployment:     &model.Deployment{ID: deploymentID, App: nonce.App, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ImageTag: "ghcr.io/acme/hello-norn-mysql@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Environment: nonce.Environment, SagaID: "external-fleet:" + nonce.ID, Status: model.StatusDeployed, SourceKind: "external-fleet", SourceRef: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", StartedAt: now, FinishedAt: &finished},
		Regions:        []model.DeploymentRegion{{Region: "global", NomadRegion: "global", Status: model.StatusDeployed, DesiredWeight: 100, ActiveWeight: 100, EvalID: "00000000-0000-4000-8000-000000000010"}},
		Operation:      &model.Operation{ID: operationID, Kind: "app.deploy", App: nonce.App, SagaID: "external-fleet:" + nonce.ID, Ref: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: model.OperationSucceeded, Source: "external-fleet-admission", Payload: map[string]interface{}{"deploymentId": deploymentID}, Metadata: map[string]interface{}{"idempotencyKey": key, "requestDigest": "request-" + key}, StartedAt: now, FinishedAt: &finished, MaxAttempts: 1},
		IdempotencyKey: key, RequestDigest: "request-" + key,
	}
}

func cloneExternalDeployment(value *model.Deployment) *model.Deployment { copy := *value; return &copy }
func cloneExternalOperation(value *model.Operation) *model.Operation {
	copy := *value
	copy.Payload = map[string]interface{}{"deploymentId": value.Payload["deploymentId"]}
	copy.Metadata = map[string]interface{}{"idempotencyKey": value.Metadata["idempotencyKey"], "requestDigest": value.Metadata["requestDigest"]}
	return &copy
}
