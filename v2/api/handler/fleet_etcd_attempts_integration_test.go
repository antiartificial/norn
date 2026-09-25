package handler

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
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestEtcdFleetRunnerHTTPAdmissionAndEvidenceGate(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-conf/fleet-runner-http/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	const auditKey = "norn-fleet-runner-http-audit-signing-key"
	signer, err := store.NewHMACAcceptanceSigner(auditKey)
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	planID := uuid.NewString()
	plan := fleet.CapacityPlan{SchemaVersion: "norn.fleet-capacity-plan/v1", ID: planID, Cluster: "fleet", Pool: "workers", Current: fleet.NodePool{Desired: 2}, Proposed: fleet.NodePool{Desired: 3}, Action: "scale", SourceDigest: "sha256:" + strings.Repeat("1", 64)}
	canonical, err := canonicalCapacityPlan(&plan)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	plan.Digest = "sha256:" + hex.EncodeToString(sum[:])
	mac := hmac.New(sha256.New, []byte(auditKey))
	_, _ = mac.Write([]byte(plan.Digest))
	plan.Signature = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
	encodedPlan, _ := json.Marshal(plan)
	var payload map[string]interface{}
	_ = json.Unmarshal(encodedPlan, &payload)
	now := time.Now().UTC()
	finished := now
	planOperation := model.Operation{ID: planID, Kind: "fleet.capacity-plan", Ref: "workers", Status: model.OperationSucceeded, Source: "test", Risk: "plan", Payload: payload, Metadata: map[string]interface{}{}, StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1}
	acceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: planOperation.Kind, Resource: planOperation.Ref, Key: "plan-" + planID}, Operation: planOperation, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.capacity-plan"}}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(context.Background(), acceptance); err != nil {
		t.Fatal(err)
	}
	const rawNonce = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	nonceSum := sha256.Sum256([]byte(rawNonce))
	commitSHA := strings.Repeat("c", 40)
	planSHA := strings.Repeat("b", 64)
	if err := operations.BindFleetRunnerDispatch(context.Background(), etcdstore.FleetRunnerDispatchBinding{PlanID: planID, PlanSHA256: planSHA, ApprovedHeadSHA: commitSHA, DispatchNonceSHA256: hex.EncodeToString(nonceSum[:]), RunID: 7, WorkflowURL: "https://github.com/acme/fleet/actions/runs/7"}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AuditSigningKey: auditKey, ControlAuthority: authority}
	h := NewEtcdFleetRunnerHandler(cfg, operations)
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/plans/{planID}/attempts", h.Create)
	router.Post("/api/v1/fleet/plans/{planID}/reconciliations", h.Reconcile)
	router.Post("/api/v1/fleet/plans/{planID}/attempts/{attemptID}/advance", h.Advance)
	ci := &CIIdentity{Provider: "github-actions", Repository: "acme/fleet", RunID: "7", RunAttempt: "1", SHA: commitSHA, Intent: "apply"}
	principal := AccessPrincipal{Source: AccessPrincipalSourceManagedToken, TokenID: "oidc-managed-token-1", Scopes: []string{ScopeFleetOperate}, CI: ci}
	call := func(path, key string, body interface{}) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
		req.Header.Set("Idempotency-Key", key)
		req = WithAccessPrincipal(req, &principal)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	path := "/api/v1/fleet/plans/" + planID
	create := fleet.RunnerAttemptCreateRequest{SchemaVersion: fleet.RunnerAttemptSchemaVersion, RunnerAttemptID: canonicalRunnerAttemptID(ci), CommitSHA: commitSHA, PlanSHA256: planSHA, WorkflowURL: canonicalWorkflowRunURL(ci.Repository, ci.RunID), DispatchNonce: rawNonce, SourceDispatchRunID: "7", HeartbeatTimeoutSeconds: 120}
	created := call(path+"/attempts", "create-one", create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var attempt fleet.RunnerAttempt
	if err := json.Unmarshal(created.Body.Bytes(), &attempt); err != nil {
		t.Fatal(err)
	}
	if attempt.ID == "" || attempt.CurrentPhase != "prechange_verified" {
		t.Fatalf("created attempt=%+v", attempt)
	}
	if replay := call(path+"/attempts", "create-one", create); replay.Code != http.StatusOK {
		t.Fatalf("create replay: %d %s", replay.Code, replay.Body.String())
	}
	advancePath := path + "/attempts/" + attempt.ID + "/advance"
	advance := fleet.RunnerAttemptAdvanceRequest{SchemaVersion: fleet.RunnerAttemptSchemaVersion, ExpectedPhase: attempt.CurrentPhase, Revision: attempt.Revision}
	if noEvidence := call(advancePath, "", advance); noEvidence.Code != http.StatusConflict {
		t.Fatalf("advance without checkpoint: %d %s", noEvidence.Code, noEvidence.Body.String())
	}
	checkpoint := fleet.ReconciliationRequest{SchemaVersion: fleet.ReconciliationSchemaVersion, Phase: "prechange_verified", Status: "succeeded", CommitSHA: commitSHA, PlanSHA256: planSHA, EvidenceDigest: "sha256:" + strings.Repeat("d", 64), AttemptID: attempt.ID}
	if got := call(path+"/reconciliations", "checkpoint-one", checkpoint); got.Code != http.StatusCreated {
		t.Fatalf("checkpoint: %d %s", got.Code, got.Body.String())
	}
	if got := call(advancePath, "", advance); got.Code != http.StatusOK {
		t.Fatalf("advance with signed checkpoint: %d %s", got.Code, got.Body.String())
	}
	if replay := call(path+"/reconciliations", "checkpoint-one", checkpoint); replay.Code != http.StatusOK {
		t.Fatalf("checkpoint replay after phase advance: %d %s", replay.Code, replay.Body.String())
	}
	forged := principal
	forged.CI = &CIIdentity{Provider: "github-actions", Repository: "acme/fleet", RunID: "8", RunAttempt: "1", SHA: commitSHA, Intent: "apply"}
	principal = forged
	if got := call(advancePath, "", advance); got.Code != http.StatusForbidden {
		t.Fatalf("other workflow accessed runner attempt: %d %s", got.Code, got.Body.String())
	}
}
