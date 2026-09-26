package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

// TestClaimedFunctionV3HTTPNomadPostgres is an opt-in qualification of the
// complete PostgreSQL function-v3 path. It owns a schema and uniquely named
// service/function jobs on a disposable loopback Nomad agent.
func TestClaimedFunctionV3HTTPNomadPostgres(t *testing.T) {
	address, image, databaseURL := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_FUNCTION_IMAGE"), os.Getenv("NORN_TEST_DATABASE_URL")
	if address == "" || image == "" || databaseURL == "" || os.Getenv("NORN_TEST_NOMAD_DOCKER") != "1" {
		t.Skip("set disposable NORN_TEST_NOMAD_ADDR, NORN_TEST_FUNCTION_IMAGE, NORN_TEST_DATABASE_URL, and NORN_TEST_NOMAD_DOCKER=1")
	}
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "http" || net.ParseIP(endpoint.Hostname()) == nil || !net.ParseIP(endpoint.Hostname()).IsLoopback() || !model.IsContentAddressedImage(image) {
		t.Fatal("qualification requires loopback Nomad and a content-addressed image")
	}
	db := functionV3IntegrationDB(t, databaseURL)
	client, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}

	app := "norn-fnv3-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	appsDir := t.TempDir()
	appDir := filepath.Join(appsDir, app)
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte("name: "+app+"\ndeploy: true\nprocesses:\n  web:\n    command: sleep 120\n  resize:\n    command: printf '%s' \\\"$NORN_REQUEST_BODY\\\" | sha256sum\n    function:\n      timeout: 30s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := model.LoadInfraSpec(filepath.Join(appDir, "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	service := nomad.TranslateForRegion(spec, image, nil, spec.ResolvedRegions()[0])
	service.TaskGroups[0].Tasks[0].Config["force_pull"] = false
	if _, _, err := client.API().Jobs().Register(service, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _, _ = client.API().Jobs().Deregister(app, true, nil) })
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	for {
		if err := client.VerifyRunningAppImage(ctx, spec, image); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("service image never became provable: %v", err)
		}
		time.Sleep(time.Second)
	}

	deployment := &model.Deployment{ID: uuid.NewString(), App: app, SagaID: uuid.NewString(), Status: model.StatusDeployed, ImageTag: image, SpecDigest: digest, Environment: "staging", CommitSHA: strings.Repeat("a", 40), SourceKind: "git_clone", SourceRef: strings.Repeat("a", 40), StartedAt: time.Now().UTC().Add(-time.Second)}
	if err := db.InsertDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	keyMaterial := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x71}, 32))
	cfg := &config.Config{Profile: "production", Environment: "staging", AppsDir: appsDir, AuditSigningKey: strings.Repeat("a", 32), PrivateInvocationEnabled: true, FunctionV3PreviewEnabled: true, PrivateInvocationCurrentKeyID: "function-live", PrivateInvocationKeys: `{"function-live":"` + keyMaterial + `"}`}
	pipe := &pipeline.Pipeline{DB: db, Nomad: client, AppsDir: appsDir}
	h := handler.New(db, client, nil, nil, cfg, pipe, nil, nil, nil, nil, nil)
	if err := h.OperationStoreError(); err != nil || h.OperationStore() == nil {
		t.Fatalf("operation acceptance=%v store=%T", err, h.OperationStore())
	}
	pipe.SetOperationStore(h.OperationStore())
	admission, claimed, err := configureFunctionV3(cfg, db, pipe, client, nil, h.OperationStore())
	if err != nil || admission == nil || claimed == nil {
		t.Fatalf("configure function v3 admission=%v worker=%v err=%v", admission, claimed, err)
	}

	privateBody := "function-v3-private-body\nwith-utf8-é"
	serve := func(key string) (int, model.Operation, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/"+app+"/invoke", strings.NewReader(`{"process":"resize","body":`+mustJSON(t, privateBody)+`,"method":"POST","path":"/private/function"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", app)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		req = handler.WithAccessPrincipal(req, &handler.AccessPrincipal{Subject: "operator", TokenID: "function-live-token", DeviceID: "function-live-device", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeAPIWrite}})
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(admission)).ServeHTTP(rec, req)
		var op model.Operation
		if rec.Code == http.StatusAccepted || rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &op); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, op, rec.Body.String()
	}
	code, accepted, body := serve("function-live-key")
	if code != http.StatusAccepted || accepted.ID == "" || accepted.Kind != store.PrivateInvocationOperationKind || strings.Contains(body, privateBody) {
		t.Fatalf("accept status=%d operation=%+v body=%s", code, accepted, body)
	}
	if code, replay, body := serve("function-live-key"); code != http.StatusOK || replay.ID != accepted.ID || strings.Contains(body, privateBody) {
		t.Fatalf("replay status=%d operation=%+v body=%s", code, replay, body)
	}

	var completed, last *model.Operation
	for ctx.Err() == nil {
		if err := claimed.RunOnce(ctx); err != nil {
			t.Fatalf("claimed worker: %v", err)
		}
		op, err := db.GetOperation(ctx, accepted.ID)
		if err != nil {
			t.Fatal(err)
		}
		last = op
		if op.Status == model.OperationSucceeded {
			completed = op
			break
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE operations SET next_attempt_at=clock_timestamp() WHERE id=$1`, accepted.ID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if completed == nil {
		t.Fatalf("function invocation did not finish: %v operation=%+v", ctx.Err(), last)
	}

	var variablePath, jobID string
	if err := db.Pool.QueryRow(ctx, `SELECT target FROM function_invocation_effect_attempts WHERE operation_id=$1 AND stage='variable'`, accepted.ID).Scan(&variablePath); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT target FROM function_invocation_effect_attempts WHERE operation_id=$1 AND stage='job'`, accepted.ID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	identity := nomad.FunctionInvocationVariableIdentity{Path: variablePath, OwnerMarker: "norn.function-invoke/" + accepted.ID}
	variable, err := client.LookupFunctionInvocationVariable(ctx, "global", identity)
	var privateRuntime struct {
		Body string `json:"body"`
	}
	if err == nil {
		err = json.Unmarshal(variable.PrivateContent, &privateRuntime)
	}
	if err != nil || variable.State != nomad.FunctionInvocationVariableFound || variable.Path != variablePath || variable.OwnerMarker != identity.OwnerMarker || privateRuntime.Body != privateBody {
		t.Fatalf("private variable=%+v err=%v", variable, err)
	}
	if _, _, err := client.API().Jobs().Info(jobID, nil); err != nil {
		t.Fatalf("exact function job %s: %v", jobID, err)
	}
	if intent, err := db.ClaimFunctionInvocationCleanup(ctx); err != nil || intent != nil {
		t.Fatalf("cleanup before verified archive=%+v err=%v", intent, err)
	}
	if _, err := db.ProcessPendingEvidenceIntent(ctx, 0, func(_ context.Context, _ store.EvidenceIntent, source store.EvidenceSource) (store.EvidencePublication, error) {
		for _, public := range [][]byte{source.OperationJSON, source.FunctionExecutionJSON, source.FunctionEffectAttemptsJSON, source.Acceptance.CanonicalBytes, source.Acceptance.RequestCanonicalBytes} {
			if bytes.Contains(public, []byte(privateBody)) {
				t.Fatal("private function bytes entered archive source")
			}
		}
		return store.EvidencePublication{ObjectKey: "evidence/function-v3-live", ObjectSHA256: strings.Repeat("a", 64), ObjectBytes: 1}, nil
	}); err != nil {
		t.Fatal(err)
	}
	cleanup := &worker.FunctionInvocationCleanupConsumer{Store: db, Remote: client}
	if err := cleanup.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	variable, err = client.LookupFunctionInvocationVariable(ctx, "global", identity)
	if err != nil || variable.State != nomad.FunctionInvocationVariableNotFound {
		t.Fatalf("variable after archive-gated cleanup=%+v err=%v", variable, err)
	}
}

func functionV3IntegrationDB(t *testing.T, databaseURL string) *store.DB {
	t.Helper()
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(context.Background(), adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "function_v3_live_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	poolConfig := adminConfig.Copy()
	if poolConfig.ConnConfig.RuntimeParams == nil {
		poolConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	if poolConfig.MaxConns < 4 {
		poolConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	db := &store.DB{Pool: pool}
	t.Cleanup(db.Close)
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
