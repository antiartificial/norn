package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
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
	testCronScheduleHTTPWorkerNomadPostgres(t, false, false, "")
}
func TestCronScheduleLostNomadResponseReconciles(t *testing.T) {
	testCronScheduleHTTPWorkerNomadPostgres(t, true, false, "")
}
func TestCronScheduleTwoWorkersSameKey(t *testing.T) {
	testCronScheduleHTTPWorkerNomadPostgres(t, false, true, "")
}
func TestCronScheduleCrashAfterReservationRemainsUnresolved(t *testing.T) {
	testCronScheduleHTTPWorkerNomadPostgres(t, false, false, "reserved")
}
func TestCronScheduleCrashAfterNomadCommitReconciles(t *testing.T) {
	testCronScheduleHTTPWorkerNomadPostgres(t, false, false, "committed")
}
func TestCronScheduleWorkerProcessCrashAfterNomadCommit(t *testing.T) {
	if os.Getenv("NORN_CRON_SCHEDULE_CRASH_WORKER") == "1" {
		runCronScheduleCrashWorker(t)
		return
	}
	testCronScheduleHTTPWorkerNomadPostgres(t, false, false, "process-committed")
}

func testCronScheduleHTTPWorkerNomadPostgres(t *testing.T, loseResponse, twoWorkers bool, crashWindow string) {
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
	if crashWindow == "process-committed" {
		testCronScheduleProcessCrashWindow(t, ctx, db, root, address, client, paused, accepted, serve)
		return
	}
	if crashWindow != "" {
		testCronScheduleCrashWindow(t, ctx, db, p, client, job, paused, accepted, serve, crashWindow)
		return
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

// Abandon the first claimed worker at either side of the Nomad write. A
// replacement must never issue another schedule update for an unknown result.
func testCronScheduleCrashWindow(t *testing.T, ctx context.Context, db *store.DB, p *pipeline.Pipeline, client *nomad.Client, originalJob *nomadapi.Job, paused *nomad.PeriodicJobInfo, accepted model.Operation, serve func() (int, model.Operation, string), window string) {
	t.Helper()
	claimed, oldClaim, err := db.ClaimNextOperation(ctx, "crashed-schedule-worker", time.Minute, []string{"app.cron-schedule"})
	if err != nil || claimed == nil || claimed.ID != accepted.ID {
		t.Fatalf("first claim=%+v err=%v", claimed, err)
	}
	es, err := store.NewPGEffectStore(db)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := es.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parse := func(name string) uint64 {
		value, e := strconv.ParseUint(fmt.Sprint(accepted.Payload[name]), 10, 64)
		if e != nil {
			t.Fatalf("parse %s: %v", name, e)
		}
		return value
	}
	jobID := fmt.Sprint(accepted.Payload["jobId"])
	launchPayload, err := json.Marshal(map[string]interface{}{
		"app": accepted.App, "process": "nightly", "previousSchedule": paused.Schedule,
		"schedule": "15 2 * * *", "timezone": paused.TimeZone, "jobId": jobID,
		"imageTag": accepted.Payload["imageTag"], "specDigest": accepted.Payload["specDigest"],
		"deliveryRevision": parse("deliveryRevision"), "version": parse("version"),
		"modifyIndex": parse("modifyIndex"), "paused": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: authority, Resource: "app/" + accepted.App + "/cron/nightly", OperationClaim: effect.OperationClaim{OperationID: oldClaim.OperationID(), OwnerID: oldClaim.OwnerID(), Generation: oldClaim.Generation()}, Stage: "app.cron-schedule.nomad", Supervisor: "nomad-cron-schedule", LaunchPayload: launchPayload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(reservation.Authority + "\x00" + reservation.OperationClaim.OperationID + "\x00" + reservation.InputDigest + "\x00" + fmt.Sprint(reservation.OperationClaim.Generation)))
	reservation.SupervisorExecutionID = "nomad-cron-schedule-" + hex.EncodeToString(sum[:16])
	reserved, err := es.Reserve(ctx, reservation)
	if err != nil || !reserved.Created || reserved.Record.Lifecycle != effect.LifecycleReserved {
		t.Fatalf("effect reservation=%+v err=%v", reserved, err)
	}
	if window == "committed" {
		replacement := *originalJob
		periodic := *originalJob.Periodic
		schedule := "15 2 * * *"
		periodic.Spec = &schedule
		replacement.Periodic = &periodic
		if err := client.UpdatePeriodicJobSchedule(jobID, paused.ModifyIndex, reservation.SupervisorExecutionID, &replacement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, accepted.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckOperationClaim(ctx, oldClaim); err == nil {
		t.Fatal("expired worker retained claim")
	}
	recovered, err := db.GetOperation(ctx, accepted.ID)
	if err != nil || recovered.Status != model.OperationQueued {
		t.Fatalf("recovered operation=%+v err=%v", recovered, err)
	}
	var successor *model.Operation
	var claim store.OperationClaim
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		successor, claim, err = db.ClaimNextOperation(ctx, "successor-schedule-worker", time.Minute, []string{"app.cron-schedule"})
		if err != nil || successor != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || successor == nil || claim.Generation() <= oldClaim.Generation() {
		t.Fatalf("successor claim=%+v err=%v recovered=%+v", successor, err, recovered)
	}
	result, execErr := p.ExecuteOperation(ctx, successor, claim)
	state, err := client.PeriodicJobSchedule(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if window == "reserved" {
		if !effect.IsDeferred(execErr) || result != nil || state.Version != paused.Version || state.Schedule != paused.Schedule {
			t.Fatalf("unproved reservation advanced: result=%+v err=%v Nomad=%+v", result, execErr, state)
		}
		record, found, lookupErr := es.LatestForOperation(ctx, accepted.ID, reservation.Stage)
		if lookupErr != nil || !found || record.Lifecycle != effect.LifecycleReserved {
			t.Fatalf("unresolved reservation=%+v found=%v err=%v", record, found, lookupErr)
		}
		return
	}
	if execErr != nil || result == nil || result.Status != model.OperationSucceeded || !result.Finished() || state.Version != paused.Version+1 || state.Schedule != "15 2 * * *" || !state.Paused || state.CronScheduleEffectID != reservation.SupervisorExecutionID {
		t.Fatalf("committed schedule was not reconciled: result=%+v err=%v Nomad=%+v", result, execErr, state)
	}
	finished, err := db.GetOperation(ctx, accepted.ID)
	if err != nil || finished.Status != model.OperationSucceeded || finished.Metadata["effectId"] != reserved.Record.Token.EffectID {
		t.Fatalf("recovered receipt=%+v err=%v", finished, err)
	}
	cronState, err := db.GetCronState(ctx, accepted.App, "nightly")
	if err != nil || !cronState.Paused || cronState.Schedule != "15 2 * * *" {
		t.Fatalf("recovered cron state=%+v err=%v", cronState, err)
	}
	status, replay, body := serve()
	if status != http.StatusOK || replay.ID != accepted.ID || replay.Receipt == nil {
		t.Fatalf("replay status=%d operation=%+v body=%s", status, replay, body)
	}
}

// The first normal worker is killed after Nomad commits the CAS replacement,
// while its HTTP response is withheld. A separate process must reconcile the
// durable reservation from the exact marker without another Nomad write.
func testCronScheduleProcessCrashWindow(t *testing.T, ctx context.Context, db *store.DB, appsDir, address string, client *nomad.Client, paused *nomad.PeriodicJobInfo, accepted model.Operation, serve func() (int, model.Operation, string)) {
	t.Helper()
	target, err := url.Parse(address)
	if err != nil || target.Scheme != "http" || net.ParseIP(target.Hostname()) == nil || !net.ParseIP(target.Hostname()).IsLoopback() {
		t.Fatal("process-crash qualification requires disposable loopback Nomad")
	}
	committed := make(chan struct{})
	var writes atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isWrite := r.Method == http.MethodPut && r.URL.Path == "/v1/jobs"
		if isWrite && writes.Add(1) != 1 {
			http.Error(w, "duplicate schedule update rejected by qualification", http.StatusConflict)
			return
		}
		upstream := r.Clone(r.Context())
		upstream.URL.Scheme, upstream.URL.Host, upstream.Host, upstream.RequestURI = target.Scheme, target.Host, target.Host, ""
		response, e := http.DefaultTransport.RoundTrip(upstream)
		if e != nil {
			http.Error(w, e.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		if isWrite {
			_, _ = io.Copy(io.Discard, response.Body)
			close(committed)
			<-r.Context().Done()
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
	schema := db.Pool.Config().ConnConfig.RuntimeParams["search_path"]
	if schema == "" {
		t.Fatal("disposable schema is required")
	}
	command := func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCronScheduleWorkerProcessCrashAfterNomadCommit$")
		cmd.Env = append(os.Environ(), "NORN_CRON_SCHEDULE_CRASH_WORKER=1", "NORN_CRON_SCHEDULE_CRASH_SCHEMA="+schema, "NORN_CRON_SCHEDULE_CRASH_APPS_DIR="+appsDir, "NORN_TEST_NOMAD_ADDR="+proxy.URL)
		return cmd
	}
	crashed := command()
	if err := crashed.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = crashed.Process.Kill(); _, _ = crashed.Process.Wait() })
	select {
	case <-committed:
	case <-time.After(20 * time.Second):
		t.Fatal("worker did not reach committed Nomad schedule boundary")
	}
	jobID := fmt.Sprint(accepted.Payload["jobId"])
	state, err := client.PeriodicJobSchedule(jobID)
	if err != nil || state.Version != paused.Version+1 || state.Schedule != "15 2 * * *" || state.CronScheduleEffectID == "" {
		t.Fatalf("Nomad did not commit exact schedule update: state=%+v err=%v", state, err)
	}
	var lifecycle string
	if err := db.Pool.QueryRow(ctx, `SELECT lifecycle FROM operation_effects WHERE operation_id=$1`, accepted.ID).Scan(&lifecycle); err != nil || lifecycle != "reserved" {
		t.Fatalf("pre-kill effect lifecycle=%q err=%v", lifecycle, err)
	}
	if err := crashed.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL worker: %v", err)
	}
	if err := crashed.Wait(); err == nil {
		t.Fatal("worker exited cleanly before SIGKILL")
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=now()-interval '1 second' WHERE id=$1`, accepted.ID); err != nil {
		t.Fatal(err)
	}
	restarted := command()
	if err := restarted.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Process.Kill(); _, _ = restarted.Process.Wait() })
	deadline := time.Now().Add(25 * time.Second)
	var finished *model.Operation
	for time.Now().Before(deadline) {
		finished, err = db.GetOperation(ctx, accepted.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !finished.Active() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if finished == nil || finished.Status != model.OperationSucceeded || writes.Load() != 1 {
		t.Fatalf("restarted operation=%+v writes=%d err=%v", finished, writes.Load(), err)
	}
	state, err = client.PeriodicJobSchedule(jobID)
	if err != nil || state.Version != paused.Version+1 || state.CronScheduleEffectID == "" {
		t.Fatalf("recovery repeated or lost Nomad update: state=%+v err=%v", state, err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT lifecycle FROM operation_effects WHERE operation_id=$1`, accepted.ID).Scan(&lifecycle); err != nil || lifecycle != "completed" {
		t.Fatalf("post-recovery effect lifecycle=%q err=%v", lifecycle, err)
	}
	status, replay, body := serve()
	if status != http.StatusOK || replay.ID != accepted.ID || replay.Receipt == nil {
		t.Fatalf("replay status=%d operation=%+v body=%s", status, replay, body)
	}
}

func runCronScheduleCrashWorker(t *testing.T) {
	t.Helper()
	db, err := openCronTriggerCrashDB(os.Getenv("NORN_TEST_DATABASE_URL"), os.Getenv("NORN_CRON_SCHEDULE_CRASH_SCHEMA"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client, err := nomad.NewClient(os.Getenv("NORN_TEST_NOMAD_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{DB: db, Nomad: client, AppsDir: os.Getenv("NORN_CRON_SCHEDULE_CRASH_APPS_DIR"), SagaStore: saga.NewPostgresStore(db.Pool)}
	p.CronScheduleEffects, err = pipeline.NewNomadCronScheduleEffects(db, client, p)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, client, nil, nil, &config.Config{AppsDir: p.AppsDir, AuditSigningKey: strings.Repeat("c", 32)}, p, nil, nil, p.SagaStore, nil, nil)
	p.SetOperationStore(h.OperationStore())
	worker.NewOperationWorkerForKinds(db, p, []string{"app.cron-schedule"}).Run(context.Background())
}
