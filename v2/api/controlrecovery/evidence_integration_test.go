package controlrecovery

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// verifyWithin runs the restored-evidence verifier inside a transaction after
// applying a tamper statement, then rolls both back.
func verifyWithin(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string, material *RecoveryKeyMaterial, tamper string, args ...any) error {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if tamper != "" {
		result, err := tx.Exec(ctx, tamper, args...)
		if err != nil {
			t.Fatalf("tamper %q: %v", tamper, err)
		}
		if result.RowsAffected() == 0 {
			t.Fatalf("tamper %q affected no rows", tamper)
		}
	}
	return material.VerifyRestoredEvidence(ctx, tx, schema, material.KeyIDs())
}

func TestQualificationEvidenceBindsProducerReceiptToOuterRows(t *testing.T) {
	pool, schema := inspectionTestDatabase(t)
	ctx := context.Background()
	receipt, raw, public := loadQualificationVector(t)
	if _, err := pool.Exec(ctx, `INSERT INTO deployments(id,app,commit_sha,image_tag,environment,saga_id,status) VALUES($1,$2,$3,$4,'staging','saga-qualified','deployed')`,
		receipt.DeploymentID, receipt.App, receipt.SourceSHA, receipt.Artifact); err != nil {
		t.Fatal(err)
	}
	metadata := `{"deploymentId":"` + receipt.DeploymentID + `","environment":"staging","requestCI":null}`
	if _, err := pool.Exec(ctx, `INSERT INTO operations(id,kind,app,ref,status,risk,source,message,payload,metadata,max_attempts)
		VALUES($1,'release.qualification',$2,$3,'succeeded','staging release evidence','release-control-api','staging deployment qualified',$4::jsonb,$5::jsonb,1)`,
		receipt.ID, receipt.App, receipt.SourceSHA, string(raw), metadata); err != nil {
		t.Fatal(err)
	}
	material := &RecoveryKeyMaterial{QualificationPublicKeys: []ed25519.PublicKey{public}}
	if err := verifyWithin(t, ctx, pool, schema, material, ""); err != nil {
		t.Fatalf("producer qualification rejected after PostgreSQL round trip: %v", err)
	}
	other, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWithin(t, ctx, pool, schema, &RecoveryKeyMaterial{QualificationPublicKeys: []ed25519.PublicKey{other}}, ""); err == nil {
		t.Fatal("qualification verified with an unrelated key")
	}
	for name, tamper := range map[string]string{
		"outer app":           `UPDATE operations SET app='other' WHERE kind='release.qualification'`,
		"outer source sha":    `UPDATE operations SET ref=repeat('d',40) WHERE kind='release.qualification'`,
		"outer status":        `UPDATE operations SET status='failed' WHERE kind='release.qualification'`,
		"outer source":        `UPDATE operations SET source='pipeline' WHERE kind='release.qualification'`,
		"outer id":            `UPDATE operations SET id='33333333-3333-4333-8333-333333333333' WHERE kind='release.qualification'`,
		"metadata deployment": `UPDATE operations SET metadata=jsonb_set(metadata,'{deploymentId}','"other"') WHERE kind='release.qualification'`,
		"metadata env":        `UPDATE operations SET metadata=jsonb_set(metadata,'{environment}','"production"') WHERE kind='release.qualification'`,
		"receipt app":         `UPDATE operations SET payload=jsonb_set(payload,'{app}','"other"') WHERE kind='release.qualification'`,
		"receipt source sha":  `UPDATE operations SET payload=jsonb_set(payload,'{sourceSha}',to_jsonb(repeat('d',40))) WHERE kind='release.qualification'`,
		"receipt candidate":   `UPDATE operations SET payload=jsonb_set(payload,'{candidate,runId}','"304"') WHERE kind='release.qualification'`,
		"receipt extra field": `UPDATE operations SET payload=payload||'{"approvedBy":"attacker"}' WHERE kind='release.qualification'`,
		"deployment sha":      `UPDATE deployments SET commit_sha=repeat('d',40)`,
		"deployment artifact": `UPDATE deployments SET image_tag='registry.example.test/demo:latest'`,
		"deployment app":      `UPDATE deployments SET app='other'`,
		"deployment missing":  `DELETE FROM deployments`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifyWithin(t, ctx, pool, schema, material, tamper); err == nil {
				t.Fatal("tampered qualification evidence verified")
			}
		})
	}

	// A receipt with an authentic signature but a structurally incomplete
	// candidate (no signer workflow) is rejected, as the handler rejects it.
	seed := bytes.Repeat([]byte{'k'}, ed25519.SeedSize)
	private := ed25519.NewKeyFromSeed(seed)
	legacyPublic := private.Public().(ed25519.PublicKey)
	incomplete := receipt
	incomplete.ID = "44444444-4444-4444-8444-444444444444"
	incomplete.Candidate.SignerWorkflowRef, incomplete.Candidate.SignerWorkflowSHA = "", ""
	payload := []byte(releaseQualificationCanonical(incomplete))
	incomplete.KeyID = manifestKeyID(legacyPublic)
	incomplete.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, dssePAE(releaseQualificationPayloadType, payload)))
	incomplete.DSSE = model.DSSEEnvelope{PayloadType: releaseQualificationPayloadType, Payload: base64.RawStdEncoding.EncodeToString(payload), Signatures: []model.DSSESignature{{KeyID: incomplete.KeyID, Sig: incomplete.Signature}}}
	encoded, err := json.Marshal(incomplete)
	if err != nil {
		t.Fatal(err)
	}
	both := &RecoveryKeyMaterial{QualificationPublicKeys: []ed25519.PublicKey{public, legacyPublic}}
	if err := verifyWithin(t, ctx, pool, schema, both, `INSERT INTO operations(id,kind,app,ref,status,source,payload,metadata,max_attempts) VALUES($1,'release.qualification',$2,$3,'succeeded','release-control-api',$4::jsonb,$5::jsonb,1)`,
		incomplete.ID, incomplete.App, incomplete.SourceSHA, string(encoded), metadata); err == nil {
		t.Fatal("signed but structurally incomplete qualification verified")
	}
}

func TestAcceptanceEvidenceVerifierCoversAdmissionReconciliationAndRunnerVariants(t *testing.T) {
	pool, schema := inspectionTestDatabase(t)
	ctx := context.Background()
	hmacKey := "recovery-variant-hmac-key-0123456789abcdef"
	db := &store.DB{Pool: pool}
	signer, err := store.NewHMACAcceptanceSigner(hmacKey)
	if err != nil {
		t.Fatal(err)
	}
	operationStore, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := operationStore.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	accept := func(acceptance store.OperationAcceptance) store.AcceptedOperation {
		t.Helper()
		acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := operationStore.Accept(ctx, acceptance)
		if err != nil {
			t.Fatal(err)
		}
		return accepted
	}
	now := time.Now().UTC().Truncate(time.Microsecond)

	// One-active-mutable admission policy, with an integer above 2^53.
	admission := accept(store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: "app.restart", Resource: "admission-app", Key: "admission-key"},
		Operation: model.Operation{ID: uuid.NewString(), Kind: "app.restart", App: "admission-app", Status: model.OperationQueued, Source: "control-api", StartedAt: now, Payload: map[string]interface{}{"app": "admission-app", "generation": json.Number("9007199254740993")}, Metadata: map[string]interface{}{"reason": "variant"}},
		Audit:     store.AcceptanceAuditContext{Source: "variant-test", Scopes: []string{"api:write"}},
		Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
	})

	// Fleet plan, protected dispatch, runner attempt 1 and a recovery attempt 2.
	planID := uuid.NewString()
	finished := now
	plan := model.Operation{ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Source: "control-api", StartedAt: now, FinishedAt: &finished, MaxAttempts: 1,
		Payload: map[string]interface{}{"id": planID, "pool": "app", "action": "replace", "digest": "sha256:" + strings.Repeat("a", 64), "current": map[string]interface{}{"desired": 2}, "proposed": map[string]interface{}{"desired": 2}}, Metadata: map[string]interface{}{}}
	if err := db.InsertCompletedOperation(ctx, &plan); err != nil {
		t.Fatal(err)
	}
	rawNonce := strings.Repeat("f", 64)
	nonceDigest := sha256.Sum256([]byte(rawNonce))
	nonceHash := hex.EncodeToString(nonceDigest[:])
	if _, err := db.CreateFleetGitHubDispatch(ctx, store.FleetGitHubDispatch{PlanID: planID, PlanRunID: 6, PlanSHA256: strings.Repeat("b", 64), ApprovedHeadSHA: strings.Repeat("c", 40), FleetEnvironment: "staging/nyc3", DispatchNonce: rawNonce, DispatchNonceSHA256: nonceHash}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishFleetGitHubDispatch(ctx, planID, nonceHash, 7, "https://github.com/acme/fleet/actions/runs/7"); err != nil {
		t.Fatal(err)
	}
	runner := func(key, runID, intent string, resume bool, predecessor string) store.AcceptedOperation {
		attemptID := uuid.NewString()
		runnerID := "github:acme/fleet:" + runID + ":1"
		workflowURL := "https://github.com/acme/fleet/actions/runs/" + runID
		admission := &store.FleetRunnerAttemptAdmission{PlanID: planID, AttemptID: attemptID, ExpectedPredecessorID: predecessor, RunnerAttemptID: runnerID, CommitSHA: strings.Repeat("c", 40), PlanSHA256: strings.Repeat("b", 64),
			WorkflowURL: workflowURL, DispatchNonceSHA256: nonceHash, SourceDispatchRunID: "7", Resume: resume, HeartbeatTimeoutSeconds: 300, WorkloadIntent: intent, WorkloadRunID: runID, WorkloadSHA: strings.Repeat("c", 40)}
		return accept(store.OperationAcceptance{
			Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "1:2:" + runID + ":1"}, Kind: "fleet.runner-attempt", Resource: planID, Key: key},
			Operation: model.Operation{ID: uuid.NewString(), Kind: "fleet.runner-attempt", Ref: planID, Status: model.OperationSucceeded, Source: "fleet-runner", Risk: "bounded protected runner lease", StartedAt: now, FinishedAt: &finished, MaxAttempts: 1,
				Payload:  map[string]interface{}{"attemptId": attemptID, "planId": planID, "runnerAttemptId": runnerID, "commitSha": admission.CommitSHA, "planSha256": admission.PlanSHA256, "workflowUrl": workflowURL, "dispatchNonceSha256": nonceHash, "sourceDispatchRunId": "7", "resume": resume, "heartbeatTimeoutSeconds": 300},
				Metadata: map[string]interface{}{}},
			Audit:              store.AcceptanceAuditContext{Source: "github-actions"},
			Semantics:          map[string]interface{}{"action": "fleet.runner-attempt", "planId": planID, "workload": map[string]interface{}{"intent": intent, "runId": runID}},
			FleetRunnerAttempt: admission,
		})
	}
	first := runner("runner-first", "7", "apply", false, "")
	second := runner("runner-recovery", "8", "recover", true, first.FleetRunnerAttempt.ID)
	if second.FleetRunnerAttempt.Attempt != 2 || second.FleetRunnerAttempt.RetryOf != first.FleetRunnerAttempt.ID {
		t.Fatalf("recovery lineage = %#v", second.FleetRunnerAttempt)
	}

	// Reconciliation evidence bound to the active recovery attempt.
	attempt := second.FleetRunnerAttempt
	request := fleet.ReconciliationRequest{SchemaVersion: fleet.ReconciliationSchemaVersion, Phase: attempt.CurrentPhase, Status: "succeeded", CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256, EvidenceDigest: "sha256:" + strings.Repeat("d", 64), AttemptID: attempt.ID}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var reconciliationPayload map[string]interface{}
	if err := json.Unmarshal(requestJSON, &reconciliationPayload); err != nil {
		t.Fatal(err)
	}
	reconciliation := accept(store.OperationAcceptance{
		Identity:            store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "1:2:8:1"}, Kind: "fleet.reconciliation", Resource: planID, Key: "reconcile"},
		Operation:           model.Operation{ID: uuid.NewString(), Kind: "fleet.reconciliation", Ref: planID, Status: model.OperationSucceeded, Source: "fleet-runner", Risk: "append-only infrastructure reconciliation evidence", StartedAt: now, FinishedAt: &finished, MaxAttempts: 1, Payload: reconciliationPayload, Metadata: map[string]interface{}{}},
		Audit:               store.AcceptanceAuditContext{Source: "github-actions"},
		Semantics:           map[string]interface{}{"action": "fleet.reconciliation", "planId": planID, "request": request},
		FleetReconciliation: &store.FleetReconciliationAdmission{PlanID: planID, AttemptID: attempt.ID, RequireActiveAttempt: true, RunnerAttemptID: attempt.RunnerAttemptID, WorkflowURL: attempt.WorkflowURL},
	})

	// Each variant must still replay through the live store with the shared verifier.
	for _, accepted := range []store.AcceptedOperation{admission, first, second, reconciliation} {
		var identity store.OperationRequestIdentity
		if err := pool.QueryRow(ctx, `SELECT authority::text,actor_issuer,actor_subject,kind,resource,request_key FROM operation_request_identities WHERE id=$1`, accepted.RequestIdentityID).
			Scan(&identity.Authority, &identity.Actor.Issuer, &identity.Actor.Subject, &identity.Kind, &identity.Resource, &identity.Key); err != nil {
			t.Fatal(err)
		}
		if _, err := operationStore.Resolve(ctx, identity, accepted.Intent.Fingerprint); err != nil {
			t.Fatalf("store replay of %s failed: %v", identity.Kind, err)
		}
	}

	material := &RecoveryKeyMaterial{HMACKeys: [][]byte{[]byte(hmacKey)}}
	if err := verifyWithin(t, ctx, pool, schema, material, ""); err != nil {
		t.Fatalf("restored evidence rejected untampered variants: %v", err)
	}
	if err := verifyWithin(t, ctx, pool, schema, &RecoveryKeyMaterial{HMACKeys: [][]byte{[]byte(strings.Repeat("x", 32))}}, ""); err == nil {
		t.Fatal("acceptance evidence verified without its retained key")
	}
	for name, tamper := range map[string]struct {
		statement string
		args      []any
	}{
		"admission bigint":          {`UPDATE operations SET payload=jsonb_set(payload,'{generation}','9007199254740992'::jsonb) WHERE id=$1`, []any{admission.Operation.ID}},
		"admission app":             {`UPDATE operations SET app='other-app' WHERE id=$1`, []any{admission.Operation.ID}},
		"admission metadata":        {`UPDATE operations SET metadata='{}'::jsonb WHERE id=$1`, []any{admission.Operation.ID}},
		"acceptance not required":   {`UPDATE operations SET acceptance_required=false WHERE id=$1`, []any{admission.Operation.ID}},
		"reconciliation phase":      {`UPDATE operations SET payload=jsonb_set(payload,'{phase}','"complete"') WHERE id=$1`, []any{reconciliation.Operation.ID}},
		"reconciliation attempt":    {`UPDATE operations SET payload=jsonb_set(payload,'{attemptId}',to_jsonb($2::text)) WHERE id=$1`, []any{reconciliation.Operation.ID, first.FleetRunnerAttempt.ID}},
		"runner workflow":           {`UPDATE fleet_runner_attempts SET workflow_url='https://example.invalid/run' WHERE id=$1`, []any{first.FleetRunnerAttempt.ID}},
		"runner heartbeat timeout":  {`UPDATE fleet_runner_attempts SET heartbeat_timeout_seconds=301 WHERE id=$1`, []any{second.FleetRunnerAttempt.ID}},
		"runner retry lineage":      {`UPDATE fleet_runner_attempts SET retry_of='' WHERE id=$1`, []any{second.FleetRunnerAttempt.ID}},
		"runner root lineage":       {`UPDATE fleet_runner_attempts SET root_attempt_id=$2 WHERE id=$1`, []any{second.FleetRunnerAttempt.ID, second.FleetRunnerAttempt.ID}},
		"runner predecessor number": {`UPDATE fleet_runner_attempts SET attempt=5 WHERE id=$1`, []any{first.FleetRunnerAttempt.ID}},
		"runner operation status":   {`UPDATE operations SET status='failed' WHERE id=$1`, []any{first.Operation.ID}},
		"identity key":              {`UPDATE operation_request_identities SET request_key='forged' WHERE id=$1`, []any{reconciliation.RequestIdentityID}},
		"identity operation":        {`UPDATE operation_request_identities SET operation_id=$2 WHERE id=$1`, []any{reconciliation.RequestIdentityID, planID}},
		"intent scopes":             {`UPDATE operation_acceptance_intents SET scopes='["admin"]'::jsonb WHERE id=$1`, []any{admission.AcceptanceIntentID}},
		"intent signature":          {`UPDATE operation_acceptance_intents SET signature=repeat('0',64) WHERE id=$1`, []any{first.AcceptanceIntentID}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifyWithin(t, ctx, pool, schema, material, tamper.statement, tamper.args...); err == nil {
				t.Fatal("tampered acceptance evidence verified")
			}
		})
	}
	var identityCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{schema, "operation_acceptance_intents"}.Sanitize()).Scan(&identityCount); err != nil || identityCount != 4 {
		t.Fatalf("variant intents = %d, %v", identityCount, err)
	}
}
