package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"norn/v2/api/fleet/executor"
	"norn/v2/api/githubapp"
)

var (
	shaA = strings.Repeat("a", 40)
	shaB = strings.Repeat("b", 40)
	shaC = strings.Repeat("c", 40)
)

// fakeGitHub serves the three endpoints E3 reads, with mutable state.
type fakeGitHub struct {
	mu        sync.Mutex
	tip       string
	protected bool
	etag      string
	committed time.Time
	runs      map[string]string // sha -> "success" | "failure" | "pending"
	files     []string
	status    string
	behind    map[string]bool // compare base sha -> served as "behind"
	tipReads  int
	notMod    int
	compares  int
}

func (f *fakeGitHub) set(fn func()) { f.mu.Lock(); defer f.mu.Unlock(); fn() }

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/app/installations/5678/access_tokens":
		fmt.Fprint(w, `{"token":"t","expires_at":"2099-01-15T09:00:00Z"}`)
	case strings.HasSuffix(r.URL.Path, "/branches/main"):
		f.tipReads++
		if r.Header.Get("If-None-Match") == f.etag && f.etag != "" {
			f.notMod++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", f.etag)
		fmt.Fprintf(w, `{"commit":{"sha":%q,"commit":{"committer":{"date":%q}}},"protected":%t}`, f.tip, f.committed.UTC().Format(time.RFC3339), f.protected)
	case strings.HasSuffix(r.URL.Path, "/actions/workflows/validate.yml/runs"):
		var runs []string
		if state, ok := f.runs[r.URL.Query().Get("head_sha")]; ok {
			status, conclusion := "completed", state
			if state == "pending" {
				status, conclusion = "in_progress", ""
			}
			runs = append(runs, fmt.Sprintf(`{"id":1,"head_sha":%q,"event":"push","head_branch":"main","head_repository":{"full_name":"acme/norn-fleet"},"path":".github/workflows/validate.yml","status":%q,"conclusion":%q}`, r.URL.Query().Get("head_sha"), status, conclusion))
		}
		fmt.Fprintf(w, `{"workflow_runs":[%s]}`, strings.Join(runs, ","))
	case strings.Contains(r.URL.Path, "/compare/"):
		f.compares++
		status := f.status
		for base := range f.behind {
			if strings.Contains(r.URL.Path, "/compare/"+base+"...") {
				status = "behind"
			}
		}
		var files []string
		for _, p := range f.files {
			files = append(files, fmt.Sprintf(`{"filename":%q}`, p))
		}
		fmt.Fprintf(w, `{"status":%q,"ahead_by":1,"files":[%s]}`, status, strings.Join(files, ","))
	default:
		http.NotFound(w, r)
	}
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, *githubapp.Client) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGitHub{tip: shaB, protected: true, etag: `"e1"`, committed: time.Now(), status: "ahead",
		runs: map[string]string{shaB: "success"}, files: []string{"environments/staging/nyc3/cluster.yaml"}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := githubapp.New(githubapp.Config{
		AppID: "1234", InstallationID: 5678, PrivateKeyFile: keyPath, Repository: "acme/norn-fleet", DefaultBranch: "main",
		Environment: "staging", ConfigPath: "environments/staging/nyc3/cluster.yaml", APIBaseURL: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return fake, client
}

// fakeRecorder is an in-memory ConvergePlanRecorder with replay by key.
type fakeRecorder struct {
	mu         sync.Mutex
	plans      []*ConvergeCapacityPlan
	byKey      map[string]*RecordedConverge
	inadmiss   map[string]bool
	recordErr  error
	records    int
	onRecorded func()
}

func (f *fakeRecorder) NewestConvergePlan(_ context.Context, resource string) (*RecordedConverge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.plans) - 1; i >= 0; i-- {
		if p := f.plans[i]; p.Converge.Resource == resource {
			return &RecordedConverge{PlanID: p.ID, Digest: p.Digest, Converge: *p.Converge, Admissible: !f.inadmiss[p.ID]}, nil
		}
	}
	return nil, nil
}

func (f *fakeRecorder) RecordConvergePlan(_ context.Context, plan *ConvergeCapacityPlan, key string) (*RecordedConverge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return nil, f.recordErr
	}
	if got, ok := f.byKey[key]; ok {
		return got, nil
	}
	if f.byKey == nil {
		f.byKey = map[string]*RecordedConverge{}
	}
	f.records++
	f.plans = append(f.plans, plan)
	rec := &RecordedConverge{PlanID: plan.ID, Digest: plan.Digest, Converge: *plan.Converge, Admissible: true}
	f.byKey[key] = rec
	return rec, nil
}

func (f *fakeRecorder) recorded() []*ConvergeCapacityPlan {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*ConvergeCapacityPlan(nil), f.plans...)
}

type fakeSource struct {
	mu        sync.Mutex
	resources []WatchedResource
}

func (s *fakeSource) ListWatchedResources(context.Context) ([]WatchedResource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]WatchedResource(nil), s.resources...), nil
}

func (s *fakeSource) setApplied(sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[0].LastApplied = sha
}

func newWatcher(t *testing.T, lastApplied string) (*Watcher, *fakeGitHub, *fakeRecorder, *fakeSource) {
	t.Helper()
	gh, client := newFakeGitHub(t)
	rec := &fakeRecorder{}
	src := &fakeSource{resources: []WatchedResource{{Name: "staging", Cluster: "staging", Repository: "acme/norn-fleet", LastApplied: lastApplied, DesiredGeneration: 4}}}
	n := 0
	w := &Watcher{Source: src, GitHub: client, Recorder: rec, Clock: newFakeClock(), SigningKey: "k",
		NewID: func() string { n++; return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }}
	return w, gh, rec, src
}

func decisionOf(t *testing.T, w *Watcher) WatchDecision {
	t.Helper()
	d, ok := w.Decision("staging")
	if !ok {
		t.Fatal("no decision recorded")
	}
	return d
}

func TestWatcherRecordsOnlyNewestVerifiedTip(t *testing.T) {
	w, gh, rec, _ := newWatcher(t, shaA)
	ctx := context.Background()
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	plans := rec.recorded()
	if len(plans) != 1 || plans[0].Converge.CommitSHA != shaB || plans[0].Converge.BaseCommitSHA != shaA || plans[0].Converge.Generation != 5 {
		t.Fatalf("plans = %+v", plans)
	}
	if plans[0].Converge.PathClass != executor.PathClassConfig || plans[0].Converge.RequiresApproval {
		t.Fatalf("config range must auto-apply: %+v", plans[0].Converge)
	}
	if !VerifyConvergePlan(plans[0], []string{"k"}) {
		t.Fatal("recorded plan does not verify")
	}
	// A newer tip supersedes it: the range is still lastApplied..tip.
	gh.set(func() { gh.tip, gh.etag, gh.runs[shaC] = shaC, `"e2"`, "success" })
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	plans = rec.recorded()
	if len(plans) != 2 || plans[1].Converge.CommitSHA != shaC || plans[1].Converge.BaseCommitSHA != shaA {
		t.Fatalf("plans = %+v", plans)
	}
	// The tip moves back to the older, already-planned commit (reset or force
	// push): it is behind the newest plan, so nothing older is ever recorded.
	gh.set(func() { gh.tip, gh.etag, gh.behind = shaB, `"e3"`, map[string]bool{shaC: true} })
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if d := decisionOf(t, w); d.Action != WatchHistoryRewritten || len(rec.recorded()) != 2 {
		t.Fatalf("older tip must not be recorded: %+v plans=%d", d, len(rec.recorded()))
	}
}

func TestWatcherNeverRecordsOlderSHA(t *testing.T) {
	w, gh, rec, _ := newWatcher(t, shaA)
	ctx := context.Background()
	// The tip the poll read goes stale before verification: verification
	// re-reads the branch, sees a different tip and refuses the old SHA.
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rec.recorded()) != 1 {
		t.Fatal("expected first plan")
	}
	stale := w.tip
	gh.set(func() { gh.tip, gh.etag, gh.runs[shaC] = shaC, `"e2"`, "success" })
	d, err := w.evaluate(ctx, WatchedResource{Name: "staging", Cluster: "staging", Repository: "r", LastApplied: shaA}, stale)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != WatchUnverified || len(rec.recorded()) != 1 {
		t.Fatalf("stale SHA must not be recorded: %+v plans=%d", d, len(rec.recorded()))
	}
}

func TestWatcherCoalescesRange(t *testing.T) {
	w, gh, rec, _ := newWatcher(t, shaA)
	// One range lastApplied..tip spanning config and executable files is one plan
	// whose class covers every file.
	gh.set(func() {
		gh.files = []string{"environments/staging/nyc3/cluster.yaml", "scripts/executor.py", "README.md"}
	})
	if err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	plans := rec.recorded()
	if len(plans) != 1 || plans[0].Converge.PathClass != executor.PathClassExecutable || !plans[0].Converge.RequiresApproval {
		t.Fatalf("plans = %+v", plans)
	}
	if gh.compares != 1 {
		t.Fatalf("compare calls = %d, want one range compare", gh.compares)
	}
}

func TestWatcherFirstConvergeNeedsApproval(t *testing.T) {
	w, _, rec, _ := newWatcher(t, "")
	if err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	plans := rec.recorded()
	if len(plans) != 1 || !plans[0].Converge.RequiresApproval || plans[0].Converge.BaseCommitSHA != "" {
		t.Fatalf("plans = %+v", plans)
	}
}

func TestWatcherInertNoConverge(t *testing.T) {
	w, gh, rec, _ := newWatcher(t, shaA)
	gh.set(func() { gh.files = []string{"README.md", "docs/runbooks/x.md"} })
	if err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.recorded()) != 0 || decisionOf(t, w).Action != WatchNone {
		t.Fatalf("docs-only must not converge: %+v", decisionOf(t, w))
	}
	// Already applied tip: also nothing.
	w2, _, rec2, _ := newWatcher(t, shaB)
	if err := w2.Step(context.Background()); err != nil || len(rec2.recorded()) != 0 {
		t.Fatalf("applied tip recorded a plan: %v", err)
	}
}

func TestWatcherIdempotentExistingPlan(t *testing.T) {
	w, _, rec, _ := newWatcher(t, shaA)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := w.Rescan(ctx, "staging"); err != nil {
			t.Fatal(err)
		}
		if err := w.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if rec.records != 1 || decisionOf(t, w).Action != WatchExists {
		t.Fatalf("records = %d decision = %+v", rec.records, decisionOf(t, w))
	}
	// A refused (inadmissible) newest plan is replaced on rescan, once.
	rec.mu.Lock()
	rec.inadmiss = map[string]bool{rec.plans[0].ID: true}
	rec.mu.Unlock()
	d, err := w.Rescan(ctx, "staging")
	if err != nil || d.Action != WatchRecorded || rec.records != 2 {
		t.Fatalf("rescan after refusal: %+v err=%v records=%d", d, err, rec.records)
	}
	if d, _ = w.Rescan(ctx, "staging"); d.Action != WatchExists || rec.records != 2 {
		t.Fatalf("second rescan must be a no-op: %+v records=%d", d, rec.records)
	}
}

func TestWatcherPendingStaysPending(t *testing.T) {
	w, gh, rec, _ := newWatcher(t, shaA)
	ctx := context.Background()
	gh.set(func() { gh.runs[shaB] = "pending" })
	for i := 0; i < 3; i++ {
		if err := w.Step(ctx); err != nil {
			t.Fatal(err)
		}
		if d := decisionOf(t, w); d.Action != WatchPending || len(rec.recorded()) != 0 {
			t.Fatalf("round %d: %+v", i, d)
		}
	}
	// A pending verdict is re-evaluated even though the ETag is unchanged (304).
	gh.set(func() { gh.runs[shaB] = "success" })
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if gh.notMod == 0 || len(rec.recorded()) != 1 || decisionOf(t, w).Action != WatchRecorded {
		t.Fatalf("pending did not resolve on 304: notMod=%d %+v", gh.notMod, decisionOf(t, w))
	}
}

func TestWatcherNoRunGraceBecomesUnverified(t *testing.T) {
	w, gh, rec, _ := newWatcher(t, shaA)
	ctx := context.Background()
	gh.set(func() { delete(gh.runs, shaB); gh.committed = time.Now().Add(-time.Minute) })
	if err := w.Step(ctx); err != nil || decisionOf(t, w).Action != WatchPending {
		t.Fatalf("within grace must be pending: %v %+v", err, decisionOf(t, w))
	}
	gh.set(func() { gh.committed = time.Now().Add(-githubapp.ValidateRunGrace - time.Minute) })
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	d := decisionOf(t, w)
	if d.Action != WatchUnverified || d.Code != executor.CodeFleetExecutorTriggerUnverified || len(rec.recorded()) != 0 {
		t.Fatalf("past grace must be trigger_unverified: %+v", d)
	}
	// A completed failure is a blocker, and an unprotected branch never records.
	gh.set(func() { gh.runs[shaB] = "failure" })
	if err := w.Step(ctx); err != nil || decisionOf(t, w).Action != WatchUnverified {
		t.Fatalf("failed validate: %+v", decisionOf(t, w))
	}
	gh.set(func() { gh.runs[shaB], gh.protected = "success", false })
	if err := w.Step(ctx); err != nil || decisionOf(t, w).Action != WatchUnverified || len(rec.recorded()) != 0 {
		t.Fatalf("unprotected branch: %+v", decisionOf(t, w))
	}
}

func TestWatcherHistoryRewrittenNeverRecords(t *testing.T) {
	w, gh, rec, _ := newWatcher(t, shaA)
	gh.set(func() { gh.status = "diverged" })
	if err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := decisionOf(t, w); d.Action != WatchHistoryRewritten || len(rec.recorded()) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestWatcherETag304NoWork(t *testing.T) {
	w, gh, rec, _ := newWatcher(t, shaA)
	ctx := context.Background()
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	compares := gh.compares
	for i := 0; i < 3; i++ {
		if err := w.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if gh.notMod != 3 || gh.compares != compares || rec.records != 1 {
		t.Fatalf("304 polls did work: notMod=%d compares=%d->%d records=%d", gh.notMod, compares, gh.compares, rec.records)
	}
}

func TestWatcherRecordFailureRetries(t *testing.T) {
	w, _, rec, _ := newWatcher(t, shaA)
	ctx := context.Background()
	rec.recordErr = errors.New("store down")
	if err := w.Step(ctx); err == nil {
		t.Fatal("record failure must surface")
	}
	rec.recordErr = nil
	if err := w.Step(ctx); err != nil || rec.records != 1 {
		t.Fatalf("retry: err=%v records=%d", err, rec.records)
	}
}

type fakeDesired struct {
	fail  bool
	calls []string
}

func (d *fakeDesired) SetConvergeDesired(_ context.Context, r WatchedResource, p RecordedConverge) error {
	d.calls = append(d.calls, p.PlanID)
	if d.fail {
		return errors.New("cas conflict")
	}
	return nil
}

func TestWatcherDesiredFailureHealsWithoutSecondPlan(t *testing.T) {
	w, _, rec, _ := newWatcher(t, shaA)
	desired := &fakeDesired{fail: true}
	w.Desired = desired
	ctx := context.Background()
	if err := w.Step(ctx); err == nil {
		t.Fatal("desired failure must surface")
	}
	desired.fail = false
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if rec.records != 1 || len(desired.calls) != 2 || desired.calls[0] != desired.calls[1] {
		t.Fatalf("records=%d calls=%v", rec.records, desired.calls)
	}
}

func TestWatcherRescanNotWatched(t *testing.T) {
	w, _, _, _ := newWatcher(t, shaA)
	if _, err := w.Rescan(context.Background(), "other"); !errors.Is(err, ErrNotWatched) {
		t.Fatalf("err = %v", err)
	}
}

func TestWatcherRunIsClockInjectableAndStopSafe(t *testing.T) {
	w, gh, rec, _ := newWatcher(t, shaA)
	clock := newFakeClock()
	w.Clock = clock
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	waitFor := func(cond func() bool) {
		t.Helper()
		for i := 0; i < 400 && !cond(); i++ {
			time.Sleep(5 * time.Millisecond)
		}
		if !cond() {
			t.Fatal("condition not reached")
		}
	}
	reads := func() int { gh.mu.Lock(); defer gh.mu.Unlock(); return 1 + gh.notMod } // polls: one 200, then 304s
	timers := func() int { clock.mu.Lock(); defer clock.mu.Unlock(); return len(clock.timers) }
	waitFor(func() bool { return reads() == 1 && timers() == 1 })
	clock.Advance(DefaultWatchInterval - time.Second)
	time.Sleep(30 * time.Millisecond)
	if reads() != 1 {
		t.Fatal("polled before the interval elapsed")
	}
	clock.Advance(time.Second)
	waitFor(func() bool { return reads() == 2 && timers() == 1 })
	// Stop between polls: Run returns, and a later tip is never recorded.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	gh.set(func() { gh.tip, gh.etag, gh.runs[shaC] = shaC, `"e9"`, "success" })
	clock.Advance(time.Hour)
	time.Sleep(30 * time.Millisecond)
	if rec.records != 1 {
		t.Fatalf("records after stop = %d", rec.records)
	}
	// A cancelled Step records nothing.
	if err := w.Step(ctx); err == nil || rec.records != 1 {
		t.Fatalf("cancelled step: err=%v records=%d", err, rec.records)
	}
}

// flipCommits moves the branch tip just before the n-th verification call.
type flipCommits struct {
	CommitSource
	calls, at int
	flip      func()
}

func (f *flipCommits) VerifyProtectedCommit(ctx context.Context, sha, lastApplied string) (*githubapp.CommitVerification, error) {
	if f.calls++; f.calls == f.at {
		f.flip()
	}
	return f.CommitSource.VerifyProtectedCommit(ctx, sha, lastApplied)
}

func TestWatcherDescendantCheckFailsClosed(t *testing.T) {
	w, gh, rec, _ := newWatcher(t, shaA)
	ctx := context.Background()
	if err := w.Step(ctx); err != nil || len(rec.recorded()) != 1 {
		t.Fatalf("first plan: %v", err)
	}
	// An unapplied plan for B exists and C is the new tip. The branch moves
	// again between C's verification and the descendant check over B: C is no
	// longer the tip, so nothing is recorded and the resource is re-polled.
	gh.set(func() { gh.tip, gh.etag, gh.runs[shaC] = shaC, `"e2"`, "success" })
	shaD := strings.Repeat("d", 40)
	w.GitHub = &flipCommits{CommitSource: w.GitHub, at: 2, flip: func() { gh.set(func() { gh.tip, gh.etag = shaD, `"e3"` }) }}
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if d := decisionOf(t, w); d.Action != WatchUnverified || len(rec.recorded()) != 1 {
		t.Fatalf("moved tip recorded: %+v plans=%d", d, len(rec.recorded()))
	}
}

// staleNewest hides recorded plans, as a second watcher racing the first sees.
type staleNewest struct{ *fakeRecorder }

func (staleNewest) NewestConvergePlan(context.Context, string) (*RecordedConverge, error) {
	return nil, nil
}

func TestWatcherConcurrentRecordersShareOneKey(t *testing.T) {
	w, _, rec, src := newWatcher(t, shaA)
	ctx := context.Background()
	if err := w.Step(ctx); err != nil || rec.records != 1 {
		t.Fatalf("first: %v records=%d", err, rec.records)
	}
	// A second watcher (authority restart overlap) that has not seen the first
	// plan, reached through a rescan, derives the same record key.
	_, client := newFakeGitHub(t)
	other := &Watcher{Source: src, GitHub: client, Recorder: staleNewest{rec}, SigningKey: "k",
		NewID: func() string { return "00000000-0000-4000-8000-0000000000ff" }}
	d, err := other.Rescan(ctx, "staging")
	if err != nil || rec.records != 1 || d.PlanID != rec.recorded()[0].ID {
		t.Fatalf("second watcher: %+v err=%v records=%d", d, err, rec.records)
	}
}
