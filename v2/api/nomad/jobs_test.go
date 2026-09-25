package nomad

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestCronRunHealthDetectsOOMRestartsAndFailedAllocation(t *testing.T) {
	t.Parallel()

	restartedAt := time.Date(2026, 8, 22, 10, 6, 23, 0, time.FixedZone("CDT", -5*60*60))
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/allocations") {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("all"); got != "true" {
			t.Fatalf("all query = %q, want true", got)
		}
		_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{
			{
				ID:           "running",
				ClientStatus: nomadapi.AllocClientStatusRunning,
				TaskStates: map[string]*nomadapi.TaskState{
					"sync": {
						State:       "running",
						Restarts:    2,
						LastRestart: restartedAt,
						Events: []*nomadapi.TaskEvent{{
							Type:           nomadapi.TaskTerminated,
							Time:           restartedAt.UnixNano(),
							DisplayMessage: `Exit Code: 0, Exit Message: "OOM Killed"`,
							Details:        map[string]string{"oom_killed": "true"},
						}},
					},
				},
			},
			{ID: "failed", ClientStatus: nomadapi.AllocClientStatusFailed},
		})
	}))

	health, err := client.CronRunHealth("field-harbor-sync/periodic-1")
	if err != nil {
		t.Fatal(err)
	}
	if health.RunningAllocations != 1 || health.FailedAllocations != 1 {
		t.Fatalf("allocation counts = running %d failed %d", health.RunningAllocations, health.FailedAllocations)
	}
	if health.Restarts != 2 || !health.OOMKilled {
		t.Fatalf("restart health = restarts %d oom %t", health.Restarts, health.OOMKilled)
	}
	if !health.LastRestart.Equal(restartedAt) {
		t.Fatalf("last restart = %s, want %s", health.LastRestart, restartedAt)
	}
}

func TestRequireColdStartJobAbsentRequires404AndNoPendingEvaluation(t *testing.T) {
	for _, test := range []struct {
		name        string
		jobStatus   int
		evalStatus  int
		evaluations []*nomadapi.Evaluation
		wantErr     bool
	}{
		{name: "absent with completed history", jobStatus: http.StatusNotFound, evalStatus: http.StatusOK, evaluations: []*nomadapi.Evaluation{{Status: "complete"}}},
		{name: "registered job", jobStatus: http.StatusOK, wantErr: true},
		{name: "pending evaluation", jobStatus: http.StatusNotFound, evalStatus: http.StatusOK, evaluations: []*nomadapi.Evaluation{{Status: "pending"}}, wantErr: true},
		{name: "non 404 job failure", jobStatus: http.StatusInternalServerError, wantErr: true},
		{name: "unreadable evaluations", jobStatus: http.StatusNotFound, evalStatus: http.StatusInternalServerError, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/job/cold-start":
					w.WriteHeader(test.jobStatus)
					if test.jobStatus == http.StatusOK {
						_ = json.NewEncoder(w).Encode(&nomadapi.Job{})
					}
				case "/v1/job/cold-start/evaluations":
					w.WriteHeader(test.evalStatus)
					if test.evalStatus == http.StatusOK {
						_ = json.NewEncoder(w).Encode(test.evaluations)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			err := client.RequireColdStartJobAbsent("global", "cold-start")
			if (err != nil) != test.wantErr {
				t.Fatalf("RequireColdStartJobAbsent error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestPeriodicJobSchedulePreservesVersionAndJobModifyIndex(t *testing.T) {
	jobID, status, schedule, timezone := "widget-nightly", "running", "0 2 * * *", "America/Chicago"
	version, modifyIndex, genericModifyIndex := uint64(3), uint64(42), uint64(99)
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/job/widget-nightly" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Status: &status, Version: &version, ModifyIndex: &genericModifyIndex, JobModifyIndex: &modifyIndex, Meta: map[string]string{cronPauseEffectMetaKey: "effect-1", cronResumeEffectMetaKey: "effect-2"}, Periodic: &nomadapi.PeriodicConfig{Spec: &schedule, TimeZone: &timezone}})
	}))

	info, err := client.PeriodicJobSchedule(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != version || info.ModifyIndex != modifyIndex || info.Schedule != schedule || info.TimeZone != timezone || info.CronPauseEffectID != "effect-1" || info.CronResumeEffectID != "effect-2" {
		t.Fatalf("periodic info = %#v", info)
	}
}

func TestPausePeriodicJobRejectsMutationBetweenReadAndCASWrite(t *testing.T) {
	jobID, status, schedule := "widget-nightly", "running", "0 2 * * *"
	modifyIndex, genericModifyIndex := uint64(42), uint64(99)
	var wrote bool
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.11.5"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/widget-nightly":
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Status: &status, ModifyIndex: &genericModifyIndex, JobModifyIndex: &modifyIndex, Periodic: &nomadapi.PeriodicConfig{Spec: &schedule}})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
			var request nomadapi.JobRegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if !request.EnforceIndex || request.JobModifyIndex != modifyIndex {
				t.Fatalf("CAS request = enforce=%t modifyIndex=%d", request.EnforceIndex, request.JobModifyIndex)
			}
			if request.Job == nil || request.Job.Stop == nil || !*request.Job.Stop {
				t.Fatalf("pause request did not set job Stop: %#v", request.Job)
			}
			if got := request.Job.Meta[cronPauseEffectMetaKey]; got != "effect-1" {
				t.Fatalf("pause effect marker = %q", got)
			}
			// Another writer changed the parent after our read. Nomad must reject
			// the guarded registration rather than pausing that replacement.
			wrote = true
			http.Error(w, nomadapi.RegisterEnforceIndexErrPrefix+": job changed", http.StatusConflict)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))

	err := client.PausePeriodicJob(jobID, modifyIndex, "effect-1")
	if !errors.Is(err, ErrJobRevisionChanged) {
		t.Fatalf("PausePeriodicJob() error = %v, want revision conflict", err)
	}
	if !wrote {
		t.Fatal("expected guarded registration attempt")
	}
}

func TestResumePeriodicJobUsesGuardedRegistrationAndEffectMarker(t *testing.T) {
	jobID, status, schedule := "widget-nightly", "dead", "0 2 * * *"
	modifyIndex := uint64(42)
	stopped := true
	var wrote bool
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "2.0.7"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/widget-nightly":
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Status: &status, Stop: &stopped, JobModifyIndex: &modifyIndex, Periodic: &nomadapi.PeriodicConfig{Spec: &schedule}})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
			var request nomadapi.JobRegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if !request.EnforceIndex || request.JobModifyIndex != modifyIndex || request.Job == nil || request.Job.Stop == nil || *request.Job.Stop || request.Job.Meta[cronResumeEffectMetaKey] != "resume-effect" {
				t.Fatalf("resume CAS request = %+v", request)
			}
			wrote = true
			_ = json.NewEncoder(w).Encode(&nomadapi.JobRegisterResponse{})
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	if err := client.ResumePeriodicJob(jobID, modifyIndex, "resume-effect"); err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Fatal("expected guarded Nomad registration")
	}
}

func TestResumePeriodicJobRejectsRevisionConflict(t *testing.T) {
	jobID, status, schedule := "widget-nightly", "dead", "0 2 * * *"
	modifyIndex := uint64(42)
	stopped := true
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "2.0.7"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/widget-nightly":
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Status: &status, Stop: &stopped, JobModifyIndex: &modifyIndex, Periodic: &nomadapi.PeriodicConfig{Spec: &schedule}})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
			http.Error(w, nomadapi.RegisterEnforceIndexErrPrefix+": job changed", http.StatusConflict)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	if err := client.ResumePeriodicJob(jobID, modifyIndex, "resume-effect"); !errors.Is(err, ErrJobRevisionChanged) {
		t.Fatalf("ResumePeriodicJob() = %v, want revision conflict", err)
	}
}

func TestResumePeriodicJobWithReplacementPreservesTranslatedJobAndCAS(t *testing.T) {
	jobID, schedule, status := "widget-nightly", "15 3 * * *", "dead"
	index := uint64(42)
	stopped := true
	image := "registry.example/widget:new"
	replacement := &nomadapi.Job{ID: &jobID, Periodic: &nomadapi.PeriodicConfig{Spec: &schedule}, Meta: map[string]string{"delivery-revision": "8"}, TaskGroups: []*nomadapi.TaskGroup{{Name: &jobID, Tasks: []*nomadapi.Task{{Name: "widget", Driver: "docker", Config: map[string]interface{}{"image": image}, Env: map[string]string{"API_TOKEN": "secret-value"}}}}}}
	var wrote bool
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "2.0.7"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/widget-nightly":
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Status: &status, Stop: &stopped, JobModifyIndex: &index, Periodic: &nomadapi.PeriodicConfig{Spec: &schedule}})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
			var request nomadapi.JobRegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if !request.EnforceIndex || request.JobModifyIndex != index || request.Job == nil || request.Job.Stop == nil || *request.Job.Stop || request.Job.Meta[cronResumeEffectMetaKey] != "resume-effect" || request.Job.Meta["delivery-revision"] != "8" || request.Job.TaskGroups[0].Tasks[0].Env["API_TOKEN"] != "secret-value" || request.Job.TaskGroups[0].Tasks[0].Config["image"] != image {
				t.Fatalf("guarded replacement did not preserve translated job: %+v", request)
			}
			wrote = true
			_ = json.NewEncoder(w).Encode(&nomadapi.JobRegisterResponse{})
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	if err := client.ResumePeriodicJobWithReplacement(jobID, index, "resume-effect", replacement); err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Fatal("expected guarded registration")
	}
	if replacement.Stop != nil || replacement.Meta[cronResumeEffectMetaKey] != "" {
		t.Fatal("caller-owned replacement was modified")
	}
}

func TestUpdatePeriodicJobSchedulePreservesPauseAndUsesEffectMarker(t *testing.T) {
	jobID, oldSchedule, newSchedule, status := "widget-nightly", "0 2 * * *", "15 2 * * *", "dead"
	index := uint64(42)
	stopped := true
	replacement := &nomadapi.Job{ID: &jobID, Periodic: &nomadapi.PeriodicConfig{Spec: &newSchedule}, Meta: map[string]string{"delivery-revision": "8"}}
	var wrote bool
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "2.0.7"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/widget-nightly":
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Status: &status, Stop: &stopped, JobModifyIndex: &index, Periodic: &nomadapi.PeriodicConfig{Spec: &oldSchedule}})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
			var request nomadapi.JobRegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if !request.EnforceIndex || request.JobModifyIndex != index || request.Job == nil || request.Job.Stop == nil || !*request.Job.Stop || request.Job.Periodic == nil || request.Job.Periodic.Spec == nil || *request.Job.Periodic.Spec != newSchedule || request.Job.Meta[cronScheduleEffectMetaKey] != "schedule-effect" || request.Job.Meta["delivery-revision"] != "8" {
				t.Fatalf("guarded schedule replacement did not preserve intent: %+v", request)
			}
			wrote = true
			_ = json.NewEncoder(w).Encode(&nomadapi.JobRegisterResponse{})
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	if err := client.UpdatePeriodicJobSchedule(jobID, index, "schedule-effect", replacement); err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Fatal("expected guarded registration")
	}
	if replacement.Stop != nil || replacement.Meta[cronScheduleEffectMetaKey] != "" {
		t.Fatal("caller-owned replacement was modified")
	}
}

func TestResumePeriodicJobWithReplacementRejectsStaleRevision(t *testing.T) {
	jobID, schedule, status := "widget-nightly", "15 3 * * *", "dead"
	index := uint64(43)
	stopped := true
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "2.0.7"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/widget-nightly":
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Status: &status, Stop: &stopped, JobModifyIndex: &index, Periodic: &nomadapi.PeriodicConfig{Spec: &schedule}})
		default:
			t.Fatalf("unexpected mutation %s %s", r.Method, r.URL.Path)
		}
	}))
	err := client.ResumePeriodicJobWithReplacement(jobID, 42, "resume-effect", &nomadapi.Job{ID: &jobID, Periodic: &nomadapi.PeriodicConfig{Spec: &schedule}})
	if !errors.Is(err, ErrJobRevisionChanged) {
		t.Fatalf("got %v, want stale revision", err)
	}
}

func TestResumePeriodicJobWithReplacementRejectsConcurrentNomadWrite(t *testing.T) {
	jobID, schedule, status := "widget-nightly", "15 3 * * *", "dead"
	index := uint64(42)
	stopped := true
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent/self":
			_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "2.0.7"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/widget-nightly":
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &jobID, Status: &status, Stop: &stopped, JobModifyIndex: &index, Periodic: &nomadapi.PeriodicConfig{Spec: &schedule}})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs":
			var request nomadapi.JobRegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if !request.EnforceIndex || request.JobModifyIndex != index {
				t.Fatalf("unguarded registration: %+v", request)
			}
			http.Error(w, nomadapi.RegisterEnforceIndexErrPrefix+": job changed", http.StatusConflict)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	err := client.ResumePeriodicJobWithReplacement(jobID, index, "resume-effect", &nomadapi.Job{ID: &jobID, Periodic: &nomadapi.PeriodicConfig{Spec: &schedule}})
	if !errors.Is(err, ErrJobRevisionChanged) {
		t.Fatalf("got %v, want concurrent revision conflict", err)
	}
}

func TestPausePeriodicJobRejectsUnsupportedAtomicCASServer(t *testing.T) {
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/agent/self" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(&nomadapi.AgentSelf{Config: map[string]interface{}{"Version": map[string]interface{}{"Version": "1.9.7"}}})
	}))

	err := client.PausePeriodicJob("widget-nightly", 42, "effect-1")
	if !errors.Is(err, ErrAtomicJobCASUnsupported) {
		t.Fatalf("PausePeriodicJob() error = %v, want unsupported atomic CAS", err)
	}
}

func TestSupportsAtomicJobCAS(t *testing.T) {
	for version, want := range map[string]bool{
		"1.9.7":         false,
		"1.10.10":       false,
		"1.10.11":       true,
		"1.11.4":        false,
		"1.11.5":        true,
		"2.0.0":         false,
		"2.0.1":         true,
		"v2.1.0":        true,
		"1.10.11-beta1": false,
		"2.0.1-rc1":     false,
		"unknown":       false,
	} {
		if got := supportsAtomicJobCAS(version); got != want {
			t.Errorf("supportsAtomicJobCAS(%q) = %t, want %t", version, got, want)
		}
	}
}

func TestRestartJobStopsOnlyActiveDesiredAllocations(t *testing.T) {
	t.Parallel()

	var stopped []string
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/like-trove/allocations":
			if got := r.URL.Query().Get("all"); got != "false" {
				t.Fatalf("all query = %q, want false", got)
			}
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{
				{ID: "running", Namespace: "apps", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning},
				{ID: "pending", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusPending},
				{ID: "unknown", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusUnknown},
				{ID: "complete", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusComplete},
				{ID: "stopping", DesiredStatus: nomadapi.AllocDesiredStatusStop, ClientStatus: nomadapi.AllocClientStatusRunning},
			})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/allocation/") && strings.HasSuffix(r.URL.Path, "/stop"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/allocation/"), "/stop")
			if id == "running" && r.URL.Query().Get("namespace") != "apps" {
				t.Fatalf("namespace query = %q, want apps", r.URL.Query().Get("namespace"))
			}
			stopped = append(stopped, id)
			_ = json.NewEncoder(w).Encode(nomadapi.AllocStopResponse{EvalID: "eval-" + id})
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))

	if err := client.RestartJob("like-trove"); err != nil {
		t.Fatalf("RestartJob() error = %v", err)
	}
	if want := []string{"running", "pending", "unknown"}; !reflect.DeepEqual(stopped, want) {
		t.Fatalf("stopped allocations = %v, want %v", stopped, want)
	}
}

func TestScaleStatusRequiresExactSuccessfulDurableOperationEvent(t *testing.T) {
	t.Parallel()
	const generation = "9007199254740993"
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/job/widget/scale" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("region"); got != "global" {
			t.Fatalf("Nomad region=%q, want global", got)
		}
		response := nomadapi.JobScaleStatusResponse{TaskGroups: map[string]nomadapi.TaskGroupScaleStatus{
			"web": {Desired: 3, Events: []nomadapi.ScalingEvent{
				{Meta: map[string]interface{}{"norn.operationId": "operation-1", "norn.claimGeneration": "1", "norn.executionId": "wrong-generation"}, Count: int64Pointer(3), EvalID: stringPointer("eval-wrong-generation")},
				{Meta: map[string]interface{}{"norn.operationId": "operation-1", "norn.claimGeneration": "2", "norn.executionId": "wrong-execution"}, Count: int64Pointer(3), EvalID: stringPointer("eval-wrong-execution")},
				{Meta: map[string]interface{}{"norn.operationId": "operation-1", "norn.claimGeneration": generation, "norn.executionId": "execution-1"}, Count: int64Pointer(3), Error: true, EvalID: stringPointer("eval-error")},
				{Meta: map[string]interface{}{"norn.operationId": "operation-1", "norn.claimGeneration": generation, "norn.executionId": "execution-1"}, Count: int64Pointer(2), EvalID: stringPointer("eval-wrong-count")},
				{Meta: map[string]interface{}{"norn.operationId": "operation-1", "norn.claimGeneration": float64(9007199254740992), "norn.executionId": "execution-1"}, Count: int64Pointer(3), EvalID: stringPointer("eval-lossy-number")},
				{Meta: map[string]interface{}{"norn.operationId": "operation-1", "norn.claimGeneration": generation, "norn.executionId": "execution-1"}, Count: int64Pointer(3), EvalID: stringPointer("eval-1")},
			}},
		}}
		_ = json.NewEncoder(w).Encode(response)
	}))
	desired, matched, evalID, err := client.ScaleStatus("widget", "web", "global", "operation-1", generation, "execution-1", 3, "eval-1")
	if err != nil || desired != 3 || !matched || evalID != "eval-1" {
		t.Fatalf("ScaleStatus() = %d, %t, %q, %v", desired, matched, evalID, err)
	}
}

func TestExactCanaryPromotionNeverResolvesLatestDeployment(t *testing.T) {
	t.Parallel()
	var promoted string
	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/deployment/promote/deployment-accepted":
			if got := r.URL.Query().Get("region"); got != "global" {
				t.Fatalf("Nomad region=%q, want global", got)
			}
			promoted = "deployment-accepted"
			_ = json.NewEncoder(w).Encode(map[string]string{"EvalID": "eval-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/deployment/deployment-accepted":
			if got := r.URL.Query().Get("region"); got != "global" {
				t.Fatalf("Nomad region=%q, want global", got)
			}
			_ = json.NewEncoder(w).Encode(&nomadapi.Deployment{ID: "deployment-accepted", JobID: "widgets", Status: "successful"})
		default:
			http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	if err := client.PromoteDeploymentIDRegion("deployment-accepted", "global"); err != nil {
		t.Fatalf("PromoteDeploymentIDRegion() = %v", err)
	}
	if promoted != "deployment-accepted" {
		t.Fatalf("promoted = %q", promoted)
	}
	info, err := client.DeploymentByIDRegion("deployment-accepted", "global")
	if err != nil || info == nil || info.ID != "deployment-accepted" || info.JobID != "widgets" || info.Status != "successful" {
		t.Fatalf("DeploymentByIDRegion() = %#v, %v", info, err)
	}
}

func TestDeploymentCanaryStateRequiresPromotedTaskGroups(t *testing.T) {
	groups := map[string]*nomadapi.DeploymentState{
		"web": {PlacedCanaries: []string{"alloc-1"}, Promoted: true},
		"api": {PlacedCanaries: []string{"alloc-2"}, Promoted: false},
	}
	if has, promoted := deploymentCanaryState(groups); !has || promoted {
		t.Fatalf("partially promoted canary = has %t promoted %t", has, promoted)
	}
	groups["api"].Promoted = true
	if has, promoted := deploymentCanaryState(groups); !has || !promoted {
		t.Fatalf("promoted canary = has %t promoted %t", has, promoted)
	}
}

func TestDeploymentCanaryReadyRequiresEveryPlacedAllocationHealthy(t *testing.T) {
	groups := map[string]*nomadapi.DeploymentState{
		"web": {DesiredCanaries: 2, PlacedCanaries: []string{"web-1"}, HealthyAllocs: 1},
		"api": {PlacedCanaries: []string{"api-1"}, HealthyAllocs: 0},
	}
	if deploymentCanaryReady(groups) {
		t.Fatal("underplaced canary accepted")
	}
	groups["web"].PlacedCanaries = append(groups["web"].PlacedCanaries, "web-2")
	groups["web"].HealthyAllocs = 2
	if deploymentCanaryReady(groups) {
		t.Fatal("unhealthy api canary accepted")
	}
	groups["api"].HealthyAllocs = 1
	if !deploymentCanaryReady(groups) {
		t.Fatal("healthy canaries refused")
	}
	groups["web"].Promoted = true
	if deploymentCanaryReady(groups) {
		t.Fatal("already promoted group accepted")
	}
}

func stringPointer(value string) *string { return &value }
func int64Pointer(value int64) *int64    { return &value }

func TestRestartJobRequiresActiveAllocation(t *testing.T) {
	t.Parallel()

	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/job/batch/allocations" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{
			{ID: "done", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusComplete},
		})
	}))

	err := client.RestartJob("batch")
	if err == nil || !strings.Contains(err.Error(), "no active allocations found") {
		t.Fatalf("RestartJob() error = %v, want no active allocations error", err)
	}
}

func TestRestartJobReportsAllocationStopFailure(t *testing.T) {
	t.Parallel()

	client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/api/allocations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{
				{ID: "broken", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/allocation/broken/stop":
			http.Error(w, "stop rejected", http.StatusInternalServerError)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))

	err := client.RestartJob("api")
	if err == nil || !strings.Contains(err.Error(), "stop allocation broken") {
		t.Fatalf("RestartJob() error = %v, want allocation stop error", err)
	}
}

func TestIsRestartableAllocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		alloc *nomadapi.AllocationListStub
		want  bool
	}{
		{name: "nil", alloc: nil},
		{name: "missing id", alloc: &nomadapi.AllocationListStub{DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning}},
		{name: "desired stop", alloc: &nomadapi.AllocationListStub{ID: "a", DesiredStatus: nomadapi.AllocDesiredStatusStop, ClientStatus: nomadapi.AllocClientStatusRunning}},
		{name: "failed", alloc: &nomadapi.AllocationListStub{ID: "a", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusFailed}},
		{name: "running", alloc: &nomadapi.AllocationListStub{ID: "a", DesiredStatus: nomadapi.AllocDesiredStatusRun, ClientStatus: nomadapi.AllocClientStatusRunning}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRestartableAllocation(tt.alloc); got != tt.want {
				t.Fatalf("isRestartableAllocation() = %t, want %t", got, tt.want)
			}
		})
	}
}

func newTestNomadClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := nomadapi.DefaultConfig()
	cfg.Address = server.URL
	apiClient, err := nomadapi.NewClient(cfg)
	if err != nil {
		t.Fatalf("new Nomad client: %v", err)
	}
	return &Client{api: apiClient}
}
