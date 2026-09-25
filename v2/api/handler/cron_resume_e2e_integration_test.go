package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/config"
	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

// This qualification needs a disposable Nomad agent and PostgreSQL database.
// It drives HTTP acceptance, the claimed worker, the real Nomad CAS effect,
// and the PostgreSQL cron state and operation receipt in one path.
func TestCronResumeHTTPWorkerNomadPostgres(t *testing.T) {
	testCronResumeHTTPWorkerNomadPostgres(t, false, "")
}

func TestCronResumeLostNomadResponseReconciles(t *testing.T) {
	testCronResumeHTTPWorkerNomadPostgres(t, true, "")
}

func TestCronResumeCrashAfterReservationRemainsUnresolved(t *testing.T) {
	testCronResumeHTTPWorkerNomadPostgres(t, false, "reserved")
}

func TestCronResumeCrashAfterNomadCommitReconciles(t *testing.T) {
	testCronResumeHTTPWorkerNomadPostgres(t, false, "committed")
}

func testCronResumeHTTPWorkerNomadPostgres(t *testing.T, loseRegistrationResponse bool, crashWindow string) {
	address := os.Getenv("NORN_TEST_NOMAD_ADDR")
	if address == "" || os.Getenv("NORN_TEST_DATABASE_URL") == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR and NORN_TEST_DATABASE_URL to disposable services")
	}
	db := acceptanceIntegrationDB(t)
	ctx := context.Background()
	nomadClient, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}

	app := "cron-qual-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	root := t.TempDir()
	appDir := filepath.Join(root, app)
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := "name: " + app + "\ndeploy: true\nprocesses:\n  nightly:\n    command: /bin/true\n    schedule: \"0 0 * * *\"\n    timezone: UTC\n"
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := model.LoadInfraSpec(filepath.Join(appDir, "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	image := "busybox:1.36"
	job := nomad.TranslatePeriodic(loaded, "nightly", loaded.Processes["nightly"], image, nil)
	if job == nil {
		t.Fatal("no periodic job")
	}
	jobID := app + "-nightly"
	t.Cleanup(func() { _, _, _ = nomadClient.API().Jobs().Deregister(jobID, true, nil) })
	if _, _, err := nomadClient.API().Jobs().Register(job, nil); err != nil {
		t.Fatal(err)
	}
	running, err := nomadClient.PeriodicJobSchedule(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := nomadClient.PausePeriodicJob(jobID, running.ModifyIndex, "fixture-pause"); err != nil {
		t.Fatal(err)
	}
	paused, err := nomadClient.PeriodicJobSchedule(jobID)
	if err != nil || !paused.Paused {
		t.Fatalf("job pause: %+v, %v", paused, err)
	}
	if err := db.UpsertCronState(ctx, app, "nightly", true, paused.Schedule); err != nil {
		t.Fatal(err)
	}
	deployment := &model.Deployment{ID: uuid.NewString(), App: app, CommitSHA: strings.Repeat("a", 40), ImageTag: image, SagaID: uuid.NewString(), Status: model.StatusDeployed, StartedAt: time.Now().UTC()}
	if err := db.InsertDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	var forwardedRegistrations atomic.Int32
	if loseRegistrationResponse {
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
			response, err := http.DefaultTransport.RoundTrip(upstream)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			defer response.Body.Close()
			if r.Method == http.MethodPut && r.URL.Path == "/v1/jobs" {
				forwardedRegistrations.Add(1)
				_, _ = io.Copy(io.Discard, response.Body)
				// The registration committed in Nomad, but its reply is lost.
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("hijack committed registration response: %v", err)
					return
				}
				_ = conn.Close()
				return
			}
			for name, values := range response.Header {
				for _, value := range values {
					w.Header().Add(name, value)
				}
			}
			w.WriteHeader(response.StatusCode)
			_, _ = io.Copy(w, response.Body)
		}))
		t.Cleanup(proxy.Close)
		nomadClient, err = nomad.NewClient(proxy.URL)
		if err != nil {
			t.Fatal(err)
		}
	}

	p := &pipeline.Pipeline{DB: db, Nomad: nomadClient, AppsDir: root, SagaStore: saga.NewPostgresStore(db.Pool)}
	p.CronResumeEffects, err = pipeline.NewNomadCronResumeEffects(db, nomadClient, p)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, nomadClient, nil, nil, &config.Config{AppsDir: root, AuditSigningKey: strings.Repeat("c", 32)}, p, nil, nil, p.SagaStore, nil, nil)
	p.SetOperationStore(h.OperationStore())
	serve := func(key string) (int, model.Operation, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/"+app+"/cron/resume", bytes.NewBufferString(`{"process":"nightly"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", app)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", TokenID: "cron-test-token", DeviceID: "cron-test-device", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}})
		rec := httptest.NewRecorder()
		h.MutationAuditMiddleware(http.HandlerFunc(h.CronResume)).ServeHTTP(rec, req)
		var operation model.Operation
		if rec.Code == http.StatusAccepted || rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &operation); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, operation, rec.Body.String()
	}
	status, accepted, body := serve("cron-resume-e2e")
	if status != http.StatusAccepted || accepted.Kind != "app.cron-resume" {
		t.Fatalf("accept status=%d op=%+v body=%s", status, accepted, body)
	}
	if crashWindow != "" {
		testCronResumeCrashWindow(t, ctx, db, p, nomadClient, job, paused, accepted, serve, crashWindow)
		return
	}
	operationWorker := worker.NewOperationWorkerForKinds(db, p, []string{"app.cron-resume"})
	workerCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	go operationWorker.Run(workerCtx)
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
	resumed, err := nomadClient.PeriodicJobSchedule(jobID)
	if err != nil || resumed.Paused || resumed.CronResumeEffectID == "" {
		t.Fatalf("Nomad resume=%+v, %v", resumed, err)
	}
	if loseRegistrationResponse {
		if got := forwardedRegistrations.Load(); got != 1 {
			t.Fatalf("forwarded resume registrations=%d, want exactly 1", got)
		}
		if resumed.Version != paused.Version+1 {
			t.Fatalf("Nomad parent version=%d, want one mutation after paused version %d", resumed.Version, paused.Version)
		}
	}
	state, err := db.GetCronState(ctx, app, "nightly")
	if err != nil || state.Paused || state.Schedule != resumed.Schedule {
		t.Fatalf("cron state=%+v, %v", state, err)
	}
	if finished.Metadata["effectId"] == "" {
		t.Fatalf("missing atomic effect receipt: %+v", finished)
	}
	if loseRegistrationResponse {
		effects, err := store.NewPGEffectStore(db)
		if err != nil {
			t.Fatal(err)
		}
		record, found, err := effects.LatestForOperation(ctx, accepted.ID, "app.cron-resume.nomad")
		if err != nil || !found || record.Lifecycle != effect.LifecycleCompleted || record.Completion == nil || record.Completion.Outcome != effect.OutcomeSucceeded || record.Reservation.SupervisorExecutionID != resumed.CronResumeEffectID {
			t.Fatalf("reconciled effect=%+v found=%v err=%v", record, found, err)
		}
	}
	replayStatus, replay, replayBody := serve("cron-resume-e2e")
	if replayStatus != http.StatusOK || replay.ID != accepted.ID || replay.Receipt == nil {
		t.Fatalf("replay status=%d op=%+v body=%s", replayStatus, replay, replayBody)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind='app.cron-resume' AND app=$1`, app).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("resume operation count=%d, want 1", count)
	}
}

// Recreate a process death at the two durable boundaries without relying on
// goroutine timing. The first owner is deliberately abandoned after Reserve;
// in the committed case Nomad performs the exact guarded write before that
// owner can persist its effect completion or operation receipt.
func testCronResumeCrashWindow(t *testing.T, ctx context.Context, db *store.DB, p *pipeline.Pipeline, client *nomad.Client, replacement *nomadapi.Job, paused *nomad.PeriodicJobInfo, accepted model.Operation, serve func(string) (int, model.Operation, string), window string) {
	t.Helper()
	claimed, oldClaim, err := db.ClaimNextOperation(ctx, "crashed-cron-worker", time.Minute, []string{"app.cron-resume"})
	if err != nil || claimed == nil || claimed.ID != accepted.ID {
		t.Fatalf("first claim=%+v, %v", claimed, err)
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
		value, err := strconv.ParseUint(fmt.Sprint(accepted.Payload[name]), 10, 64)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return value
	}
	jobID := fmt.Sprint(accepted.Payload["jobId"])
	payload, err := json.Marshal(map[string]interface{}{
		"app": accepted.App, "process": "nightly", "schedule": paused.Schedule,
		"timezone": paused.TimeZone, "jobId": jobID,
		"imageTag": accepted.Payload["imageTag"], "specDigest": accepted.Payload["specDigest"],
		"deliveryRevision": parse("deliveryRevision"), "version": parse("version"), "modifyIndex": parse("modifyIndex"),
	})
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: authority, Resource: "app/" + accepted.App + "/cron/nightly", OperationClaim: effect.OperationClaim{OperationID: oldClaim.OperationID(), OwnerID: oldClaim.OwnerID(), Generation: oldClaim.Generation()}, Stage: "app.cron-resume.nomad", Supervisor: "nomad-cron-resume", LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(reservation.Authority + "\x00" + reservation.OperationClaim.OperationID + "\x00" + reservation.InputDigest + "\x00" + fmt.Sprint(reservation.OperationClaim.Generation)))
	reservation.SupervisorExecutionID = "nomad-cron-resume-" + hex.EncodeToString(sum[:16])
	reserved, err := es.Reserve(ctx, reservation)
	if err != nil || !reserved.Created || reserved.Record.Lifecycle != effect.LifecycleReserved {
		t.Fatalf("effect reservation=%+v, %v", reserved, err)
	}
	if window == "committed" {
		if err := client.ResumePeriodicJobWithReplacement(jobID, paused.ModifyIndex, reservation.SupervisorExecutionID, replacement); err != nil {
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
	queued, err := db.GetOperation(ctx, accepted.ID)
	if err != nil || queued.Status != model.OperationQueued {
		t.Fatalf("recovered operation=%+v, %v", queued, err)
	}
	successor, claim, err := db.ClaimNextOperation(ctx, "successor-cron-worker", time.Minute, []string{"app.cron-resume"})
	if err != nil || successor == nil || claim.Generation() <= oldClaim.Generation() {
		t.Fatalf("successor claim=%+v, %v", successor, err)
	}
	result, err := p.ExecuteOperation(ctx, successor, claim)
	state, stateErr := client.PeriodicJobSchedule(jobID)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if state.Version != paused.Version+map[bool]uint64{true: 1, false: 0}[window == "committed"] {
		t.Fatalf("Nomad version after recovery=%d, paused version=%d", state.Version, paused.Version)
	}
	if window == "reserved" {
		if !effect.IsDeferred(err) || result != nil || !state.Paused {
			t.Fatalf("reserved crash was not conservative: result=%+v err=%v Nomad=%+v", result, err, state)
		}
		record, found, lookupErr := es.LatestForOperation(ctx, accepted.ID, reservation.Stage)
		if lookupErr != nil || !found || record.Lifecycle != effect.LifecycleReserved {
			t.Fatalf("unresolved reservation=%+v found=%v err=%v", record, found, lookupErr)
		}
		cronState, stateErr := db.GetCronState(ctx, accepted.App, "nightly")
		if stateErr != nil || !cronState.Paused {
			t.Fatalf("cron state changed after reserved crash: %+v, %v", cronState, stateErr)
		}
		return
	}
	if err != nil || result == nil || result.Status != model.OperationSucceeded || !result.Finished() || state.Paused || state.CronResumeEffectID != reservation.SupervisorExecutionID {
		t.Fatalf("committed crash did not reconcile: result=%+v err=%v Nomad=%+v", result, err, state)
	}
	finished, err := db.GetOperation(ctx, accepted.ID)
	if err != nil || finished.Status != model.OperationSucceeded || finished.Metadata["effectId"] != reserved.Record.Token.EffectID {
		t.Fatalf("recovered receipt=%+v, %v", finished, err)
	}
	status, replay, body := serve("cron-resume-e2e")
	if status != http.StatusOK || replay.ID != accepted.ID || replay.Receipt == nil {
		t.Fatalf("replay status=%d op=%+v body=%s", status, replay, body)
	}
}
