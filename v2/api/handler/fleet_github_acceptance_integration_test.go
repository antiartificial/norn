package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// TestFleetGitHubReceiptUsesSignedAtomicAcceptance reserves the immutable
// protected-plan intent before GitHub is called, then permits exactly one
// recovered result to complete it. PostgreSQL exercises the admission and
// completion boundaries rather than only an in-memory recording store.
func TestFleetGitHubReceiptUsesSignedAtomicAcceptance(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	const auditKey = "fleet-github-acceptance-signing-key-0001"
	h := New(db, nil, nil, nil, &config.Config{AuditSigningKey: auditKey}, nil, nil, nil, nil, nil, nil)
	planID := "7d4b716d-788a-4e43-8f0b-5d4b8f3a2a4c"
	if err := db.ReserveMutationAudit(context.Background(), &store.MutationAuditEvent{
		ID: "receipt-fleet-github", RequestID: "request-fleet-github", PrincipalSubject: "operator-1",
		Method: http.MethodPost, Path: "/api/v1/fleet/plans/{planID}/github/pull-request", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/github/pull-request", nil)
	req = withOperationAcceptanceRequestContext(req, operationAcceptanceRequestContext{
		ReceiptID: "receipt-fleet-github", RequestID: "request-fleet-github",
		Actor: verifiedOperationActor{Issuer: "https://access.example.test", Subject: "operator-1", CredentialID: "token-1", DeviceID: "device-1", Source: string(AccessPrincipalSourceManagedToken), Scopes: []string{ScopeAPIWrite}},
	})
	principal := AccessPrincipal{Subject: "operator-1", TokenID: "token-1", DeviceID: "device-1", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}}
	payload := map[string]interface{}{"planId": planID, "planDigest": "sha256:plan", "sourceDigest": "sha256:source", "pool": "workers", "action": "scale", "proposed": map[string]interface{}{"desired": 3}}

	first, err := h.reserveFleetGitHubOperation(req, principal, planID, "fleet.github.pull-request", payload)
	if err != nil {
		t.Fatal(err)
	}
	if queued, found, err := h.resolveFleetGitHubOperation(req, planID, "fleet.github.pull-request"); err != nil || !found || queued.ID != first.ID || queued.Status != model.OperationQueued {
		t.Fatalf("queued reconciliation lookup=%+v found=%v err=%v", queued, found, err)
	}
	second, err := h.reserveFleetGitHubOperation(req, principal, planID, "fleet.github.pull-request", payload)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || second.ID != first.ID {
		t.Fatalf("replay ids first=%q second=%q", first.ID, second.ID)
	}

	if first.Status != "queued" || second.Status != "queued" {
		t.Fatalf("pre-external receipt statuses first=%q second=%q, want queued", first.Status, second.Status)
	}
	conflictPayload := map[string]interface{}{"planId": planID, "planDigest": "sha256:other", "sourceDigest": "sha256:source", "pool": "workers", "action": "scale", "proposed": map[string]interface{}{"desired": 3}}
	if _, err := h.reserveFleetGitHubOperation(req, principal, planID, "fleet.github.pull-request", conflictPayload); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("conflicting reservation error=%v, want ErrAcceptanceConflict", err)
	}
	// A temporary GitHub readiness result makes no external mutation. The same
	// plan must retain its queued reservation and later complete, rather than
	// being terminalized under the plan-scoped idempotency identity.
	resumed, err := h.reserveFleetGitHubOperation(req, principal, planID, "fleet.github.pull-request", payload)
	if err != nil || resumed.ID != first.ID || resumed.Status != model.OperationQueued {
		t.Fatalf("not-ready retry reservation=%+v err=%v", resumed, err)
	}
	result := map[string]interface{}{"planId": planID, "pullRequestNumber": 42, "url": "https://github.example.test/acme/fleet/pull/42", "branch": "norn/fleet-plan", "state": "open"}
	// Two recovering API requests can reach completion concurrently after an
	// interrupted GitHub call. Exactly one result becomes durable; the other
	// must fail rather than rewrite the accepted action.
	competing := map[string]interface{}{"planId": planID, "pullRequestNumber": 43}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, candidate := range []map[string]interface{}{result, competing} {
		wg.Add(1)
		go func(candidate map[string]interface{}) {
			defer wg.Done()
			<-start
			_, err := h.finishFleetGitHubReservation(context.Background(), first.ID, planID, "fleet.github.pull-request", model.OperationSucceeded, "fleet pull request opened", candidate)
			errs <- err
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded, rejected := 0, 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else {
			rejected++
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("competing completion outcomes succeeded=%d rejected=%d", succeeded, rejected)
	}
	var completion map[string]interface{}
	if err := db.Pool.QueryRow(context.Background(), `SELECT metadata->'fleetGitHubCompletion' FROM operations WHERE id=$1`, first.ID).Scan(&completion); err != nil {
		t.Fatal(err)
	}
	canonicalText, _ := completion["canonicalBytes"].(string)
	completionCanonical, err := base64.StdEncoding.DecodeString(canonicalText)
	if err != nil {
		t.Fatal(err)
	}
	completionSigner, err := store.NewHMACAcceptanceSigner(auditKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := completionSigner.Verify(context.Background(), store.AcceptanceSignature{Algorithm: completion["signingAlgorithm"].(string), KeyID: completion["signingKeyId"].(string), Value: completion["signature"].(string)}, completionCanonical); err != nil {
		t.Fatalf("stored Fleet GitHub completion signature did not verify: %v", err)
	}
	// The queued signed intent and terminal operation must still be a valid
	// archive bundle after completion. This protects the recovery path from a
	// pre-dispatch reservation that can never be sealed.
	if _, err := db.ProcessPendingEvidenceIntent(context.Background(), 0, func(_ context.Context, intent store.EvidenceIntent, source store.EvidenceSource) (store.EvidencePublication, error) {
		if source.Acceptance == nil || source.OperationKind != "fleet.github.pull-request" {
			t.Fatalf("archive source missing accepted Fleet GitHub reservation: %+v", source)
		}
		if err := store.VerifyArchivedAcceptance(store.ArchivedAcceptance{
			IntentID: source.Acceptance.IntentID, RequestIdentityID: source.Acceptance.RequestIdentityID, RequestReceiptID: source.Acceptance.RequestReceiptID,
			FingerprintVersion: source.Acceptance.FingerprintVersion, FingerprintDigest: source.Acceptance.FingerprintDigest,
			RequestCanonicalBytes: source.Acceptance.RequestCanonicalBytes, CanonicalBytes: source.Acceptance.CanonicalBytes,
			CanonicalDigest: source.Acceptance.CanonicalDigest, SigningAlgorithm: source.Acceptance.SigningAlgorithm, SigningKeyID: source.Acceptance.SigningKeyID,
			OperationRow: source.OperationJSON, OperationID: intent.OperationID,
		}); err != nil {
			return store.EvidencePublication{}, err
		}
		return store.EvidencePublication{ObjectKey: "test/" + intent.ID, ObjectSHA256: "test-sha", ObjectBytes: 1}, nil
	}); err != nil {
		t.Fatalf("completed reservation was not archivable: %v", err)
	}

	var operations, intents int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE kind='fleet.github.pull-request' AND ref=$1`, planID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_acceptance_intents WHERE operation_id=$1`, first.ID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || intents != 1 {
		t.Fatalf("durable receipt counts operations=%d intents=%d, want 1/1", operations, intents)
	}
	var subjectKind, subjectID, archiveOperationID, archiveState string
	if err := db.Pool.QueryRow(context.Background(), `SELECT subject_kind,subject_id,operation_id,state FROM evidence_archive_intents WHERE operation_id=$1`, first.ID).Scan(&subjectKind, &subjectID, &archiveOperationID, &archiveState); err != nil {
		t.Fatalf("non-saga receipt did not reserve archive work: %v", err)
	}
	if subjectKind != "operation" || subjectID != first.ID || archiveOperationID != first.ID || archiveState != "pending" {
		t.Fatalf("non-saga archive reservation = %q/%q/%q/%q", subjectKind, subjectID, archiveOperationID, archiveState)
	}

	var canonical []byte
	var algorithm, keyID, signature string
	if err := db.Pool.QueryRow(context.Background(), `SELECT canonical_bytes,signing_algorithm,signing_key_id,signature FROM operation_acceptance_intents WHERE operation_id=$1`, first.ID).Scan(&canonical, &algorithm, &keyID, &signature); err != nil {
		t.Fatal(err)
	}
	signer, err := store.NewHMACAcceptanceSigner(auditKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Verify(context.Background(), store.AcceptanceSignature{Algorithm: algorithm, KeyID: keyID, Value: signature}, canonical); err != nil {
		t.Fatalf("stored receipt signature did not verify: %v", err)
	}
	if !strings.Contains(string(canonical), "fleet.github.pull-request") || !strings.Contains(string(canonical), planID) {
		t.Fatalf("signed receipt does not bind fleet GitHub operation: %s", canonical)
	}
}

func TestFleetGitHubVerifiedNoWriteCompletionIsIdempotent(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	h := New(db, nil, nil, nil, &config.Config{AuditSigningKey: "fleet-github-no-write-key-0001"}, nil, nil, nil, nil, nil, nil)
	const planID = "8d4b716d-788a-4e43-8f0b-5d4b8f3a2a4c"
	if err := db.ReserveMutationAudit(context.Background(), &store.MutationAuditEvent{ID: "receipt-no-write", RequestID: "request-no-write", PrincipalSubject: "operator-1", Method: http.MethodPost, Path: "/api/v1/fleet/plans/{planID}/github/reconcile", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/github/reconcile", nil)
	req = withOperationAcceptanceRequestContext(req, operationAcceptanceRequestContext{ReceiptID: "receipt-no-write", RequestID: "request-no-write", Actor: verifiedOperationActor{Issuer: "https://access.example.test", Subject: "operator-1", CredentialID: "token-1", DeviceID: "device-1", Source: string(AccessPrincipalSourceManagedToken), Scopes: []string{ScopeAPIWrite}}})
	reserved, err := h.reserveFleetGitHubOperation(req, AccessPrincipal{Subject: "operator-1"}, planID, "fleet.github.pull-request", map[string]interface{}{"planId": planID})
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]interface{}{"planId": planID, "outcome": "verified-no-write", "verifiedAt": "2026-09-24T12:00:00Z"}
	first, err := h.finishFleetGitHubReservation(context.Background(), reserved.ID, planID, "fleet.github.pull-request", model.OperationCanceled, "verified no write", result)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.finishFleetGitHubReservation(context.Background(), reserved.ID, planID, "fleet.github.pull-request", model.OperationCanceled, "verified no write", result)
	if err != nil || first.ID != second.ID || second.Status != model.OperationCanceled {
		t.Fatalf("idempotent completion first=%+v second=%+v err=%v", first, second, err)
	}
	resolved, found, err := h.resolveFleetGitHubOperation(req, planID, "fleet.github.pull-request")
	if err != nil || !found || resolved.Status != model.OperationCanceled {
		t.Fatalf("terminal reconciliation lookup=%+v found=%v err=%v", resolved, found, err)
	}
}

// TestFleetGitHubReservationRefusesArchiveExhaustion verifies the refusal is
// made while the action is still an admitted intent. A caller therefore has no
// GitHub result to recover when archive capacity cannot accept the receipt.
func TestFleetGitHubReservationRefusesArchiveExhaustion(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	const auditKey = "fleet-github-reserve-exhaustion-key-0001"
	h := New(db, nil, nil, nil, &config.Config{AuditSigningKey: auditKey}, nil, nil, nil, nil, nil, nil)
	if err := db.SetEvidenceReservePolicy(context.Background(), store.EvidenceReservePolicy{Enabled: true, MaxPending: 1, MaxPendingAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	reserve := func(planID, receiptID string) error {
		if err := db.ReserveMutationAudit(context.Background(), &store.MutationAuditEvent{
			ID: receiptID, RequestID: receiptID + "-request", PrincipalSubject: "operator-1",
			Method: http.MethodPost, Path: "/api/v1/fleet/plans/{planID}/github/pull-request", StartedAt: time.Now().UTC(),
		}); err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/github/pull-request", nil)
		req = withOperationAcceptanceRequestContext(req, operationAcceptanceRequestContext{
			ReceiptID: receiptID, RequestID: receiptID + "-request",
			Actor: verifiedOperationActor{Issuer: "https://access.example.test", Subject: "operator-1", CredentialID: "token-1", DeviceID: "device-1", Source: string(AccessPrincipalSourceManagedToken), Scopes: []string{ScopeAPIWrite}},
		})
		_, err := h.reserveFleetGitHubOperation(req, AccessPrincipal{Subject: "operator-1"}, planID, "fleet.github.pull-request", map[string]interface{}{
			"planId": planID, "planDigest": "sha256:plan", "sourceDigest": "sha256:source", "pool": "workers", "action": "scale",
		})
		return err
	}
	firstPlan := "1d4b716d-788a-4e43-8f0b-5d4b8f3a2a4c"
	if err := reserve(firstPlan, "reserve-first"); err != nil {
		t.Fatal(err)
	}
	secondPlan := "2d4b716d-788a-4e43-8f0b-5d4b8f3a2a4c"
	if err := reserve(secondPlan, "reserve-second"); err == nil {
		t.Fatal("second pre-dispatch reservation succeeded after reserve was exhausted")
	} else {
		var exhausted *store.EvidenceReserveExhaustedError
		if !errors.As(err, &exhausted) {
			t.Fatalf("second pre-dispatch reservation error=%v, want reserve exhaustion", err)
		}
	}
	var secondOperations int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE ref=$1`, secondPlan).Scan(&secondOperations); err != nil {
		t.Fatal(err)
	}
	if secondOperations != 0 {
		t.Fatalf("archive-exhausted reservation persisted %d operations", secondOperations)
	}
}
