package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/config"
	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

// This qualification owns a disposable periodic job and drives the schedule
// HTTP endpoint through signed acceptance, the claimed worker, Nomad CAS, and
// PostgreSQL state. In the lost-response variant Nomad commits the guarded
// replacement while the client loses its response; recovery must observe the
// exact effect marker and never register a second replacement.
func TestCronScheduleHTTPWorkerNomadPostgres(t *testing.T) {
	testCronScheduleHTTPWorkerNomadPostgres(t, false, false)
}
func TestCronScheduleLostNomadResponseReconciles(t *testing.T) {
	testCronScheduleHTTPWorkerNomadPostgres(t, true, false)
}
func TestCronScheduleTwoWorkersSameKey(t *testing.T) {
	testCronScheduleHTTPWorkerNomadPostgres(t, false, true)
}

func testCronScheduleHTTPWorkerNomadPostgres(t *testing.T, loseResponse, twoWorkers bool) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	if address == "" || os.Getenv("NORN_TEST_DATABASE_URL") == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR and NORN_TEST_DATABASE_URL to disposable services")
	}
	ctx := context.Background()
	db := acceptanceIntegrationDB(t)
	client, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	app := "cron-schedule-qual-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	root := t.TempDir()
	appDir := filepath.Join(root, app)
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	specText := "name: " + app + "\ndeploy: true\nprocesses:\n  nightly:\n    command: /bin/true\n    schedule: \"0 0 * * *\"\n    timezone: UTC\n"
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(specText), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := model.LoadInfraSpec(filepath.Join(appDir, "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	job := nomad.TranslatePeriodic(spec, "nightly", spec.Processes["nightly"], "busybox:1.36", nil)
	if job == nil {
		t.Fatal("no periodic job")
	}
	jobID := app + "-nightly"
	t.Cleanup(func() { _, _, _ = client.API().Jobs().Deregister(jobID, true, nil) })
	if _, _, err := client.API().Jobs().Register(job, nil); err != nil {
		t.Fatal(err)
	}
	running, err := client.PeriodicJobSchedule(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.PausePeriodicJob(jobID, running.ModifyIndex, "fixture-pause"); err != nil {
		t.Fatal(err)
	}
	paused, err := client.PeriodicJobSchedule(jobID)
	if err != nil || !paused.Paused {
		t.Fatalf("fixture pause=%+v err=%v", paused, err)
	}
	if err = db.UpsertCronState(ctx, app, "nightly", true, paused.Schedule); err != nil {
		t.Fatal(err)
	}
	if err = db.InsertDeployment(ctx, &model.Deployment{ID: uuid.NewString(), App: app, CommitSHA: strings.Repeat("a", 40), ImageTag: "busybox:1.36", SagaID: uuid.NewString(), Status: model.StatusDeployed, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var forwarded atomic.Int32
	if loseResponse {
		target, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstream := r.Clone(r.Context())
			upstream.URL.Scheme = target.Scheme
			upstream.URL.Host = target.Host
			upstream.Host = target.Host
			upstream.RequestURI = ""
			response, e := http.DefaultTransport.RoundTrip(upstream)
			if e != nil {
				http.Error(w, e.Error(), http.StatusBadGateway)
				return
			}
			defer response.Body.Close()
			if r.Method == http.MethodPut && r.URL.Path == "/v1/jobs" {
				forwarded.Add(1)
				_, _ = io.Copy(io.Discard, response.Body)
				conn, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Errorf("hijack response: %v", e)
					return
				}
				_ = conn.Close()
				return
			}
			for n, values := range response.Header {
				for _, value := range values {
					w.Header().Add(n, value)
				}
			}
			w.WriteHeader(response.StatusCode)
			_, _ = io.Copy(w, response.Body)
		}))
		t.Cleanup(proxy.Close)
		client, err = nomad.NewClient(proxy.URL)
		if err != nil {
			t.Fatal(err)
		}
	}
	p := &pipeline.Pipeline{DB: db, Nomad: client, AppsDir: root, SagaStore: saga.NewPostgresStore(db.Pool)}
	p.CronScheduleEffects, err = pipeline.NewNomadCronScheduleEffects(db, client, p)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, client, nil, nil, &config.Config{AppsDir: root, AuditSigningKey: strings.Repeat("c", 32)}, p, nil, nil, p.SagaStore, nil, nil)
	p.SetOperationStore(h.OperationStore())
	serve := func() (int, model.Operation, string) {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/apps/"+app+"/cron/schedule", bytes.NewBufferString(`{"process":"nightly","schedule":"15 2 * * *"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "cron-schedule-e2e")
		route := chi.NewRouteContext()
		route.URLParams.Add("id", app)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "cron-test-token", DeviceID: "cron-test-device", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.CronUpdateSchedule)).ServeHTTP(rec, req)
		var op model.Operation
		if rec.Code == http.StatusAccepted || rec.Code == http.StatusOK {
			if e := json.Unmarshal(rec.Body.Bytes(), &op); e != nil {
				t.Fatal(e)
			}
		}
		return rec.Code, op, rec.Body.String()
	}
	var status int
	var accepted model.Operation
	var body string
	if twoWorkers {
		type response struct {
			status int
			op     model.Operation
			body   string
		}
		responses := make([]response, 4)
		start := make(chan struct{})
		var group sync.WaitGroup
		for i := range responses {
			group.Add(1)
			go func(i int) {
				defer group.Done()
				<-start
				responses[i].status, responses[i].op, responses[i].body = serve()
			}(i)
		}
		close(start)
		group.Wait()
		acceptedCount := 0
		for i, response := range responses {
			if response.status == http.StatusAccepted {
				acceptedCount++
				status, accepted, body = response.status, response.op, response.body
			} else if response.status != http.StatusOK {
				t.Fatalf("duplicate request %d: status=%d op=%+v body=%s", i, response.status, response.op, response.body)
			}
		}
		if acceptedCount != 1 {
			t.Fatalf("concurrent accepts=%d, want one", acceptedCount)
		}
		for i, response := range responses {
			if response.op.ID != accepted.ID {
				t.Fatalf("duplicate request %d returned operation %s, want %s", i, response.op.ID, accepted.ID)
			}
		}
	} else {
		status, accepted, body = serve()
	}
	if status != http.StatusAccepted || accepted.Kind != "app.cron-schedule" {
		t.Fatalf("accept status=%d operation=%+v body=%s", status, accepted, body)
	}
	workerCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	go worker.NewOperationWorkerForKinds(db, p, []string{"app.cron-schedule"}).Run(workerCtx)
	if twoWorkers {
		secondPool, poolErr := pgxpool.NewWithConfig(ctx, db.Pool.Config())
		if poolErr != nil {
			t.Fatal(poolErr)
		}
		defer secondPool.Close()
		secondDB := &store.DB{Pool: secondPool}
		secondPipeline := &pipeline.Pipeline{DB: secondDB, Nomad: client, AppsDir: root, SagaStore: saga.NewPostgresStore(secondPool)}
		secondPipeline.CronScheduleEffects, err = pipeline.NewNomadCronScheduleEffects(secondDB, client, secondPipeline)
		if err != nil {
			t.Fatal(err)
		}
		secondPipeline.SetOperationStore(h.OperationStore())
		go worker.NewOperationWorkerForKinds(secondDB, secondPipeline, []string{"app.cron-schedule"}).Run(workerCtx)
	}
	var finished *model.Operation
	for workerCtx.Err() == nil {
		finished, err = db.GetOperation(ctx, accepted.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finished.Status == model.OperationSucceeded || finished.Status == model.OperationFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if finished == nil || finished.Status != model.OperationSucceeded {
		t.Fatalf("worker result=%+v err=%v", finished, workerCtx.Err())
	}
	updated, err := client.PeriodicJobSchedule(jobID)
	if err != nil || !updated.Paused || updated.Schedule != "15 2 * * *" || updated.CronScheduleEffectID == "" {
		t.Fatalf("updated periodic parent=%+v err=%v", updated, err)
	}
	if updated.Version != paused.Version+1 {
		t.Fatalf("parent version=%d want one CAS update from %d", updated.Version, paused.Version)
	}
	state, err := db.GetCronState(ctx, app, "nightly")
	if err != nil || !state.Paused || state.Schedule != updated.Schedule {
		t.Fatalf("persisted cron state=%+v err=%v", state, err)
	}
	if finished.Metadata["effectId"] == "" {
		t.Fatalf("missing atomic effect receipt: %+v", finished)
	}
	if loseResponse {
		if got := forwarded.Load(); got != 1 {
			t.Fatalf("forwarded registrations=%d want 1", got)
		}
		effects, e := store.NewPGEffectStore(db)
		if e != nil {
			t.Fatal(e)
		}
		record, found, e := effects.LatestForOperation(ctx, accepted.ID, "app.cron-schedule.nomad")
		if e != nil || !found || record.Lifecycle != effect.LifecycleCompleted || record.Completion == nil || record.Completion.Outcome != effect.OutcomeSucceeded || record.Reservation.SupervisorExecutionID != updated.CronScheduleEffectID {
			t.Fatalf("reconciled effect=%+v found=%v err=%v", record, found, e)
		}
	}
	replayStatus, replay, replayBody := serve()
	if replayStatus != http.StatusOK || replay.ID != accepted.ID || replay.Receipt == nil {
		t.Fatalf("replay status=%d operation=%+v body=%s", replayStatus, replay, replayBody)
	}
	var count int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind='app.cron-schedule' AND app=$1`, app).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("schedule operation count=%d want 1", count)
	}
}
