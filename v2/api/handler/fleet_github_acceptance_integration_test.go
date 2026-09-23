package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/store"
)

// TestFleetGitHubReceiptUsesSignedAtomicAcceptance covers the receipt written
// after GitHub has deterministically created or recovered a protected action.
// It uses PostgreSQL so replay, conflict, and the stored signature are tested
// at the transaction boundary rather than only through a recording store.
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
	payload := map[string]interface{}{"planId": planID, "pullRequestNumber": 42, "url": "https://github.example.test/acme/fleet/pull/42", "branch": "norn/fleet-plan", "state": "open"}

	first, err := h.recordFleetGitHubOperation(req, principal, planID, "fleet.github.pull-request", "fleet pull request opened", payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.recordFleetGitHubOperation(req, principal, planID, "fleet.github.pull-request", "fleet pull request opened", payload)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || second.ID != first.ID {
		t.Fatalf("replay ids first=%q second=%q", first.ID, second.ID)
	}

	conflictPayload := map[string]interface{}{"planId": planID, "pullRequestNumber": 43, "url": "https://github.example.test/acme/fleet/pull/43", "branch": "norn/fleet-plan", "state": "open"}
	if _, err := h.recordFleetGitHubOperation(req, principal, planID, "fleet.github.pull-request", "fleet pull request opened", conflictPayload); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("conflicting recovered receipt error=%v, want ErrAcceptanceConflict", err)
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
