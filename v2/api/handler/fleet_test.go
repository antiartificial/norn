package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

func TestValidateFleetDocumentReturnsTypedInvalidReport(t *testing.T) {
	h := New(nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/validate", strings.NewReader(`{"document":"apiVersion: wrong\nkind: Cluster\ncluster: {}\nnodePools: {}\n"}`))
	rec := httptest.NewRecorder()
	h.ValidateFleetDocument(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var report fleet.ValidationReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Valid || report.SchemaVersion != "norn.validation-report/v1" {
		t.Fatalf("report=%#v", report)
	}
}

func TestValidateInfraSpecDocumentRejectsUnknownFieldsAsFinding(t *testing.T) {
	h := New(nil, nil, nil, nil, &config.Config{NetworkMode: "local"}, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/validate/infraspec", strings.NewReader(`{"document":"name: toy\ndeploy: false\nprocesses: {web: {command: run}}\ntyop: true\n"}`))
	rec := httptest.NewRecorder()
	h.ValidateInfraSpecDocument(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "infraspec.document.decode-failed") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var report model.ValidationResult
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != "norn.validation-report/v1" || report.DocumentKind != "infraspec" {
		t.Fatalf("report lacks typed versioning: %#v", report)
	}
}

func TestDocumentValidatorsHonorAdvertisedDocumentLimit(t *testing.T) {
	h := New(nil, nil, nil, nil, &config.Config{NetworkMode: "local"}, nil, nil, nil, nil, nil, nil)
	// The OpenAPI contract permits 65,536 document characters. JSON framing
	// makes that request larger than the ordinary 64 KiB control payload cap.
	document := "#" + strings.Repeat("x", 65535)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/validate", strings.NewReader(`{"document":`+strconv.Quote(document)+`}`))
	rec := httptest.NewRecorder()
	h.ValidateFleetDocument(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("large document status=%d body=%s", rec.Code, rec.Body.String())
	}

	tooLarge := document + "x"
	req = httptest.NewRequest(http.MethodPost, "/api/v1/fleet/validate", strings.NewReader(`{"document":`+strconv.Quote(tooLarge)+`}`))
	rec = httptest.NewRecorder()
	h.ValidateFleetDocument(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "validation_document_too_large") {
		t.Fatalf("oversized document status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBuildCapacityPlanRequiresBlueGreenForSizeChangeAndSigns(t *testing.T) {
	pool := fleet.NodePool{Size: "small", Min: 2, Desired: 2, Max: 8, Replacement: fleet.Replacement{Strategy: "blueGreen"}}
	inventory := &fleet.Inventory{Digest: "sha256:source", Document: &fleet.Document{Cluster: fleet.Cluster{Name: "production-nyc3"}, Metadata: fleet.Metadata{WorkflowURL: "https://example.test/apply"}}}
	if _, err := buildCapacityPlan(inventory, "app", pool, fleet.PlanRequest{Size: "large", Strategy: "rolling"}, "audit-key"); err == nil {
		t.Fatal("rolling size replacement accepted")
	}
	plan, err := buildCapacityPlan(inventory, "app", pool, fleet.PlanRequest{Size: "large"}, "audit-key")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "replace" || !strings.HasPrefix(plan.Digest, "sha256:") || !strings.HasPrefix(plan.Signature, "hmac-sha256:") {
		t.Fatalf("plan = %#v", plan)
	}
	canonical, err := canonicalCapacityPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if plan.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %q, want digest of canonical plan", plan.Digest)
	}
	mac := hmac.New(sha256.New, []byte("audit-key"))
	_, _ = mac.Write([]byte(plan.Digest))
	if plan.Signature != "hmac-sha256:"+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature does not authenticate plan digest: %q", plan.Signature)
	}
	plan.Proposed.Desired++
	modified, err := canonicalCapacityPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	modifiedSum := sha256.Sum256(modified)
	if plan.Digest == "sha256:"+hex.EncodeToString(modifiedSum[:]) {
		t.Fatal("plan digest did not detect a proposed-capacity change")
	}
}

func TestFleetGitHubRequiresAnAuthenticCapacityPlan(t *testing.T) {
	pool := fleet.NodePool{Size: "small", Min: 2, Desired: 2, Max: 8}
	inventory := &fleet.Inventory{Digest: "sha256:source", Document: &fleet.Document{Cluster: fleet.Cluster{Name: "production-nyc3"}}}
	plan, err := buildCapacityPlan(inventory, "app", pool, fleet.PlanRequest{Desired: intPointer(3)}, "audit-key")
	if err != nil {
		t.Fatal(err)
	}
	h := New(nil, nil, nil, nil, &config.Config{Profile: "production", AuditSigningKey: "audit-key"}, nil, nil, nil, nil, nil, nil)
	if !h.verifyCapacityPlan(plan) {
		t.Fatal("authentic signed plan was rejected")
	}
	plan.Proposed.Desired = 4
	if h.verifyCapacityPlan(plan) {
		t.Fatal("tampered plan was accepted")
	}
	plan, err = buildCapacityPlan(inventory, "app", pool, fleet.PlanRequest{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if h.verifyCapacityPlan(plan) {
		t.Fatal("unsigned plan was accepted in production")
	}
}

func intPointer(value int) *int { return &value }

func TestBuildCapacityPlanRejectsBlankSize(t *testing.T) {
	pool := fleet.NodePool{Size: "small", Min: 2, Desired: 2, Max: 8}
	inventory := &fleet.Inventory{Digest: "sha256:source", Document: &fleet.Document{Cluster: fleet.Cluster{Name: "production-nyc3"}}}
	if _, err := buildCapacityPlan(inventory, "app", pool, fleet.PlanRequest{Size: ""}, "audit-key"); err != nil {
		t.Fatalf("empty optional size should preserve current size: %v", err)
	}
	// HTTP request normalization rejects whitespace before it reaches this builder.
	if _, err := buildCapacityPlan(inventory, "app", pool, fleet.PlanRequest{Size: " "}, "audit-key"); err == nil {
		t.Fatal("blank VM size accepted")
	}
}

func TestUnsignedCapacityPlanDigestIncludesUnsignedWarning(t *testing.T) {
	pool := fleet.NodePool{Size: "small", Min: 2, Desired: 2, Max: 8}
	inventory := &fleet.Inventory{Digest: "sha256:source", Document: &fleet.Document{Cluster: fleet.Cluster{Name: "production-nyc3"}}}
	plan, err := buildCapacityPlan(inventory, "app", pool, fleet.PlanRequest{}, "")
	if err != nil {
		t.Fatal(err)
	}
	unsignedWarning := false
	for _, finding := range plan.Findings {
		if finding.Code == "fleet.plan.unsigned-development" {
			unsignedWarning = true
			break
		}
	}
	if !unsignedWarning {
		t.Fatalf("unsigned plan warning missing: %#v", plan.Findings)
	}
	canonical, err := canonicalCapacityPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if plan.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("unsigned warning was not included in digest: %q", plan.Digest)
	}
}

func TestFleetPlanIdempotencyIsPrincipalScopedAndRequestBound(t *testing.T) {
	request := fleet.PlanRequest{Reason: "capacity review"}
	firstKey, firstDigest := fleetPlanIdempotency(AccessPrincipal{Subject: "one"}, "app", "retry-1", request)
	secondKey, _ := fleetPlanIdempotency(AccessPrincipal{Subject: "two"}, "app", "retry-1", request)
	if firstKey == secondKey {
		t.Fatal("idempotency key was not principal scoped")
	}
	op := &model.Operation{Kind: "fleet.capacity-plan", Metadata: map[string]interface{}{"requestDigest": firstDigest}}
	if !matchesFleetPlanRequest(op, firstDigest) {
		t.Fatal("matching idempotent request rejected")
	}
	if matchesFleetPlanRequest(op, "sha256:other") {
		t.Fatal("different request accepted for idempotency replay")
	}
}

func TestFleetInventoryRequiresReadScopeAndRedactsConfigPath(t *testing.T) {
	unconfigured, err := New(nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil).loadFleetInventory()
	if err != nil || unconfigured.Configured || unconfigured.Source != "" {
		t.Fatalf("unconfigured inventory = %#v, err=%v", unconfigured, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "production-fleet.yaml")
	document := `apiVersion: norn.dev/fleet/v1
kind: Cluster
cluster: {name: production-nyc3, provider: digitalocean, region: nyc3}
nodePools:
  app:
    size: s-4vcpu-8gb
    min: 2
    desired: 2
    max: 8
    replacement: {strategy: blueGreen, requireCapacityHeadroom: true, requireReadiness: true}
`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	h := New(nil, nil, nil, nil, &config.Config{FleetConfig: path}, nil, nil, nil, nil, nil, nil)

	unauthorized := httptest.NewRecorder()
	h.FleetInventory(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/fleet/node-pools", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated inventory status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/node-pools", nil)
	req = WithAccessPrincipal(req, &AccessPrincipal{Scopes: []string{ScopeAPIRead}})
	rec := httptest.NewRecorder()
	h.FleetInventory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inventory status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), dir) || !strings.Contains(rec.Body.String(), `"source":"production-fleet.yaml"`) {
		t.Fatalf("inventory leaked configured path: %s", rec.Body.String())
	}
}

func TestFleetReconciliationRequestAndTransitionAreBoundAndOrdered(t *testing.T) {
	request := fleet.ReconciliationRequest{
		SchemaVersion: fleet.ReconciliationSchemaVersion,
		AttemptID:     uuid.NewString(),
		Phase:         "prechange_verified", Status: "succeeded",
		CommitSHA: strings.Repeat("a", 40), PlanSHA256: strings.Repeat("b", 64),
		StateSerial: 7, EvidenceDigest: "sha256:" + strings.Repeat("c", 64),
	}
	if err := validateFleetReconciliationRequest(request); err != nil {
		t.Fatal(err)
	}
	withoutAttempt := request
	withoutAttempt.AttemptID = ""
	if err := validateFleetReconciliationRequest(withoutAttempt); err == nil {
		t.Fatal("unbound reconciliation evidence was accepted")
	}
	plan := &model.Operation{Kind: "fleet.capacity-plan", Payload: map[string]interface{}{
		"action": "replace", "current": map[string]interface{}{"desired": 2}, "proposed": map[string]interface{}{"desired": 2},
	}}
	if err := validateFleetReconciliationTransition(plan, nil, request); err != nil {
		t.Fatal(err)
	}
	existing := []model.Operation{{Kind: "fleet.reconciliation", Status: model.OperationSucceeded, Payload: map[string]interface{}{
		"phase": request.Phase, "commitSha": request.CommitSHA, "planSha256": request.PlanSHA256,
	}}}
	next := request
	next.Phase = "provider_applying"
	next.StateSerial = 0
	if err := validateFleetReconciliationTransition(plan, existing, next); err != nil {
		t.Fatal(err)
	}
	next.Phase = "readiness_verified"
	if err := validateFleetReconciliationTransition(plan, existing, next); err == nil {
		t.Fatal("out-of-order readiness checkpoint accepted")
	}
	next = request
	next.CommitSHA = strings.Repeat("d", 40)
	if err := validateFleetReconciliationTransition(plan, existing, next); err == nil {
		t.Fatal("checkpoint binding change accepted")
	}
}

func TestFleetReconciliationCompleteRequiresDrainForReplacement(t *testing.T) {
	commit := strings.Repeat("a", 40)
	planSHA := strings.Repeat("b", 64)
	checkpoint := func(phase string) model.Operation {
		return model.Operation{Kind: "fleet.reconciliation", Status: model.OperationSucceeded, Payload: map[string]interface{}{
			"phase": phase, "commitSha": commit, "planSha256": planSHA,
		}}
	}
	existing := []model.Operation{
		checkpoint("prechange_verified"), checkpoint("provider_applying"), checkpoint("infrastructure_applied"),
		checkpoint("inventory_generated"), checkpoint("nodes_configured"), checkpoint("nodes_enrolled"), checkpoint("readiness_verified"),
	}
	request := fleet.ReconciliationRequest{Phase: "complete", Status: "succeeded", CommitSHA: commit, PlanSHA256: planSHA}
	replacePlan := &model.Operation{Payload: map[string]interface{}{"action": "replace"}}
	if err := validateFleetReconciliationTransition(replacePlan, existing, request); err == nil {
		t.Fatal("replacement completed before drain")
	}
	existing = append(existing, checkpoint("old_nodes_drained"))
	if err := validateFleetReconciliationTransition(replacePlan, existing, request); err != nil {
		t.Fatal(err)
	}
	scaleUpPlan := &model.Operation{Payload: map[string]interface{}{
		"action": "scale", "current": map[string]interface{}{"desired": 2}, "proposed": map[string]interface{}{"desired": 3},
	}}
	if err := validateFleetReconciliationTransition(scaleUpPlan, existing[2:7], request); err != nil {
		t.Fatalf("scale-up completion unexpectedly required drain: %v", err)
	}
}

func TestFleetRunnerReconciliationReadRequiresBoundAttemptAndFiltersRecords(t *testing.T) {
	owner := AccessPrincipal{Scopes: []string{ScopeFleetOperate}}
	if !fleetRunnerReconciliationReadNeedsAttemptID(owner) {
		t.Fatal("fleet:operate read was allowed without an attemptId")
	}
	if fleetRunnerReconciliationReadNeedsAttemptID(AccessPrincipal{Scopes: []string{ScopeAPIRead}}) {
		t.Fatal("api:read unexpectedly required an attemptId")
	}
	attemptID := uuid.NewString()
	otherID := uuid.NewString()
	operations := []model.Operation{
		{ID: "bound", Payload: map[string]interface{}{"attemptId": attemptID}},
		{ID: "other", Payload: map[string]interface{}{"attemptId": otherID}},
		{ID: "legacy", Payload: map[string]interface{}{"phase": "inventory_generated"}},
	}
	filtered := filterFleetReconciliationsForAttempt(operations, attemptID)
	if len(filtered) != 1 || filtered[0].ID != "bound" {
		t.Fatalf("runner reconciliation read leaked records: %#v", filtered)
	}
}

func TestFleetReconciliationIdempotencyIsPrincipalAndRequestBound(t *testing.T) {
	request := fleet.ReconciliationRequest{Phase: "inventory_generated", CommitSHA: strings.Repeat("a", 40)}
	planID := uuid.NewString()
	firstKey, digest := fleetReconciliationIdempotency(AccessPrincipal{Subject: "runner-one"}, planID, "retry", request)
	secondKey, _ := fleetReconciliationIdempotency(AccessPrincipal{Subject: "runner-two"}, planID, "retry", request)
	if firstKey == secondKey || digest == "" {
		t.Fatal("reconciliation idempotency was not bound")
	}
	op := &model.Operation{Kind: "fleet.reconciliation", Metadata: map[string]interface{}{"requestDigest": digest}}
	if !matchesFleetReconciliationRequest(op, digest) || matchesFleetReconciliationRequest(op, "sha256:other") {
		t.Fatal("reconciliation replay matching is incorrect")
	}
}
