package githubapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestObserveApplyRunByNonceHashMatchesOnlyBoundRun(t *testing.T) {
	planID := "22222222-2222-4222-8222-222222222222"
	fleetEnvironment := "production/nyc3"
	nonce := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(nonce))
	nonceHash := hex.EncodeToString(sum[:])
	otherNonce := strings.Repeat("e", 64)
	headSHA := strings.Repeat("b", 40)
	approved := &Dispatch{RunID: 93, PlanRunID: 91, PlanSHA: strings.Repeat("c", 64), ApprovedHeadSHA: headSHA}

	titleFor := func(nonce string) string {
		return fmt.Sprintf(applyRunDisplayTitleFormat, fleetEnvironment, planID, nonce)
	}
	runJSON := func(id int64, attempt int, title string) string {
		return fmt.Sprintf(`{"id":%d,"html_url":"https://github.com/acme/norn-fleet/actions/runs/%d","event":"workflow_dispatch","head_sha":%q,"head_branch":"main","path":".github/workflows/apply.yml@main","name":"apply","display_title":%q,"run_attempt":%d,"status":"completed","conclusion":"success","actor":{"login":"norn[bot]","type":"Bot"}}`, id, id, headSHA, title, attempt)
	}

	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app" {
			fmt.Fprint(w, `{"slug":"norn"}`)
			return
		}
		if tokenResponse(w, r, map[string]string{"actions": "read"}) {
			return
		}
		switch r.URL.Path {
		case "/repos/acme/norn-fleet/actions/runs/93/attempts/2", "/repos/acme/norn-fleet/actions/runs/93":
			fmt.Fprint(w, runJSON(93, 2, titleFor(nonce)))
		case "/repos/acme/norn-fleet/actions/runs/94/attempts/2", "/repos/acme/norn-fleet/actions/runs/94":
			fmt.Fprint(w, runJSON(94, 2, titleFor(otherNonce)))
		default:
			http.NotFound(w, r)
		}
	}))

	observed, err := client.ObserveApplyRunByNonceHash(context.Background(), planID, fleetEnvironment, approved, nonceHash, 2)
	if err != nil || observed.RunID != 93 || observed.RunAttempt != 2 || observed.Status != "completed" || observed.Conclusion != "success" {
		t.Fatalf("bound run observation=%+v err=%v", observed, err)
	}

	// Run 94 carries a different nonce; its hash must not match nonceHash
	// even though it otherwise looks like a valid dispatch run.
	wrongRun := &Dispatch{RunID: 94, PlanRunID: 91, PlanSHA: approved.PlanSHA, ApprovedHeadSHA: headSHA}
	if _, err := client.ObserveApplyRunByNonceHash(context.Background(), planID, fleetEnvironment, wrongRun, nonceHash, 2); err == nil {
		t.Fatal("a run carrying a different nonce was accepted by its hash")
	}

	if _, err := client.ObserveApplyRunByNonceHash(context.Background(), planID, fleetEnvironment, approved, nonceHash, 3); err == nil {
		t.Fatal("a nonexistent run attempt was accepted")
	}
}

func TestReconcileCanceledBeforeExecutionRequiresStableZeroExecution(t *testing.T) {
	planID := "22222222-2222-4222-8222-222222222222"
	fleetEnvironment := "staging/nyc3"
	nonce := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(nonce))
	nonceHash := hex.EncodeToString(sum[:])
	headSHA := strings.Repeat("b", 40)
	approved := &Dispatch{PlanRunID: 91, PlanSHA: strings.Repeat("c", 64), ApprovedHeadSHA: headSHA}
	runID := int64(39032894911)
	run := fmt.Sprintf(`{"id":%d,"html_url":"https://github.com/acme/norn-fleet/actions/runs/%d","event":"workflow_dispatch","head_sha":%q,"head_branch":"main","path":".github/workflows/apply.yml@main","name":"apply","display_title":%q,"run_attempt":1,"status":"completed","conclusion":"cancelled","actor":{"login":"norn[bot]","type":"Bot"},"inputs":null}`, runID, runID, headSHA, fmt.Sprintf(applyRunDisplayTitleFormat, fleetEnvironment, planID, nonce))
	jobs := fmt.Sprintf(`{"total_count":2,"jobs":[{"id":6,"run_id":%d,"run_attempt":1,"name":"pilot_admission","status":"completed","conclusion":"success","runner_id":1000019941,"runner_name":"GitHub Actions 1000019941","labels":["ubuntu-24.04"],"started_at":"2026-10-17T12:32:01Z","steps":[{"number":1,"name":"Set up job","status":"completed","conclusion":"success"},{"number":2,"name":"Require selected admission runner","status":"completed","conclusion":"success"},{"number":3,"name":"Run set -euo pipefail","status":"completed","conclusion":"success"},{"number":4,"name":"Complete job","status":"completed","conclusion":"success"}]},{"id":7,"run_id":%d,"run_attempt":1,"name":"apply","status":"completed","conclusion":"cancelled","runner_id":0,"runner_name":"","labels":["self-hosted","Linux","X64","norn-fleet-protected","norn-work-staging-fleet-effect"],"started_at":"2026-10-17T12:32:04Z","steps":[]}]}`, runID, runID)
	artifacts := `{"total_count":0,"artifacts":[]}`
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app" {
			fmt.Fprint(w, `{"slug":"norn"}`)
			return
		}
		if tokenResponse(w, r, map[string]string{"actions": "read"}) {
			return
		}
		switch r.URL.Path {
		case fmt.Sprintf("/repos/acme/norn-fleet/actions/runs/%d", runID):
			fmt.Fprint(w, run)
		case fmt.Sprintf("/repos/acme/norn-fleet/actions/runs/%d/jobs", runID):
			fmt.Fprint(w, jobs)
		case fmt.Sprintf("/repos/acme/norn-fleet/actions/runs/%d/artifacts", runID):
			fmt.Fprint(w, artifacts)
		default:
			http.NotFound(w, r)
		}
	}))
	client.cfg.Environment = "staging"
	client.cfg.ConfigPath = "environments/staging/nyc3/cluster.yaml"
	var decoded applyRun
	if err := json.Unmarshal([]byte(run), &decoded); err != nil {
		t.Fatal(err)
	}
	if err := client.verifyApplyRunByNonceHash(&decoded, planID, fleetEnvironment, approved, nonceHash, "norn[bot]"); err != nil {
		t.Fatalf("fixture identity: %v", err)
	}
	result, err := client.ReconcileCanceledBeforeExecution(context.Background(), planID, fleetEnvironment, false, approved, nonceHash, runID, 1)
	if err != nil || result.Outcome != "verified-cancelled-before-execution" || result.CanceledRun == nil || result.CanceledRun.RunID != runID || result.CanceledRun.JobCount != 2 {
		t.Fatalf("reconciliation=%+v err=%v", result, err)
	}

	jobs = fmt.Sprintf(`{"total_count":2,"jobs":[{"id":6,"run_id":%d,"run_attempt":1,"name":"pilot_admission","status":"completed","conclusion":"success","runner_id":1000019941,"runner_name":"GitHub Actions 1000019941","labels":["ubuntu-24.04"],"started_at":"2026-10-17T12:32:01Z","steps":[{"number":1,"name":"Set up job","status":"completed","conclusion":"success"},{"number":2,"name":"Require selected admission runner","status":"completed","conclusion":"success"},{"number":3,"name":"Run set -euo pipefail","status":"completed","conclusion":"success"},{"number":4,"name":"Complete job","status":"completed","conclusion":"success"}]},{"id":7,"run_id":%d,"run_attempt":1,"name":"apply","status":"completed","conclusion":"cancelled","runner_id":99,"runner_name":"runner","labels":["self-hosted","Linux","X64","norn-fleet-protected","norn-work-staging-fleet-effect"],"started_at":"2026-10-17T12:32:04Z","steps":[]}]}`, runID, runID)
	if _, err := client.ReconcileCanceledBeforeExecution(context.Background(), planID, fleetEnvironment, false, approved, nonceHash, runID, 1); err == nil {
		t.Fatal("run whose job acquired a runner was accepted as unexecuted")
	}
}

func TestReconcileFailedRunnerSetupMatchesObservedPreCheckoutFailureOnly(t *testing.T) {
	const runID int64 = 39104495159
	planID := "dab170e6-5677-4187-abc7-c0bf195775a6"
	nonce := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(nonce))
	nonceHash := hex.EncodeToString(sum[:])
	headSHA := "602a3867eb590341218efb93295d7afe018a842a"
	approved := &Dispatch{PlanRunID: 91, PlanSHA: strings.Repeat("c", 64), ApprovedHeadSHA: headSHA}
	run := applyRun{ID: runID, HTMLURL: "https://github.com/acme/norn-fleet/actions/runs/39104495159", Event: "workflow_dispatch", HeadSHA: headSHA, HeadBranch: "main", Path: ".github/workflows/apply.yml", Name: "apply", DisplayTitle: fmt.Sprintf(applyRunDisplayTitleFormat, "staging/nyc3", planID, nonce), RunAttempt: 1, Status: "completed", Conclusion: "failure"}
	run.Actor.Login, run.Actor.Type = "norn[bot]", "Bot"
	admission := reconciliationJob{ID: 117607337776, RunID: runID, RunAttempt: 1, HeadSHA: headSHA, Name: "pilot_admission", Status: "completed", Conclusion: "success", StartedAt: "2026-10-20T12:00:00Z", CompletedAt: "2026-10-20T12:01:00Z", RunnerID: 1000020754, RunnerName: "GitHub Actions 1000020754", Labels: []string{"ubuntu-24.04"}, Steps: []reconciliationStep{{1, "Set up job", "completed", "success"}, {2, "Require selected admission runner", "completed", "success"}, {3, "Run set -euo pipefail", "completed", "success"}, {4, "Complete job", "completed", "success"}}}
	apply := reconciliationJob{ID: 117607354084, RunID: runID, RunAttempt: 1, HeadSHA: headSHA, Name: "apply", Status: "completed", Conclusion: "failure", StartedAt: "2026-10-20T12:02:00Z", CompletedAt: "2026-10-20T12:03:00Z", RunnerID: 260, RunnerName: "norn-work-staging-work39009200347-runner", Labels: []string{"self-hosted", "Linux", "X64", "norn-fleet-protected", "norn-work-staging-fleet-effect"}, Steps: failedRunnerSetupSteps()}
	jobs := canceledRunJobs{TotalCount: 2, Jobs: []reconciliationJob{admission, apply}}
	artifacts := canceledRunArtifacts{TotalCount: 1}
	artifacts.Artifacts = append(artifacts.Artifacts, struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}{12127871960, "fleet-apply-39104495159-1"})
	var reads int
	var changeBetweenSnapshots bool
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app" {
			fmt.Fprint(w, `{"slug":"norn"}`)
			return
		}
		if tokenResponse(w, r, map[string]string{"actions": "read"}) {
			return
		}
		switch r.URL.Path {
		case "/repos/acme/norn-fleet/actions/runs/39104495159":
			reads++
			if changeBetweenSnapshots && reads%2 == 0 {
				changed := run
				changed.RunAttempt = 2
				_ = json.NewEncoder(w).Encode(changed)
				return
			}
			_ = json.NewEncoder(w).Encode(run)
		case "/repos/acme/norn-fleet/actions/runs/39104495159/jobs":
			_ = json.NewEncoder(w).Encode(jobs)
		case "/repos/acme/norn-fleet/actions/runs/39104495159/artifacts":
			_ = json.NewEncoder(w).Encode(artifacts)
		default:
			http.NotFound(w, r)
		}
	}))
	client.cfg.Environment, client.cfg.ConfigPath = "staging", "environments/staging/nyc3/cluster.yaml"
	check := func(want bool) {
		t.Helper()
		result, err := client.ReconcileFailedRunnerSetup(context.Background(), planID, "staging/nyc3", false, approved, nonceHash, runID, 1)
		if (err == nil) != want {
			t.Fatalf("accepted=%t want=%t result=%+v err=%v", err == nil, want, result, err)
		}
		if want && (result.Outcome != "verified-failed-runner-setup-before-execution" || result.CanceledRun.ArtifactCount != 1) {
			t.Fatalf("result=%+v", result)
		}
	}
	check(true)
	if reads != 2 {
		t.Fatalf("expected independent snapshots, reads=%d", reads)
	}
	changeBetweenSnapshots = true
	check(false)
	changeBetweenSnapshots = false
	for name, mutate := range map[string]func(){
		"checkout reached":         func() { jobs.Jobs[1].Steps[5].Conclusion = "success" },
		"provider binding reached": func() { jobs.Jobs[1].Steps[6].Conclusion = "success" },
		"attempt reached":          func() { jobs.Jobs[1].Steps[35].Conclusion = "success" },
		"apply reached":            func() { jobs.Jobs[1].Steps[40].Conclusion = "success" },
		"setup succeeded":          func() { jobs.Jobs[1].Steps[1].Conclusion = "success" },
		"evidence changed":         func() { jobs.Jobs[1].Steps[45].Conclusion = "success" },
		"unexpected job":           func() { jobs.TotalCount = 3 },
		"artifact changed":         func() { artifacts.Artifacts[0].Name = "other" },
		"wrong head":               func() { run.HeadSHA = strings.Repeat("d", 40) },
		"rerun":                    func() { run.RunAttempt = 2 },
		"wrong nonce":              func() { run.DisplayTitle = strings.Replace(run.DisplayTitle, nonce, strings.Repeat("b", 64), 1) },
	} {
		t.Run(name, func(t *testing.T) {
			originalJobs, _ := json.Marshal(jobs)
			originalArtifacts, _ := json.Marshal(artifacts)
			originalRun := run
			mutate()
			check(false)
			_ = json.Unmarshal(originalJobs, &jobs)
			_ = json.Unmarshal(originalArtifacts, &artifacts)
			run = originalRun
		})
	}
}

func TestListPlanRunsBounded(t *testing.T) {
	planID := "66666666-6666-4666-8666-666666666666"
	otherPlanID := "77777777-7777-4777-8777-777777777777"
	const matching = planRunsSnapshotLimit + 5

	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(w, r, map[string]string{"actions": "read"}) {
			return
		}
		if !strings.Contains(r.URL.Path, "/actions/workflows/apply.yml/runs") {
			http.NotFound(w, r)
			return
		}
		var builder strings.Builder
		builder.WriteString(`{"workflow_runs":[`)
		for i := 0; i < matching; i++ {
			if i > 0 {
				builder.WriteString(",")
			}
			fmt.Fprintf(&builder, `{"id":%d,"html_url":"https://github.com/acme/norn-fleet/actions/runs/%d","display_title":"Apply production/nyc3 pilot  Norn plan %s nonce %s","status":"completed","conclusion":"success","run_attempt":1}`, 1000+i, 1000+i, planID, strings.Repeat("a", 64))
		}
		fmt.Fprintf(&builder, `,{"id":2000,"html_url":"https://github.com/acme/norn-fleet/actions/runs/2000","display_title":"Apply production/nyc3 pilot  Norn plan %s nonce %s","status":"completed","conclusion":"success","run_attempt":1}`, otherPlanID, strings.Repeat("b", 64))
		builder.WriteString(`]}`)
		fmt.Fprint(w, builder.String())
	}))

	runs, err := client.ListPlanRuns(context.Background(), planID)
	if err != nil {
		t.Fatalf("list plan runs: %v", err)
	}
	if len(runs) != planRunsSnapshotLimit {
		t.Fatalf("snapshot not bounded: got %d entries", len(runs))
	}
	for _, run := range runs {
		if !strings.Contains(run.DisplayTitle, planID) {
			t.Fatalf("snapshot included a foreign plan run: %+v", run)
		}
	}
}

// observeFake serves one run ID with independently configurable bodies for
// the exact-attempt and latest-run endpoints so tests can model reruns,
// malformed payloads and GitHub error statuses.
type observeFake struct {
	attemptStatus, latestStatus, jobsStatus                int
	attemptBody, latestBody, latestAfterJobsBody, jobsBody string
}

func recoverJobsFixture(runID int64, attempt int, headSHA string) string {
	return fmt.Sprintf(`{"total_count":2,"jobs":[{"id":11,"run_id":%d,"run_attempt":%d,"head_sha":%q,"name":"recovery_binding","status":"completed","conclusion":"success","steps":[{"number":1,"name":"Verify immutable recovery binding","status":"completed","conclusion":"success"}]},{"id":12,"run_id":%d,"run_attempt":%d,"head_sha":%q,"name":"recover","status":"completed","conclusion":"failure"}]}`, runID, attempt, headSHA, runID, attempt, headSHA)
}

func (f *observeFake) client(t *testing.T, runID int64, attempt int) *Client {
	t.Helper()
	attemptPath := fmt.Sprintf("/repos/acme/norn-fleet/actions/runs/%d/attempts/%d", runID, attempt)
	latestPath := fmt.Sprintf("/repos/acme/norn-fleet/actions/runs/%d", runID)
	jobsPath := fmt.Sprintf("/repos/acme/norn-fleet/actions/runs/%d/attempts/%d/jobs", runID, attempt)
	jobsRead := false
	return testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app" {
			fmt.Fprint(w, `{"slug":"norn"}`)
			return
		}
		if tokenResponse(w, r, map[string]string{"actions": "read"}) {
			return
		}
		write := func(status int, body string) {
			if status != 0 {
				w.WriteHeader(status)
			}
			fmt.Fprint(w, body)
		}
		switch r.URL.Path {
		case attemptPath:
			write(f.attemptStatus, f.attemptBody)
		case latestPath:
			body := f.latestBody
			if jobsRead && f.latestAfterJobsBody != "" {
				body = f.latestAfterJobsBody
			}
			write(f.latestStatus, body)
		case jobsPath:
			jobsRead = true
			body := f.jobsBody
			if body == "" {
				body = recoverJobsFixture(runID, attempt, strings.Repeat("d", 40))
			}
			write(f.jobsStatus, body)
		default:
			http.NotFound(w, r)
		}
	}))
}

type observeRunFields struct {
	id                                       int64
	attempt                                  int
	event, branch, headSHA, path, title      string
	status, conclusion, actorLogin, actorTyp string
	inputs                                   string
}

func (r observeRunFields) json() string {
	conclusion := "null"
	if r.conclusion != "" {
		conclusion = fmt.Sprintf("%q", r.conclusion)
	}
	inputs := ""
	if r.inputs != "" {
		inputs = `,"inputs":` + r.inputs
	}
	return fmt.Sprintf(`{"id":%d,"html_url":"https://github.com/acme/norn-fleet/actions/runs/%d","event":%q,"head_sha":%q,"head_branch":%q,"path":%q,"name":"apply","display_title":%q,"run_attempt":%d,"status":%q,"conclusion":%s,"actor":{"login":%q,"type":%q}%s}`, r.id, r.id, r.event, r.headSHA, r.branch, r.path, r.title, r.attempt, r.status, conclusion, r.actorLogin, r.actorTyp, inputs)
}

func TestObserveApplyRunByNonceHashFailsClosed(t *testing.T) {
	planID := "22222222-2222-4222-8222-222222222222"
	fleetEnvironment := "production/nyc3"
	nonce := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(nonce))
	nonceHash := hex.EncodeToString(sum[:])
	headSHA := strings.Repeat("b", 40)
	approved := &Dispatch{RunID: 93, PlanRunID: 91, PlanSHA: strings.Repeat("c", 64), ApprovedHeadSHA: headSHA}
	base := observeRunFields{id: 93, attempt: 2, event: "workflow_dispatch", branch: "main", headSHA: headSHA, path: ".github/workflows/apply.yml@main", title: fmt.Sprintf(applyRunDisplayTitleFormat, fleetEnvironment, planID, nonce), status: "completed", conclusion: "success", actorLogin: "norn[bot]", actorTyp: "Bot"}
	with := func(edit func(*observeRunFields)) string {
		run := base
		edit(&run)
		return run.json()
	}
	ok := base.json()

	cases := map[string]observeFake{
		"rerun between reads":        {attemptBody: ok, latestBody: with(func(r *observeRunFields) { r.attempt, r.status, r.conclusion = 3, "in_progress", "" })},
		"latest disagrees on status": {attemptBody: ok, latestBody: with(func(r *observeRunFields) { r.status, r.conclusion = "in_progress", "" })},
		"completed with null":        {attemptBody: with(func(r *observeRunFields) { r.conclusion = "" }), latestBody: with(func(r *observeRunFields) { r.conclusion = "" })},
		"attempt number mismatch":    {attemptBody: with(func(r *observeRunFields) { r.attempt = 1 }), latestBody: ok},
		"human actor":                {attemptBody: with(func(r *observeRunFields) { r.actorLogin, r.actorTyp = "mallory", "User" }), latestBody: ok},
		"foreign plan in title": {attemptBody: with(func(r *observeRunFields) {
			r.title = fmt.Sprintf(applyRunDisplayTitleFormat, fleetEnvironment, "33333333-3333-4333-8333-333333333333", nonce)
		}), latestBody: ok},
		"foreign environment in title": {attemptBody: with(func(r *observeRunFields) {
			r.title = fmt.Sprintf(applyRunDisplayTitleFormat, "staging/nyc3", planID, nonce)
		}), latestBody: ok},
		"different head commit":        {attemptBody: with(func(r *observeRunFields) { r.headSHA = strings.Repeat("f", 40) }), latestBody: ok},
		"different run id in body":     {attemptBody: with(func(r *observeRunFields) { r.id = 94 }), latestBody: ok},
		"attempt 404":                  {attemptStatus: http.StatusNotFound, attemptBody: `{"message":"Not Found"}`, latestBody: ok},
		"attempt 403":                  {attemptStatus: http.StatusForbidden, attemptBody: `{"message":"Resource not accessible"}`, latestBody: ok},
		"latest rate limited":          {attemptBody: ok, latestStatus: http.StatusTooManyRequests, latestBody: `{"message":"API rate limit exceeded"}`},
		"latest 500":                   {attemptBody: ok, latestStatus: http.StatusInternalServerError, latestBody: ``},
		"malformed attempt json":       {attemptBody: `{"id":93,"status":`, latestBody: ok},
		"malformed latest json":        {attemptBody: ok, latestBody: `not json`},
		"unknown completed conclusion": {attemptBody: with(func(r *observeRunFields) { r.conclusion = "mystery" }), latestBody: with(func(r *observeRunFields) { r.conclusion = "mystery" })},
		"queued with stray conclusion": {attemptBody: with(func(r *observeRunFields) { r.status = "queued" }), latestBody: with(func(r *observeRunFields) { r.status = "queued" })},
	}
	for name, fake := range cases {
		t.Run(name, func(t *testing.T) {
			fake := fake
			client := fake.client(t, 93, 2)
			if observed, err := client.ObserveApplyRunByNonceHash(context.Background(), planID, fleetEnvironment, approved, nonceHash, 2); err == nil {
				t.Fatalf("observation accepted: %+v", observed)
			}
		})
	}

	fake := observeFake{attemptBody: ok, latestBody: ok}
	client := fake.client(t, 93, 2)
	if _, err := client.ObserveApplyRunByNonceHash(context.Background(), planID, fleetEnvironment, approved, strings.ToUpper(nonceHash), 2); err == nil {
		t.Fatal("a non-canonical nonce hash was accepted")
	}
	if _, err := client.ObserveApplyRunByNonceHash(context.Background(), planID, "staging/nyc3", approved, nonceHash, 2); err == nil {
		t.Fatal("a foreign fleet environment was accepted")
	}
	if observed, err := client.ObserveApplyRunByNonceHash(context.Background(), planID, fleetEnvironment, approved, nonceHash, 2); err != nil || observed.Status != "completed" {
		t.Fatalf("control observation failed: observed=%+v err=%v", observed, err)
	}
}

func TestListPlanRunsExactTokenAndErrors(t *testing.T) {
	planID := "66666666-6666-4666-8666-666666666666"
	status := 0
	body := fmt.Sprintf(`{"workflow_runs":[{"id":1,"html_url":"https://github.com/acme/norn-fleet/actions/runs/1","display_title":"Apply production/nyc3 pilot  Norn plan %s nonce %s","status":"completed","conclusion":"success","run_attempt":1},{"id":2,"html_url":"https://github.com/acme/norn-fleet/actions/runs/2","display_title":"Apply production/nyc3 pilot  Norn plan %s0 nonce %s","status":"in_progress","conclusion":null,"run_attempt":1}]}`, planID, strings.Repeat("a", 64), planID, strings.Repeat("b", 64))
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(w, r, map[string]string{"actions": "read"}) {
			return
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		fmt.Fprint(w, body)
	}))
	runs, err := client.ListPlanRuns(context.Background(), planID)
	if err != nil || len(runs) != 1 || runs[0].RunID != 1 {
		t.Fatalf("exact-token listing runs=%+v err=%v", runs, err)
	}
	for _, failure := range []struct {
		status int
		body   string
	}{{http.StatusForbidden, `{"message":"Forbidden"}`}, {http.StatusNotFound, `{"message":"Not Found"}`}, {http.StatusTooManyRequests, ``}, {0, `{"workflow_runs":`}} {
		status, body = failure.status, failure.body
		if runs, err := client.ListPlanRuns(context.Background(), planID); err == nil || runs != nil {
			t.Fatalf("listing failure %d %q returned runs=%+v err=%v", failure.status, failure.body, runs, err)
		}
	}
	if _, err := client.ListPlanRuns(context.Background(), "not-a-plan"); err == nil {
		t.Fatal("invalid plan ID was accepted")
	}
}

func recoverRunFixture() (observeRunFields, int64, string, string) {
	const applyRunID = 93
	nonce := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(nonce))
	recordedSHA := strings.Repeat("d", 40)
	run := observeRunFields{id: 155, attempt: 1, event: "workflow_dispatch", branch: "main", headSHA: recordedSHA, path: ".github/workflows/recover.yml@main", title: fmt.Sprintf(recoverRunDisplayTitleFormat, applyRunID), status: "completed", conclusion: "failure", actorLogin: "operator", actorTyp: "User", inputs: fmt.Sprintf(`{"apply_run_id":"%d","dispatch_nonce":%q,"apply_run_attempt":"1"}`, applyRunID, nonce)}
	return run, applyRunID, hex.EncodeToString(sum[:]), recordedSHA
}

func TestObserveRecoverRunReportsInProgress(t *testing.T) {
	base, applyRunID, nonceHash, recordedSHA := recoverRunFixture()
	base.status, base.conclusion = "in_progress", ""
	fake := observeFake{attemptBody: base.json(), latestBody: base.json()}
	observed, err := fake.client(t, 155, 1).ObserveRecoverRun(context.Background(), applyRunID, nonceHash, 155, 1, recordedSHA)
	if err != nil || observed.RunID != 155 || observed.RunAttempt != 1 || observed.Status != "in_progress" || observed.Conclusion != "" {
		t.Fatalf("in-progress recover run misreported: observed=%+v err=%v", observed, err)
	}
	base.status = "unrecognized"
	fake = observeFake{attemptBody: base.json(), latestBody: base.json()}
	if _, err := fake.client(t, 155, 1).ObserveRecoverRun(context.Background(), applyRunID, nonceHash, 155, 1, recordedSHA); err == nil {
		t.Fatal("unrecognized recover run status was accepted")
	}
}

func TestObserveRecoverRunAcceptsBothTriggersWithoutActorRestriction(t *testing.T) {
	base, applyRunID, nonceHash, recordedSHA := recoverRunFixture()
	automatic := base
	automatic.event, automatic.inputs, automatic.actorLogin = "workflow_run", "", "someone-who-ran-apply"
	bot := base
	bot.actorLogin, bot.actorTyp = "norn[bot]", "Bot"
	withoutRESTInputs := base
	withoutRESTInputs.inputs = "" // GitHub omits workflow_dispatch inputs on its run REST response.
	for name, run := range map[string]observeRunFields{"manual by human": base, "manual by bot": bot, "manual without REST inputs": withoutRESTInputs, "workflow_run": automatic} {
		fake := observeFake{attemptBody: run.json(), latestBody: run.json()}
		observed, err := fake.client(t, 155, 1).ObserveRecoverRun(context.Background(), applyRunID, nonceHash, 155, 1, recordedSHA)
		if err != nil || observed.Status != "completed" || observed.Conclusion != "failure" {
			t.Fatalf("%s: legitimate recover run rejected: observed=%+v err=%v", name, observed, err)
		}
	}
}

func TestObserveRecoverRunRejectsForeignWorkflowOrPlan(t *testing.T) {
	base, applyRunID, nonceHash, recordedSHA := recoverRunFixture()
	with := func(edit func(*observeRunFields)) string {
		run := base
		edit(&run)
		return run.json()
	}
	ok := base.json()
	both := func(body string) observeFake { return observeFake{attemptBody: body, latestBody: body} }
	otherNonce := strings.Repeat("e", 64)

	cases := map[string]observeFake{
		"foreign apply_run_id input": both(with(func(r *observeRunFields) {
			r.inputs = fmt.Sprintf(`{"apply_run_id":"94","dispatch_nonce":%q}`, strings.Repeat("a", 64))
		})),
		"foreign apply run in title": both(with(func(r *observeRunFields) { r.title = fmt.Sprintf(recoverRunDisplayTitleFormat, 94) })),
		"workflow_run for foreign apply": both(with(func(r *observeRunFields) {
			r.event, r.inputs, r.title = "workflow_run", "", fmt.Sprintf(recoverRunDisplayTitleFormat, 94)
		})),
		"title id with suffix": both(with(func(r *observeRunFields) { r.title = fmt.Sprintf(recoverRunDisplayTitleFormat, applyRunID) + "0" })),
		"wrong nonce": both(with(func(r *observeRunFields) {
			r.inputs = fmt.Sprintf(`{"apply_run_id":"%d","dispatch_nonce":%q}`, applyRunID, otherNonce)
		})),
		"missing nonce input":           both(with(func(r *observeRunFields) { r.inputs = fmt.Sprintf(`{"apply_run_id":"%d"}`, applyRunID) })),
		"apply workflow path":           both(with(func(r *observeRunFields) { r.path = ".github/workflows/apply.yml@main" })),
		"foreign workflow path":         both(with(func(r *observeRunFields) { r.path = ".github/workflows/other.yml@main" })),
		"wrong head sha":                both(with(func(r *observeRunFields) { r.headSHA = strings.Repeat("b", 40) })),
		"non-default branch":            both(with(func(r *observeRunFields) { r.branch, r.path = "feature", ".github/workflows/recover.yml@feature" })),
		"non-default head branch only":  both(with(func(r *observeRunFields) { r.branch = "feature" })),
		"push event":                    both(with(func(r *observeRunFields) { r.event = "push" })),
		"foreign repository url":        both(strings.Replace(ok, "acme/norn-fleet", "evil/norn-fleet", 1)),
		"completed with null":           both(with(func(r *observeRunFields) { r.conclusion = "" })),
		"queued with stray conclusion":  both(with(func(r *observeRunFields) { r.status = "queued" })),
		"attempt number mismatch":       {attemptBody: with(func(r *observeRunFields) { r.attempt = 2 }), latestBody: ok},
		"different run id in body":      both(with(func(r *observeRunFields) { r.id = 156 })),
		"rerun between reads":           {attemptBody: ok, latestBody: with(func(r *observeRunFields) { r.attempt, r.status, r.conclusion = 2, "queued", "" })},
		"rerun after binding job read":  {attemptBody: ok, latestBody: ok, latestAfterJobsBody: with(func(r *observeRunFields) { r.attempt, r.status, r.conclusion = 2, "queued", "" })},
		"latest disagrees":              {attemptBody: ok, latestBody: with(func(r *observeRunFields) { r.conclusion = "success" })},
		"latest head disagrees":         {attemptBody: ok, latestBody: with(func(r *observeRunFields) { r.headSHA = strings.Repeat("b", 40) })},
		"latest bound to foreign apply": {attemptBody: ok, latestBody: with(func(r *observeRunFields) { r.title = fmt.Sprintf(recoverRunDisplayTitleFormat, 94) })},
		"attempt 404":                   {attemptStatus: http.StatusNotFound, attemptBody: `{"message":"Not Found"}`, latestBody: ok},
		"latest 403":                    {attemptBody: ok, latestStatus: http.StatusForbidden, latestBody: `{"message":"Forbidden"}`},
		"attempt rate limited":          {attemptStatus: http.StatusTooManyRequests, attemptBody: ``, latestBody: ok},
		"malformed attempt json":        {attemptBody: `[`, latestBody: ok},
		"malformed latest json":         {attemptBody: ok, latestBody: `{"id":`},
		"jobs 403":                      {attemptBody: ok, latestBody: ok, jobsStatus: http.StatusForbidden, jobsBody: `{"message":"Forbidden"}`},
		"jobs malformed":                {attemptBody: ok, latestBody: ok, jobsBody: `{"total_count":`},
		"jobs missing binding":          {attemptBody: ok, latestBody: ok, jobsBody: strings.Replace(recoverJobsFixture(155, 1, recordedSHA), "recovery_binding", "other_job", 1)},
		"jobs duplicate binding":        {attemptBody: ok, latestBody: ok, jobsBody: strings.Replace(recoverJobsFixture(155, 1, recordedSHA), `"name":"recover"`, `"name":"recovery_binding"`, 1)},
		"jobs wrong run":                {attemptBody: ok, latestBody: ok, jobsBody: strings.Replace(recoverJobsFixture(155, 1, recordedSHA), `"run_id":155`, `"run_id":156`, 1)},
		"jobs wrong attempt":            {attemptBody: ok, latestBody: ok, jobsBody: strings.Replace(recoverJobsFixture(155, 1, recordedSHA), `"run_attempt":1`, `"run_attempt":2`, 1)},
		"jobs wrong head":               {attemptBody: ok, latestBody: ok, jobsBody: strings.Replace(recoverJobsFixture(155, 1, recordedSHA), recordedSHA, strings.Repeat("b", 40), 1)},
		"binding job failed":            {attemptBody: ok, latestBody: ok, jobsBody: strings.Replace(recoverJobsFixture(155, 1, recordedSHA), `"conclusion":"success"`, `"conclusion":"failure"`, 1)},
		"binding step skipped":          {attemptBody: ok, latestBody: ok, jobsBody: strings.Replace(recoverJobsFixture(155, 1, recordedSHA), `"name":"Verify immutable recovery binding","status":"completed","conclusion":"success"`, `"name":"Verify immutable recovery binding","status":"completed","conclusion":"skipped"`, 1)},
		"binding step absent":           {attemptBody: ok, latestBody: ok, jobsBody: strings.Replace(recoverJobsFixture(155, 1, recordedSHA), `"Verify immutable recovery binding"`, `"Unrelated step"`, 1)},
		"jobs listing truncated":        {attemptBody: ok, latestBody: ok, jobsBody: strings.Replace(recoverJobsFixture(155, 1, recordedSHA), `"total_count":2`, `"total_count":101`, 1)},
	}
	for name, fake := range cases {
		t.Run(name, func(t *testing.T) {
			fake := fake
			if observed, err := fake.client(t, 155, 1).ObserveRecoverRun(context.Background(), applyRunID, nonceHash, 155, 1, recordedSHA); err == nil {
				t.Fatalf("observation accepted: %+v", observed)
			}
		})
	}

	fake := both(ok)
	client := fake.client(t, 155, 1)
	for name, call := range map[string]func() error{
		"recover run is the apply run": func() error {
			_, err := client.ObserveRecoverRun(context.Background(), 155, nonceHash, 155, 1, recordedSHA)
			return err
		},
		"non-canonical nonce hash": func() error {
			_, err := client.ObserveRecoverRun(context.Background(), applyRunID, strings.ToUpper(nonceHash), 155, 1, recordedSHA)
			return err
		},
		"invalid recorded sha": func() error {
			_, err := client.ObserveRecoverRun(context.Background(), applyRunID, nonceHash, 155, 1, "main")
			return err
		},
	} {
		if call() == nil {
			t.Fatalf("%s: invalid binding was accepted", name)
		}
	}
}
