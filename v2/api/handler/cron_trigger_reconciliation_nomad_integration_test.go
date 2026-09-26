package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/archive"
	"norn/v2/api/config"
	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/retention"
	"norn/v2/api/saga"
)

// TestLiveCronTriggerOperatorReconciliation is opt-in. Its proxy forwards one
// Force to Nomad then loses the response, reproducing an unknowable dispatch
// result while retaining the actual EvalID only for the operator test input.
func TestLiveCronTriggerOperatorReconciliation(t *testing.T) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	if address == "" || os.Getenv("NORN_TEST_DATABASE_URL") == "" {
		t.Skip("set disposable NORN_TEST_NOMAD_ADDR and NORN_TEST_DATABASE_URL")
	}
	target, err := url.Parse(address)
	if err != nil || target.Scheme != "http" || net.ParseIP(target.Hostname()) == nil || !net.ParseIP(target.Hostname()).IsLoopback() {
		t.Fatal("requires disposable loopback Nomad")
	}
	db := acceptanceIntegrationDB(t)
	realNomad, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	app := "cron-reconcile-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	root := t.TempDir()
	appDir := filepath.Join(root, app)
	if err := os.MkdirAll(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte("name: "+app+"\ndeploy: true\nprocesses:\n  nightly:\n    command: /bin/true\n    schedule: '0 0 * * *'\n    timezone: UTC\n"), 0600); err != nil {
		t.Fatal(err)
	}
	spec, err := model.LoadInfraSpec(filepath.Join(appDir, "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	job := nomad.TranslatePeriodic(spec, "nightly", spec.Processes["nightly"], "busybox:1.36", nil)
	jobID := app + "-nightly"
	t.Cleanup(func() { _, _, _ = realNomad.API().Jobs().Deregister(jobID, true, nil) })
	t.Cleanup(func() {
		jobs, _, err := realNomad.API().Jobs().List(nil)
		if err != nil {
			t.Errorf("list cron test children for cleanup: %v", err)
			return
		}
		for _, candidate := range jobs {
			if candidate != nil && strings.HasPrefix(candidate.ID, jobID+"/periodic-") {
				if _, _, err := realNomad.API().Jobs().Deregister(candidate.ID, true, nil); err != nil {
					t.Errorf("purge cron test child %s: %v", candidate.ID, err)
				}
			}
		}
	})
	if _, _, err := realNomad.API().Jobs().Register(job, nil); err != nil {
		t.Fatal(err)
	}
	var forceCalls atomic.Int32
	evalIDs := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream := r.Clone(r.Context())
		upstream.URL.Scheme = target.Scheme
		upstream.URL.Host = target.Host
		upstream.Host = target.Host
		upstream.RequestURI = ""
		response, err := http.DefaultTransport.RoundTrip(upstream)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		if r.Method == http.MethodPut && r.URL.Path == "/v1/job/"+jobID+"/periodic/force" {
			forceCalls.Add(1)
			var result struct {
				EvalID string `json:"EvalID"`
			}
			body := io.Reader(response.Body)
			if response.Header.Get("Content-Encoding") == "gzip" {
				compressed, err := gzip.NewReader(response.Body)
				if err != nil {
					t.Errorf("decode Nomad gzip response: %v", err)
					return
				}
				defer compressed.Close()
				body = compressed
			}
			if err := json.NewDecoder(body).Decode(&result); err != nil || result.EvalID == "" {
				t.Errorf("Nomad Force response=%+v err=%v", result, err)
				return
			}
			evalIDs <- result.EvalID
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("lose Nomad response: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(proxy.Close)
	client, err := nomad.NewClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{DB: db, Nomad: client, AppsDir: root, SagaStore: saga.NewPostgresStore(db.Pool)}
	p.CronTriggerEffects, err = pipeline.NewCronTriggerEffects(db, client)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, client, nil, nil, &config.Config{AppsDir: root, AuditSigningKey: strings.Repeat("c", 32)}, p, nil, nil, p.SagaStore, nil, nil)
	p.SetOperationStore(h.OperationStore())
	serve := func(path, body, key string, handle http.HandlerFunc) (int, model.Operation, string) {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", app)
		route.URLParams.Add("operationID", strings.Split(path, "/operations/")[1][:36])
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "cron-reconcile-token", DeviceID: "cron-reconcile-device", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(handle).ServeHTTP(rec, req)
		var op model.Operation
		if rec.Code == http.StatusAccepted || rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &op); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, op, rec.Body.String()
	}
	// Source admission uses its own route context; the operator route below
	// names that exact accepted source operation.
	sourceReq := httptest.NewRequest(http.MethodPost, "/api/v1/apps/"+app+"/cron/trigger", bytes.NewBufferString(`{"process":"nightly"}`))
	sourceReq.Header.Set("Content-Type", "application/json")
	sourceReq.Header.Set("Idempotency-Key", "lost-force")
	route := chi.NewRouteContext()
	route.URLParams.Add("id", app)
	sourceReq = sourceReq.WithContext(context.WithValue(sourceReq.Context(), chi.RouteCtxKey, route))
	sourceReq = WithAccessPrincipal(sourceReq, &AccessPrincipal{Subject: "operator", TokenID: "cron-reconcile-token", DeviceID: "cron-reconcile-device", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
	sourceRec := httptest.NewRecorder()
	h.MutationAuditMiddleware(http.HandlerFunc(h.CronTrigger)).ServeHTTP(sourceRec, sourceReq)
	if sourceRec.Code != http.StatusAccepted {
		t.Fatalf("source admission=%d body=%s", sourceRec.Code, sourceRec.Body.String())
	}
	var source model.Operation
	if err := json.Unmarshal(sourceRec.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for attempt := 0; attempt < 3; attempt++ {
		claimed, claim, err := db.ClaimNextOperation(ctx, "cron-reconcile-worker", time.Minute, []string{"app.cron-trigger"})
		if err != nil || claimed == nil || claimed.ID != source.ID {
			t.Fatalf("source claim=%+v err=%v", claimed, err)
		}
		if result, err := p.ExecuteOperation(ctx, claimed, claim); result != nil || !effect.IsDeferred(err) {
			t.Fatalf("ambiguous Force result=%+v err=%v", result, err)
		}
		terminal, err := db.DeferOrFailCronPauseClaimedOperation(ctx, claim, nil, "Nomad Force response lost", time.Now().Add(-time.Second), map[string]interface{}{"externalEffectRecoveryPending": true})
		if err != nil || terminal != (attempt == 2) {
			t.Fatalf("defer attempt=%d terminal=%v err=%v", attempt, terminal, err)
		}
	}
	if got := forceCalls.Load(); got != 1 {
		t.Fatalf("Force calls=%d want 1", got)
	}
	var evalID string
	select {
	case evalID = <-evalIDs:
	default:
		t.Fatal("proxy did not record committed Nomad EvalID")
	}
	if _, err := realNomad.PeriodicForceEvaluation(ctx, evalID, jobID); err != nil {
		t.Fatalf("live evaluation proof: %v", err)
	}
	before, err := db.GetOperation(ctx, source.ID)
	if err != nil || before.Status != model.OperationFailed {
		t.Fatalf("source receipt=%+v err=%v", before, err)
	}
	var effectID, sourceIntentID string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM operation_effects WHERE operation_id=$1`, source.ID).Scan(&effectID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM evidence_archive_intents WHERE operation_id=$1`, source.ID).Scan(&sourceIntentID); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/apps/" + app + "/operations/" + source.ID + "/cron-trigger-reconciliation"
	body := fmt.Sprintf(`{"effectId":%q,"evalId":%q,"confirm":true}`, effectID, evalID)
	code, correction, response := serve(path, body, "positive-evaluation", http.HandlerFunc(h.QueueCronTriggerReconciliation))
	if code != http.StatusAccepted || correction.Kind != "app.cron-trigger-reconcile" {
		t.Fatalf("correction admission=%d op=%+v body=%s", code, correction, response)
	}
	if code, replay, response := serve(path, body, "positive-evaluation", http.HandlerFunc(h.QueueCronTriggerReconciliation)); code != http.StatusOK || replay.ID != correction.ID {
		t.Fatalf("correction replay=%d op=%+v body=%s", code, replay, response)
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "correction-worker", time.Minute, []string{"app.cron-trigger-reconcile"})
	if err != nil || claimed == nil || claimed.ID != correction.ID {
		t.Fatalf("correction claim=%+v err=%v", claimed, err)
	}
	result, err := p.ExecuteOperation(ctx, claimed, claim)
	if err != nil || result == nil || !result.Finished() || result.Status != model.OperationSucceeded {
		t.Fatalf("correction result=%+v err=%v", result, err)
	}
	after, err := db.GetOperation(ctx, source.ID)
	if err != nil || after.Status != model.OperationFailed || after.Message != before.Message || after.LastError != before.LastError || !after.UpdatedAt.Equal(before.UpdatedAt) || !reflect.DeepEqual(after.Metadata, before.Metadata) {
		t.Fatalf("original receipt changed before=%+v after=%+v err=%v", before, after, err)
	}
	var lifecycle, runtimeID string
	if err := db.Pool.QueryRow(ctx, `SELECT lifecycle,runtime_instance_id FROM operation_effects WHERE id=$1`, effectID).Scan(&lifecycle, &runtimeID); err != nil || lifecycle != "completed" || runtimeID != evalID {
		t.Fatalf("effect lifecycle=%s runtime=%s err=%v", lifecycle, runtimeID, err)
	}
	sourceIntent, err := db.EvidenceIntent(ctx, sourceIntentID)
	if err != nil {
		t.Fatal(err)
	}
	holds, err := db.EvidenceHolds(ctx, sourceIntent, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCronRecoveryHold(holds) {
		t.Fatalf("source hold released before correction archive: %v", holds)
	}
	objects, err := archive.OpenLocal(filepath.Join(t.TempDir(), "archive"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	archiver := &retention.Archiver{DB: db, Archive: objects, Mode: retention.ModeShadow, Quiet: 0, BackfillAfter: time.Hour, MinAge: 0, BatchSize: 10}
	report, err := archiver.RunOnce(ctx)
	if err != nil || report.Published < 2 || len(report.PublishErrors) > 0 {
		t.Fatalf("archive report=%+v err=%v", report, err)
	}
	var correctionArchiveState string
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM evidence_archive_intents WHERE operation_id=$1`, correction.ID).Scan(&correctionArchiveState); err != nil || correctionArchiveState != "verified" {
		t.Fatalf("correction archive=%q err=%v", correctionArchiveState, err)
	}
	holds, err = db.EvidenceHolds(ctx, sourceIntent, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hasCronRecoveryHold(holds) {
		t.Fatalf("source recovery hold remains after verified correction archive: %v", holds)
	}
	if got := forceCalls.Load(); got != 1 {
		t.Fatalf("Force calls after correction=%d", got)
	}
}

func hasCronRecoveryHold(holds []string) bool {
	for _, hold := range holds {
		if hold == "manual-recovery" || hold == "unresolved-effect" {
			return true
		}
	}
	return false
}
