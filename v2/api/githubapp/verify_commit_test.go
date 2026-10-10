package githubapp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/executor"
)

var (
	tipSHA  = strings.Repeat("b", 40)
	baseSHA = strings.Repeat("a", 40)
)

type fakeRun struct {
	sha, event, branch, status, conclusion string
}

type fakeGitHub struct {
	protected   bool
	tip         string
	noWorkflow  bool
	runs        []fakeRun
	status      string
	aheadBy     int
	files       []string // compare files, served 100 per page
	compareHits int
	etag        string
	runRepo     string    // head_repository of every run; default acme/norn-fleet
	runPath     string    // path of every run; default .github/workflows/validate.yml
	committed   time.Time // tip committer date; default testNow
}

// testNow is testClient's fixed clock.
var testNow = time.Unix(1_800_000_000, 0).UTC()

func (f *fakeGitHub) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(w, r, map[string]string{"actions": "read", "contents": "read"}) {
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/branches/main"):
			if f.etag != "" && r.Header.Get("If-None-Match") == f.etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			if f.etag != "" {
				w.Header().Set("ETag", f.etag)
			}
			committed := f.committed
			if committed.IsZero() {
				committed = testNow
			}
			fmt.Fprintf(w, `{"commit":{"sha":%q,"commit":{"committer":{"date":%q}}},"protected":%t}`, f.tip, committed.UTC().Format(time.RFC3339), f.protected)
		case strings.HasSuffix(r.URL.Path, "/actions/workflows/validate.yml/runs"):
			if f.noWorkflow {
				http.NotFound(w, r)
				return
			}
			repo, runPath := f.runRepo, f.runPath
			if repo == "" {
				repo = "acme/norn-fleet"
			}
			if runPath == "" {
				runPath = ".github/workflows/validate.yml"
			}
			var parts []string
			for i, run := range f.runs {
				parts = append(parts, fmt.Sprintf(`{"id":%d,"head_sha":%q,"event":%q,"head_branch":%q,"head_repository":{"full_name":%q},"path":%q,"status":%q,"conclusion":%q}`, i+1, run.sha, run.event, run.branch, repo, runPath, run.status, run.conclusion))
			}
			fmt.Fprintf(w, `{"workflow_runs":[%s]}`, strings.Join(parts, ","))
		case strings.Contains(r.URL.Path, "/compare/"):
			f.compareHits++
			var page int
			fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
			lo, hi := (page-1)*100, page*100
			if lo > len(f.files) {
				lo = len(f.files)
			}
			if hi > len(f.files) {
				hi = len(f.files)
			}
			var files []string
			for _, p := range f.files[lo:hi] {
				files = append(files, fmt.Sprintf(`{"filename":%q}`, p))
			}
			fmt.Fprintf(w, `{"status":%q,"ahead_by":%d,"files":[%s]}`, f.status, f.aheadBy, strings.Join(files, ","))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func goodFake() *fakeGitHub {
	return &fakeGitHub{
		protected: true, tip: tipSHA, status: "ahead", aheadBy: 1,
		runs:  []fakeRun{{tipSHA, "push", "main", "completed", "success"}},
		files: []string{"environments/staging/nyc3/cluster.yaml"},
	}
}

func verify(t *testing.T, f *fakeGitHub, last string) *CommitVerification {
	t.Helper()
	client := testClient(t, f.handler(t))
	got, err := client.VerifyProtectedCommit(context.Background(), tipSHA, last)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestVerifyProtectedCommitHappyPath(t *testing.T) {
	got := verify(t, goodFake(), baseSHA)
	if got.Verdict != CommitVerified || got.PathClass != executor.PathClassConfig || got.ValidateRun != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestVerifyProtectedCommitRequiresProtectedFlag(t *testing.T) {
	f := goodFake()
	f.protected = false
	got := verify(t, f, baseSHA)
	if got.Verdict != CommitUnverified || got.Code != executor.CodeFleetExecutorTriggerUnverified {
		t.Fatalf("got %+v", got)
	}
}

func TestVerifyProtectedCommitNotTipFailsClosed(t *testing.T) {
	f := goodFake()
	f.tip = strings.Repeat("c", 40)
	if got := verify(t, f, baseSHA); got.Verdict != CommitUnverified {
		t.Fatalf("older/non-tip sha accepted: %+v", got)
	}
}

func TestVerifyProtectedCommitRequiresValidatePushSuccessAtSHA(t *testing.T) {
	other := strings.Repeat("d", 40)
	cases := map[string][]fakeRun{
		"workflow_dispatch": {{tipSHA, "workflow_dispatch", "main", "completed", "success"}},
		"pull_request":      {{tipSHA, "pull_request", "main", "completed", "success"}},
		"other branch":      {{tipSHA, "push", "feature", "completed", "success"}},
		"different sha":     {{other, "push", "main", "completed", "success"}},
		"failure":           {{tipSHA, "push", "main", "completed", "failure"}},
		"cancelled":         {{tipSHA, "push", "main", "completed", "cancelled"}},
	}
	for name, runs := range cases {
		t.Run(name, func(t *testing.T) {
			f := goodFake()
			f.runs = runs
			got := verify(t, f, baseSHA)
			// Non-matching runs are ignored, so they are not proof; a run
			// that matched and failed is an explicit failure.
			if got.Verdict == CommitVerified {
				t.Fatalf("accepted %s: %+v", name, got)
			}
			if (name == "failure" || name == "cancelled") && got.Verdict != CommitUnverified {
				t.Fatalf("completed failure must be unverified: %+v", got)
			}
		})
	}
	t.Run("fork head repository", func(t *testing.T) {
		f := goodFake()
		f.runRepo = "evil/norn-fleet"
		if got := verify(t, f, baseSHA); got.Verdict == CommitVerified {
			t.Fatalf("accepted a run from another repository: %+v", got)
		}
	})
	t.Run("other workflow path", func(t *testing.T) {
		f := goodFake()
		f.runPath = ".github/workflows/validate.yml.bak"
		if got := verify(t, f, baseSHA); got.Verdict == CommitVerified {
			t.Fatalf("accepted a run of another workflow: %+v", got)
		}
	})
	t.Run("renamed or missing workflow", func(t *testing.T) {
		f := goodFake()
		f.noWorkflow = true
		got := verify(t, f, baseSHA)
		if got.Verdict != CommitUnverified {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestVerifyProtectedCommitPendingIsNotFailure(t *testing.T) {
	for _, status := range []string{"queued", "in_progress"} {
		f := goodFake()
		f.runs = []fakeRun{{tipSHA, "push", "main", status, ""}}
		got := verify(t, f, baseSHA)
		if got.Verdict != CommitPending || got.Code != "" {
			t.Fatalf("%s: got %+v", status, got)
		}
	}
	f := goodFake()
	f.runs = nil // not started yet
	if got := verify(t, f, baseSHA); got.Verdict != CommitPending {
		t.Fatalf("no run yet: %+v", got)
	}
}

// A tip with no validate push run at all (e.g. [skip ci]) must not stay
// pending forever; a started run stays pending however long it takes.
func TestVerifyProtectedCommitMissingValidateRunIsBounded(t *testing.T) {
	f := goodFake()
	f.runs = nil
	f.committed = testNow.Add(-ValidateRunGrace - time.Minute)
	if got := verify(t, f, baseSHA); got.Verdict != CommitUnverified || got.Code != executor.CodeFleetExecutorTriggerUnverified {
		t.Fatalf("missing run past grace: %+v", got)
	}
	f = goodFake()
	f.runs = []fakeRun{{tipSHA, "push", "main", "queued", ""}}
	f.committed = testNow.Add(-ValidateRunGrace - time.Hour)
	if got := verify(t, f, baseSHA); got.Verdict != CommitPending {
		t.Fatalf("queued run past grace: %+v", got)
	}
}

func TestVerifyProtectedCommitDivergedIsHistoryRewritten(t *testing.T) {
	for _, status := range []string{"diverged", "behind"} {
		f := goodFake()
		f.status = status
		got := verify(t, f, baseSHA)
		if got.Verdict != CommitHistoryRewritten || got.Code != executor.CodeFleetExecutorTriggerUnverified {
			t.Fatalf("%s: got %+v", status, got)
		}
	}
}

func TestVerifyProtectedCommitUnresolvableBaseIsHistoryRewritten(t *testing.T) {
	f := goodFake()
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/compare/") {
			http.NotFound(w, r)
			return
		}
		f.handler(t).ServeHTTP(w, r)
	}))
	got, err := client.VerifyProtectedCommit(context.Background(), tipSHA, baseSHA)
	if err != nil || got.Verdict != CommitHistoryRewritten {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestVerifyProtectedCommitRevertIsDescendant(t *testing.T) {
	f := goodFake()
	f.files = []string{"environments/staging/nyc3/cluster.yaml"} // a revert is an ordinary ahead commit
	if got := verify(t, f, baseSHA); got.Verdict != CommitVerified {
		t.Fatalf("got %+v", got)
	}
}

func TestVerifyProtectedCommitFirstConvergeNeedsApproval(t *testing.T) {
	f := goodFake()
	got := verify(t, f, "")
	if got.Verdict != CommitVerified || got.PathClass != executor.PathClassExecutable || f.compareHits != 0 {
		t.Fatalf("got %+v hits=%d", got, f.compareHits)
	}
}

func TestPathClassDefaultDeny(t *testing.T) {
	cases := []struct {
		paths []string
		want  string
	}{
		{nil, executor.PathClassInert},
		{[]string{"environments/staging/nyc3/cluster.yaml"}, executor.PathClassConfig},
		{[]string{"environments/staging/nyc3/cluster.yaml", ".norn/fleet-plan-receipts/575207c9-efcf-483c-992d-fd6957308ef8.json"}, executor.PathClassConfig},
		{[]string{"docs/runbooks/x.md", "README.md", "ansible/README.md"}, executor.PathClassInert},
		{[]string{"environments/staging/nyc3/cluster.yaml", "docs/a.md"}, executor.PathClassConfig},
		{[]string{"environments/staging/nyc3/cluster.yaml", "scripts/go-test-strict"}, executor.PathClassExecutable},
		// Read by scripts (setup.py, external_mac_gate3_decision.py): not inert.
		{[]string{"docs/policies/protected-merge-metadata.json"}, executor.PathClassExecutable},
		{[]string{"docs/incidents/gate2-retention-pilot260908a.json"}, executor.PathClassExecutable},
		{[]string{"docs/v3/x.txt"}, executor.PathClassExecutable},
		// tfvars are never committed; a committed *.auto.tfvars overrides TF_VAR_*.
		{[]string{"environments/staging/nyc3/terraform.auto.tfvars"}, executor.PathClassExecutable},
		{[]string{"environments/staging/nyc3/prod.tfvars"}, executor.PathClassExecutable},
		{[]string{"environments/staging/nyc3/terraform.auto.tfvars.example"}, executor.PathClassExecutable},
		{[]string{"environments/staging/nyc3/main.tf"}, executor.PathClassExecutable},
		{[]string{"environments/staging/nyc3/reconcile-hooks.json"}, executor.PathClassExecutable},
		{[]string{"environments/staging/nyc3/cold-start-timing.json"}, executor.PathClassExecutable},
		{[]string{"environments/staging/nyc3/.terraform.lock.hcl"}, executor.PathClassExecutable},
		{[]string{"environments/disposable/fleet/nyc3/cluster.yaml"}, executor.PathClassExecutable},
		{[]string{"environments/staging/cluster.yaml"}, executor.PathClassExecutable},
		{[]string{".norn/fleet-plan-receipts/a/b.json"}, executor.PathClassExecutable},
		{[]string{".norn/fleet-plan-receipts/.json"}, executor.PathClassExecutable},
		{[]string{".github/workflows/validate.yml"}, executor.PathClassExecutable},
		{[]string{".github/README.md"}, executor.PathClassExecutable},
		{[]string{"ansible/site.yml"}, executor.PathClassExecutable},
		{[]string{"modules/x/main.tf", "cloud-init/a", "reconcile-hooks.json", "schemas/s.json"}, executor.PathClassExecutable},
		{[]string{"environments/staging/../../scripts/cluster.yaml"}, executor.PathClassExecutable},
		{[]string{".norn/fleet-plan-receipts/../x"}, executor.PathClassExecutable},
		{[]string{"/environments/staging/nyc3/cluster.yaml"}, executor.PathClassExecutable},
		{[]string{""}, executor.PathClassExecutable},
		{[]string{"unknown/thing"}, executor.PathClassExecutable},
	}
	for _, tc := range cases {
		if got := ClassifyPaths(tc.paths); got != tc.want {
			t.Errorf("%q = %s, want %s", tc.paths, got, tc.want)
		}
	}
}

func TestVerifyProtectedCommitRenameClassifiesPreviousPath(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/compare/") {
			fmt.Fprint(w, `{"status":"ahead","ahead_by":1,"files":[{"filename":"environments/staging/nyc3/cluster.yaml","previous_filename":"scripts/run.sh"}]}`)
			return
		}
		goodFake().handler(t).ServeHTTP(w, r)
	}))
	got, err := client.VerifyProtectedCommit(context.Background(), tipSHA, baseSHA)
	if err != nil || got.PathClass != executor.PathClassExecutable {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestVerifyProtectedCommitComparePagination(t *testing.T) {
	cfg := func(n int, ahead int, executableAt int) *fakeGitHub {
		f := goodFake()
		f.aheadBy = ahead
		for i := 0; i < n; i++ {
			f.files = append(f.files[:0:0], f.files...)
			f.files = append(f.files, fmt.Sprintf("environments/e%d/nyc3/cluster.yaml", i))
		}
		if executableAt >= 0 {
			f.files[executableAt] = "scripts/evil.sh"
		}
		return f
	}
	t.Run("all pages are read", func(t *testing.T) {
		f := cfg(150, 5, 120) // executable file only on page 2
		f.files = f.files[1:]
		got := verify(t, f, baseSHA)
		if got.PathClass != executor.PathClassExecutable || f.compareHits < 2 {
			t.Fatalf("got %+v hits=%d", got, f.compareHits)
		}
	})
	t.Run("paginated config diff stays config", func(t *testing.T) {
		f := cfg(150, 5, -1)
		got := verify(t, f, baseSHA)
		if got.PathClass != executor.PathClassConfig || got.ChangedPaths != 151 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("file cap reached is truncated and executable", func(t *testing.T) {
		f := cfg(400, 5, -1) // GitHub caps at 300 files; all config-looking
		got := verify(t, f, baseSHA)
		if got.Verdict != CommitVerified || got.PathClass != executor.PathClassExecutable {
			t.Fatalf("got %+v", got)
		}
		if f.compareHits > 3 {
			t.Fatalf("kept paging past the cap: %d", f.compareHits)
		}
	})
	t.Run("commit cap reached is executable", func(t *testing.T) {
		f := cfg(0, compareMaxCommits, -1)
		if got := verify(t, f, baseSHA); got.PathClass != executor.PathClassExecutable {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestReadBranchTipETag304NoWork(t *testing.T) {
	f := goodFake()
	f.etag = `W/"abc"`
	client := testClient(t, f.handler(t))
	first, err := client.ReadBranchTip(context.Background(), "")
	if err != nil || first.NotModified || first.SHA != tipSHA || !first.Protected || first.ETag != f.etag {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := client.ReadBranchTip(context.Background(), first.ETag)
	if err != nil || !second.NotModified || second.SHA != "" || second.ETag != f.etag {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}
