package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
)

const catalogRefCanary = "catalog-ref-canary-9d2"

func handlerTestCatalog(generation uint64) database.Catalog {
	return database.Catalog{APIVersion: database.APIVersion,
		Services: []database.DatabaseService{{APIVersion: database.APIVersion, ID: "pg-main", Generation: 1, Purpose: database.PurposeApplication, Engine: database.EnginePostgreSQL,
			EngineVersion: "16", ProviderRef: "local:pg-main", Endpoint: database.DatabaseEndpoint{Host: "db.internal.example", Port: 5432},
			Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled},
			Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilityRuntime, database.CapabilitySnapshot}}}},
		Bindings: []database.DatabaseBinding{{APIVersion: database.APIVersion, ID: "shop-primary", ServiceID: "pg-main", Database: "shop", Role: "shop_app", Generation: generation,
			CredentialRef: "secret:" + catalogRefCanary, TLS: database.DatabaseTLS{Mode: database.TLSDisabled}}},
		Profiles: []database.DeploymentProfile{{APIVersion: database.APIVersion, ID: "mini", Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost,
			DatabaseBindings: map[string]string{"primary": "shop-primary"}}},
	}
}

// Accepted catalog bytes (endpoints, secret references) never reach a reader
// through the generic operation routes, replay or terminal receipts, while
// the stored payload and signed request material keep them unchanged.
func TestCatalogActivationOperationReadsAreRedacted(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	ctx := context.Background()
	cfg := &config.Config{AppsDir: t.TempDir(), AuditSigningKey: strings.Repeat("p", 32)}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())
	const hostCanary = "canary-host.internal.example"
	catalog := handlerTestCatalog(1)
	catalog.Services[0].Endpoint.Host = hostCanary
	leaks := func(body string) bool {
		return strings.Contains(body, hostCanary) || strings.Contains(body, catalogRefCanary) || strings.Contains(body, "secret:")
	}
	activate := func() *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(map[string]interface{}{"expectedRevision": 0, "catalog": catalog})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/database/catalog/activations", bytes.NewReader(encoded))
		req.Header.Set("Idempotency-Key", "redaction-1")
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopePlatformOperate}})
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.ActivateDatabaseCatalog)).ServeHTTP(rec, req)
		return rec
	}
	first := activate()
	if first.Code != http.StatusAccepted || leaks(first.Body.String()) {
		t.Fatalf("activation = %d %s", first.Code, first.Body.String())
	}
	if replay := activate(); replay.Code != http.StatusOK || leaks(replay.Body.String()) {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	operationID := strings.TrimPrefix(first.Header().Get("Location"), "/api/v1/operations/")
	read := func(handlerFunc http.HandlerFunc, path string, id string) string {
		t.Helper()
		req := WithAccessPrincipal(httptest.NewRequest(http.MethodGet, path, nil), &AccessPrincipal{Subject: "reader", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}})
		route := chi.NewRouteContext()
		if id != "" {
			route.URLParams.Add("id", id)
		}
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rec := httptest.NewRecorder()
		handlerFunc(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	for name, body := range map[string]string{
		"get":    read(h.GetOperation, "/api/v1/operations/"+operationID, operationID),
		"list":   read(h.ListOperations, "/api/v1/operations?kind="+pipeline.CatalogActivationKind, ""),
		"active": read(h.ActiveOperations, "/api/v1/operations/active", ""),
	} {
		if leaks(body) || !strings.Contains(body, operationID) || !strings.Contains(body, `"catalogDigest"`) {
			t.Fatalf("%s projection = %s", name, body)
		}
	}
	// After execution the terminal receipt is redacted too.
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at = now() - interval '1 second' WHERE id=$1`, operationID); err != nil {
		t.Fatal(err)
	}
	op, claim, err := db.ClaimNextOperation(ctx, "redaction-test", time.Minute, []string{pipeline.CatalogActivationKind})
	if err != nil || op == nil {
		t.Fatalf("claim = %+v, %v", op, err)
	}
	if result, err := pipe.ExecuteOperation(ctx, op, claim); err != nil || !result.Finished() {
		t.Fatalf("execute = %+v, %v", result, err)
	}
	if body := read(h.GetOperation, "/api/v1/operations/"+operationID, operationID); leaks(body) || !strings.Contains(body, `"receipt"`) {
		t.Fatalf("terminal projection = %s", body)
	}
	// Stored payload and signed request material are unchanged.
	var storedPayload, signedRequest string
	if err := db.Pool.QueryRow(ctx, `SELECT o.payload::text, convert_from(i.request_canonical_bytes,'UTF8') FROM operations o JOIN operation_acceptance_intents i ON i.operation_id=o.id WHERE o.id=$1`, operationID).Scan(&storedPayload, &signedRequest); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(storedPayload, hostCanary) || !strings.Contains(signedRequest, hostCanary) {
		t.Fatal("redaction altered the stored or signed catalog bytes")
	}
}

func TestDatabaseCatalogActivationUsesDurableAuditedAcceptance(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	ctx := context.Background()
	cfg := &config.Config{AppsDir: t.TempDir(), AuditSigningKey: strings.Repeat("c", 32)}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool)}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	pipe.SetOperationStore(h.OperationStore())

	serve := func(scopes []string, key string, body interface{}) *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/database/catalog/activations", bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		if scopes != nil {
			req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", Source: AccessPrincipalSourceSharedAPI, Scopes: scopes})
		}
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.ActivateDatabaseCatalog)).ServeHTTP(rec, req)
		return rec
	}
	operations := func() int {
		var count int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind=$1`, pipeline.CatalogActivationKind).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	body := map[string]interface{}{"expectedRevision": 0, "catalog": handlerTestCatalog(1)}

	// Authorization: no principal, and a principal without platform:operate.
	if rec := serve(nil, "a1", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve([]string{ScopeAPIRead, ScopeAPIWrite}, "a1", body); rec.Code != http.StatusForbidden {
		t.Fatalf("api:write without platform:operate = %d %s", rec.Code, rec.Body.String())
	}
	// Invalid documents are refused before acceptance.
	invalid := handlerTestCatalog(1)
	invalid.Bindings[0].ServiceID = "missing"
	if rec := serve([]string{ScopePlatformOperate}, "a2", map[string]interface{}{"expectedRevision": 0, "catalog": invalid}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "catalog_activation_refused") {
		t.Fatalf("invalid catalog = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve([]string{ScopePlatformOperate}, "a3", map[string]interface{}{"expectedRevision": 0, "catalog": handlerTestCatalog(1), "force": true}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve([]string{ScopePlatformOperate}, "a4", map[string]interface{}{"expectedRevision": 3, "catalog": handlerTestCatalog(1)}); rec.Code != http.StatusConflict {
		t.Fatalf("wrong expected revision = %d %s", rec.Code, rec.Body.String())
	}
	if operations() != 0 {
		t.Fatal("refused requests created operations")
	}

	first := serve([]string{ScopePlatformOperate}, "activate-1", body)
	if first.Code != http.StatusAccepted || strings.Contains(first.Body.String(), catalogRefCanary) {
		t.Fatalf("activation = %d %s", first.Code, first.Body.String())
	}
	var accepted model.Operation
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	replay := serve([]string{ScopePlatformOperate}, "activate-1", body)
	var replayed model.Operation
	_ = json.Unmarshal(replay.Body.Bytes(), &replayed)
	if replay.Code != http.StatusOK || replayed.ID != accepted.ID {
		t.Fatalf("identical retry = %d %s", replay.Code, replay.Body.String())
	}
	if changed := serve([]string{ScopePlatformOperate}, "activate-1", map[string]interface{}{"expectedRevision": 0, "catalog": handlerTestCatalog(2)}); changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "idempotency_key_reused") {
		t.Fatalf("changed catalog under the same key = %d %s", changed.Code, changed.Body.String())
	}
	if operations() != 1 {
		t.Fatalf("operations = %d", operations())
	}
	var receipts int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM mutation_audit_events WHERE path='/api/v1/database/catalog/activations'`).Scan(&receipts); err != nil || receipts == 0 {
		t.Fatalf("mutation audit receipts = %d, %v", receipts, err)
	}

	// Nothing is active until the operation executes.
	if _, err := db.ActiveDatabaseCatalog(ctx); err == nil {
		t.Fatal("catalog active before execution")
	}
	execute := func(id string) *pipeline.OperationResult {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at = CASE WHEN id=$1 THEN now() - interval '1 second' ELSE now() + interval '1 hour' END WHERE status='queued'`, id); err != nil {
			t.Fatal(err)
		}
		op, claim, err := db.ClaimNextOperation(ctx, "catalog-test", time.Minute, []string{pipeline.CatalogActivationKind})
		if err != nil || op == nil || op.ID != id {
			t.Fatalf("claim = %+v, %v", op, err)
		}
		result, err := pipe.ExecuteOperation(ctx, op, claim)
		if err != nil {
			t.Fatal(err)
		}
		if result.Finished() {
			// Success committed with the operation's terminal record, so
			// re-executing the same claim cannot activate or report again.
			if again, err := pipe.ExecuteOperation(ctx, op, claim); err == nil {
				t.Fatalf("re-execution after an atomic finish = %+v", again)
			}
			return result
		}
		if err := db.FinishClaimedOperation(ctx, claim, result.Status, result.Message, result.Metadata); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := execute(accepted.ID); result.Status != model.OperationSucceeded || result.Metadata["revision"] != int64(1) {
		t.Fatalf("execution = %+v", result)
	}

	// Redacted inspection: identity and generations, never references.
	req := WithAccessPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/database/catalog", nil), &AccessPrincipal{Subject: "reader", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}})
	rec := httptest.NewRecorder()
	h.GetDatabaseCatalog(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), catalogRefCanary) || strings.Contains(rec.Body.String(), "secret:") ||
		!strings.Contains(rec.Body.String(), `"credentialConfigured":true`) || !strings.Contains(rec.Body.String(), `"revision":1`) {
		t.Fatalf("inspection = %d %s", rec.Code, rec.Body.String())
	}

	// Two activations accepted against the same revision: one applies, the
	// other is refused at execution by the compare-and-set.
	bump := map[string]interface{}{"expectedRevision": 1, "catalog": handlerTestCatalog(2)}
	second := serve([]string{ScopePlatformOperate}, "activate-2", bump)
	third := serve([]string{ScopePlatformOperate}, "activate-3", map[string]interface{}{"expectedRevision": 1, "catalog": handlerTestCatalog(3)})
	var secondOp, thirdOp model.Operation
	_ = json.Unmarshal(second.Body.Bytes(), &secondOp)
	_ = json.Unmarshal(third.Body.Bytes(), &thirdOp)
	if second.Code != http.StatusAccepted || third.Code != http.StatusAccepted {
		t.Fatalf("accepted = %d/%d", second.Code, third.Code)
	}
	if result := execute(secondOp.ID); result.Status != model.OperationSucceeded {
		t.Fatalf("second = %+v", result)
	}
	if result := execute(thirdOp.ID); result.Status != model.OperationFailed || !strings.Contains(result.Message, "refused") {
		t.Fatalf("stale activation = %+v", result)
	}
	if active, err := db.ActiveDatabaseCatalog(ctx); err != nil || active.Revision != 2 || active.Catalog.Bindings[0].Generation != 2 {
		t.Fatalf("active = %+v, %v", active, err)
	}

	// Health requires a database profile.
	route := chi.NewRouteContext()
	route.URLParams.Add("id", "shop")
	healthReq := WithAccessPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/apps/shop/databases/health", nil), &AccessPrincipal{Subject: "reader", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}})
	healthReq = healthReq.WithContext(context.WithValue(healthReq.Context(), chi.RouteCtxKey, route))
	healthRec := httptest.NewRecorder()
	h.AppDatabaseHealth(healthRec, healthReq)
	if healthRec.Code != http.StatusNotFound && healthRec.Code != http.StatusConflict {
		t.Fatalf("health without app/profile = %d %s", healthRec.Code, healthRec.Body.String())
	}
}
