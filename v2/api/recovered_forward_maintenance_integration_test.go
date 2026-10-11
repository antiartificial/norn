package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/config"
	"norn/v2/api/startup"
	"norn/v2/api/store"
)

const recoveredForwardTestAuditKey = "recovered-forward-test-audit-key-000000000000"

func recoveredForwardTestDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

func recoveredForwardSignJSON(t *testing.T, schema string, fields map[string]any) []byte {
	t.Helper()
	unsigned := make(map[string]any, len(fields))
	for key, value := range fields {
		unsigned[key] = value
	}
	canonical, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(recoveredForwardTestAuditKey))
	_, _ = mac.Write([]byte(schema + "\x00"))
	_, _ = mac.Write(canonical)
	unsigned["signature"] = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
	data, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type recoveredForwardTestEvidence struct {
	path, digest, sourceSHA, candidateSHA, transitionID, beforeDigest string
	value                                                             recoveredForwardEvidence
}

func writeRecoveredForwardEvidence(t *testing.T, dir, databaseID string) recoveredForwardTestEvidence {
	t.Helper()
	const transitionID = "pilot-recovered-forward-001"
	const sourceSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const candidateSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	before := recoveredForwardTestDigest("baseline-state")
	fields := map[string]any{
		"schema": recoveredForwardEvidenceSchema, "transitionId": transitionID, "sourceSHA": sourceSHA,
		"candidateSHA": candidateSHA, "databaseIdentity": databaseID,
		"recoveryFenceSHA256":   recoveredForwardTestDigest("recovery-fence"),
		"adoptionReceiptSHA256": recoveredForwardTestDigest("adoption"),
		"incidentBindingSHA256": recoveredForwardTestDigest("incident"),
		"ledgerSHA256":          recoveredForwardTestDigest("ledger"),
		"reconciliationSHA256":  recoveredForwardTestDigest("reconciliation"),
		"backupProofSHA256":     recoveredForwardTestDigest("backup-proof"),
		"backupSHA256":          recoveredForwardTestDigest("backup"), "backupBytes": int64(8192),
		"shadowReceiptSHA256": recoveredForwardTestDigest("shadow"), "beforeDigest": before,
	}
	raw := recoveredForwardSignJSON(t, recoveredForwardEvidenceSchema, fields)
	path := filepath.Join(dir, "source-evidence.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	var parsed recoveredForwardEvidence
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	return recoveredForwardTestEvidence{path: path, digest: hex.EncodeToString(sum[:]), sourceSHA: sourceSHA, candidateSHA: candidateSHA, transitionID: transitionID, beforeDigest: before, value: parsed}
}

func recoveredForwardTestReceipt(t *testing.T, evidence recoveredForwardTestEvidence, candidateSHA string) []byte {
	t.Helper()
	fields := map[string]any{
		"schema": recoveredForwardReceiptSchema, "transitionId": evidence.transitionID,
		"candidateSHA": candidateSHA, "sourceEvidenceSHA256": evidence.digest,
		"beforeDigest": evidence.beforeDigest, "afterDigest": evidence.beforeDigest,
		"preservationMatched": true, "finalizedAt": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
	}
	return recoveredForwardSignJSON(t, recoveredForwardReceiptSchema, fields)
}

func TestRecoveredForwardSignedEvidenceRejectsMutationAndPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	evidence := writeRecoveredForwardEvidence(t, dir, "db-test")
	cfg := &config.Config{AuditSigningKey: recoveredForwardTestAuditKey}
	parsed, digest, err := validateRecoveredForwardEvidence(evidence.path, evidence.digest, evidence.sourceSHA, evidence.candidateSHA, "db-test", cfg)
	if err != nil || digest != evidence.digest || parsed.TransitionID != evidence.transitionID {
		t.Fatalf("valid signed evidence failed: digest=%s evidence=%+v err=%v", digest, parsed, err)
	}
	if _, _, err := validateRecoveredForwardEvidence(evidence.path, evidence.digest, evidence.sourceSHA, strings.Repeat("c", 40), "db-test", cfg); err == nil {
		t.Fatal("evidence for a different exact candidate was accepted")
	}
	if err := os.Chmod(evidence.path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateRecoveredForwardEvidence(evidence.path, evidence.digest, evidence.sourceSHA, evidence.candidateSHA, "db-test", cfg); err == nil {
		t.Fatal("group/world-readable source evidence was accepted")
	}
}

func TestRecoveredForwardJSONRejectsDuplicateAndCaseFoldedFields(t *testing.T) {
	fields := map[string]any{
		"schema": recoveredForwardEvidenceSchema, "transitionId": "fixture-001",
		"sourceSHA": strings.Repeat("a", 40), "candidateSHA": strings.Repeat("b", 40),
	}
	raw := recoveredForwardSignJSON(t, recoveredForwardEvidenceSchema, fields)
	duplicate := append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"schema":"norn.recovered-forward-evidence/v1"}`)...)
	var evidence recoveredForwardEvidence
	if err := decodeExactSignedJSON(duplicate, recoveredForwardEvidenceSchema, recoveredForwardTestAuditKey, &evidence); err == nil || !strings.Contains(err.Error(), "duplicate JSON object key") {
		t.Fatalf("duplicate signed field was not rejected precisely: %v", err)
	}
	caseFolded := bytes.Replace(raw, []byte(`"transitionId"`), []byte(`"TransitionId"`), 1)
	if err := decodeExactSignedJSON(caseFolded, recoveredForwardEvidenceSchema, recoveredForwardTestAuditKey, &evidence); err == nil || !strings.Contains(err.Error(), "field spelling") {
		t.Fatalf("case-folded signed field spelling was not rejected: %v", err)
	}
	var request recoveredForwardFinalizeRequest
	if err := decodeExactJSON([]byte(`{"transitionId":"one","transitionId":"two"}`), &request); err == nil || !strings.Contains(err.Error(), "duplicate JSON object key") {
		t.Fatalf("duplicate finalize field was not rejected: %v", err)
	}
	if err := decodeExactJSON([]byte(`{"TransitionId":"one"}`), &request); err == nil || !strings.Contains(err.Error(), "field spelling") {
		t.Fatalf("case-folded finalize field spelling was not rejected: %v", err)
	}
}

// This real PostgreSQL test exercises the durable owner/reason CAS across the
// maintenance HTTP handoff and normal-candidate activation verifier. It uses
// an isolated schema and never connects to a live control plane.
func TestRecoveredForwardMaintenanceFinalizeAndExactActivation(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NORN_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "norn_recovered_forward_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") }()
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &store.DB{Pool: pool}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	databaseID := "recovered-forward-test-database"
	receiptDir := t.TempDir()
	if err := os.Chmod(receiptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	evidence := writeRecoveredForwardEvidence(t, receiptDir, databaseID)
	receiptPath := filepath.Join(receiptDir, "preservation-receipt.json")
	cfg := &config.Config{BindAddr: "127.0.0.1", APIToken: strings.Repeat("t", 32), AuditSigningKey: recoveredForwardTestAuditKey, AppsDir: t.TempDir(), NomadAddr: "http://127.0.0.1:4646", ConsulAddr: "http://127.0.0.1:8500", WorkloadConnector: "nomad-consul"}
	getenv := func(name string) string {
		values := map[string]string{
			"NORN_RECOVERED_FORWARD_TRANSITION_ID":             evidence.transitionID,
			"NORN_RECOVERED_FORWARD_SOURCE_SHA":                evidence.sourceSHA,
			"NORN_RECOVERED_FORWARD_CANDIDATE_SHA":             evidence.candidateSHA,
			"NORN_RECOVERED_FORWARD_SOURCE_EVIDENCE_PATH":      evidence.path,
			"NORN_RECOVERED_FORWARD_SOURCE_EVIDENCE_SHA256":    evidence.digest,
			"NORN_RECOVERED_FORWARD_PRESERVATION_RECEIPT_PATH": receiptPath,
		}
		return values[name]
	}
	previousSourceSHA := SourceSHA
	SourceSHA = evidence.candidateSHA
	defer func() { SourceSHA = previousSourceSHA }()
	startupCfg := startup.Config{StartupMode: startup.ModeActive, SchemaMode: startup.SchemaModeCheck}
	h, err := newRecoveredForwardMaintenanceHandler(cfg, db, databaseID, store.SchemaStatus{CurrentMigrationVersion: 49}, startupCfg, getenv)
	if err != nil {
		t.Fatal(err)
	}
	maintenanceServer := httptest.NewServer(h)
	defer maintenanceServer.Close()
	legacyHandler, err := newLegacyBaselineMaintenanceHandler(cfg, db, databaseID, store.SchemaStatus{CurrentMigrationVersion: 49}, startupCfg, "legacy-parity-fixture")
	if err == nil {
		t.Fatal("legacy maintenance unexpectedly acquired the recovered-forward-owned fence")
	}
	_ = legacyHandler

	request := func(method, path string, body []byte, token string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, maintenanceServer.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := maintenanceServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	// Exercise only the maintenance endpoints against the database-backed
	// handler. The app and service inventory routes intentionally query real
	// Mini-local Nomad/Consul APIs; those are covered by the wrapper's stable
	// state contract tests and are not available in this isolated PG fixture.
	for _, path := range []string{recoveredForwardStatusPath, recoveredForwardPreservationPath} {
		response := request(http.MethodGet, path, nil, cfg.APIToken)
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatalf("authenticated GET %s = %d", path, response.StatusCode)
		}
		response.Body.Close()
	}
	unauthorized := request(http.MethodGet, recoveredForwardStatusPath, nil, "")
	if unauthorized.StatusCode != http.StatusUnauthorized {
		unauthorized.Body.Close()
		t.Fatalf("unauthenticated maintenance read = %d", unauthorized.StatusCode)
	}
	unauthorized.Body.Close()
	fence, active, err := db.RuntimeMutationFenceState(ctx)
	if err != nil || !active || fence.Owner != "recovered-forward:"+evidence.transitionID || fence.Reason != recoveredForwardHeldReasonPrefix+evidence.digest {
		t.Fatalf("maintenance did not durably acquire exact recovered-forward fence: fence=%+v active=%t err=%v", fence, active, err)
	}
	if _, err := validateRecoveredForwardActivation(cfg, db, databaseID, evidence.candidateSHA, fence, func(name string) string {
		if name == "NORN_RECOVERED_FORWARD_TRANSITION_ID" || name == "NORN_RECOVERED_FORWARD_ACTIVATE_TRANSITION_ID" {
			return evidence.transitionID
		}
		return getenv(name)
	}); err == nil {
		t.Fatal("candidate activated before a signed preservation receipt was finalized")
	}

	receipt := recoveredForwardTestReceipt(t, evidence, evidence.candidateSHA)
	finalizeBody, err := json.Marshal(map[string]any{"transitionId": evidence.transitionID, "owner": fence.Owner, "epoch": fence.Epoch, "preservationReceipt": json.RawMessage(receipt)})
	if err != nil {
		t.Fatal(err)
	}
	finalize := request(http.MethodPost, recoveredForwardFinalizePath, finalizeBody, cfg.APIToken)
	if finalize.StatusCode != http.StatusOK {
		finalize.Body.Close()
		t.Fatalf("signed finalize = %d", finalize.StatusCode)
	}
	finalize.Body.Close()
	receiptSHABytes := sha256.Sum256(receipt)
	receiptSHA := hex.EncodeToString(receiptSHABytes[:])
	fence, active, err = db.RuntimeMutationFenceState(ctx)
	if err != nil || !active || fence.Owner != "recovered-forward:"+evidence.transitionID || fence.Reason != recoveredForwardFinalReasonPrefix+evidence.digest+":"+receiptSHA {
		t.Fatalf("finalize did not persist exact source and receipt digest: fence=%+v active=%t err=%v", fence, active, err)
	}
	restartedHandler, err := newRecoveredForwardMaintenanceHandler(cfg, db, databaseID, store.SchemaStatus{CurrentMigrationVersion: 49}, startupCfg, getenv)
	if err != nil {
		t.Fatalf("maintenance restart could not recover the persisted receipt state: %v", err)
	}
	restartedResponse := httptest.NewRecorder()
	restartedRequest := httptest.NewRequest(http.MethodGet, recoveredForwardStatusPath, nil)
	restartedRequest.Header.Set("Authorization", "Bearer "+cfg.APIToken)
	restartedHandler.ServeHTTP(restartedResponse, restartedRequest)
	var restartedStatus struct {
		Finalized bool `json:"finalized"`
	}
	if err := json.Unmarshal(restartedResponse.Body.Bytes(), &restartedStatus); err != nil || restartedResponse.Code != http.StatusOK || !restartedStatus.Finalized {
		t.Fatalf("restarted maintenance did not report durable finalization: status=%d body=%s err=%v", restartedResponse.Code, restartedResponse.Body.String(), err)
	}
	activationValues := map[string]string{
		"NORN_RECOVERED_FORWARD_TRANSITION_ID":               evidence.transitionID,
		"NORN_RECOVERED_FORWARD_ACTIVATE_TRANSITION_ID":      evidence.transitionID,
		"NORN_RECOVERED_FORWARD_SOURCE_SHA":                  evidence.sourceSHA,
		"NORN_RECOVERED_FORWARD_CANDIDATE_SHA":               evidence.candidateSHA,
		"NORN_RECOVERED_FORWARD_SOURCE_EVIDENCE_PATH":        evidence.path,
		"NORN_RECOVERED_FORWARD_SOURCE_EVIDENCE_SHA256":      evidence.digest,
		"NORN_RECOVERED_FORWARD_PRESERVATION_RECEIPT_PATH":   receiptPath,
		"NORN_RECOVERED_FORWARD_PRESERVATION_RECEIPT_SHA256": receiptSHA,
	}
	activation, err := validateRecoveredForwardActivation(cfg, db, databaseID, evidence.candidateSHA, fence, func(name string) string { return activationValues[name] })
	if err != nil || activation == nil || activation.fence.Epoch != fence.Epoch {
		t.Fatalf("exact candidate activation was rejected: activation=%+v err=%v", activation, err)
	}
	if err := db.ReleaseRuntimeMutationFence(ctx, activation.fence); err != nil {
		t.Fatal(err)
	}
	if active, err := db.RuntimeMutationFenceActive(ctx); err != nil || active {
		t.Fatalf("exact candidate did not release the fence after admission: active=%t err=%v", active, err)
	}

	// The payload explicitly describes a standalone Mini and the same two
	// stable_state inventory surfaces as the legacy protocol; Fleet configuration
	// and etcd are intentionally absent from the maintenance handler.
	status := request(http.MethodGet, recoveredForwardStatusPath, nil, cfg.APIToken)
	statusCode := status.StatusCode
	status.Body.Close()
	if statusCode != http.StatusOK {
		t.Fatalf("read-only maintenance status after activation = %d", statusCode)
	}
}
