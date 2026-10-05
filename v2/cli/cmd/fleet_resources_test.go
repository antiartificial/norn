package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"norn/v2/cli/api"
)

const (
	frSHA     = "0123456789abcdef0123456789abcdef01234567"
	frPlan    = "11111111-1111-4111-8111-111111111111"
	frAttempt = "22222222-2222-4222-8222-222222222222"
	frOlder   = "99999999-9999-4999-8999-999999999999"
	frTarget  = "tgt_" + "0000000000000000000000000000000000000000000000000000000000000000"
)

type frCall struct {
	Method, Path, Key string
	Body              map[string]any
}

type frAPI struct {
	calls    []frCall
	resource string
}

func (f *frAPI) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c := frCall{Method: r.Method, Path: r.URL.RequestURI(), Key: r.Header.Get("Idempotency-Key")}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &c.Body); err != nil {
				t.Errorf("bad body %q", raw)
			}
		}
		f.calls = append(f.calls, c)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/fleet/resources/"):
			_, _ = w.Write([]byte(f.resource))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/fleet/resources":
			_, _ = w.Write([]byte(`{"items":[` + f.resource + `],"controller":{"state":"running"}}`))
		case strings.HasSuffix(r.URL.Path, "/converge") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"resource":"staging","tipSha":"` + frSHA + `","action":"recorded","planId":"` + frPlan + `"}`))
		case strings.HasSuffix(r.URL.Path, "/fence"):
			_, _ = w.Write([]byte(frFence))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/fleet/targets/"+frTarget:
			_, _ = w.Write([]byte(`{"targetId":"` + frTarget + `","provider":"digitalocean","providerAccount":"acct","stateBackend":"spaces","createdAt":"t","registrationOperationId":"op-0","aliases":["staging/nyc3"],"fence":` + frFence + `}`))
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"op-1","kind":"x","status":"succeeded"}`))
		}
	}))
	t.Cleanup(srv.Close)
	prev := client
	client = api.New(srv.URL)
	t.Cleanup(func() { client = prev })
}

const frFence = `{"targetId":"` + frTarget + `","generation":7,"held":true,"holderPlanId":"` + frPlan + `","authorityEpoch":1,"currentAuthorityEpoch":1,"revision":9,"occupancy":"Uncertain","occupancyReason":"NoLiveAttempt"}`

func frResource() string {
	return `{"name":"staging","targetId":"` + frTarget + `","revision":4,"authorityEpoch":1,"createdAt":"t","updatedAt":"t",
"desired":{"generation":3,"planId":"` + frPlan + `","commitSha":"` + frSHA + `","converge":{"planSha256":"` + strings.Repeat("a", 64) + `","requiresApproval":true,"approved":false}},
"status":{"observedGeneration":3,"lastAppliedGeneration":2,"lastApplied":{"generation":2,"commitSha":"bbbb","planId":"p0"},
"active":{"planId":"` + frPlan + `","attemptIds":["` + frAttempt + `","` + frOlder + `"],"fenceGeneration":7,"occupancy":"Uncertain"},
"conditions":[{"type":"RuntimeReady","status":"Unknown","reason":"ObservationStale","evidenceRefs":[]}],"nextAction":"approve_converge"},
"blocker":{"kind":"approval","reason":"approval_required","planId":"` + frPlan + `","message":"attempt ` + frAttempt + ` stopped and its resume approval is spent or absent; run: norn fleet converge staging --resume"},
"approvalPolicy":{"basis":"github-protected-main","protectedBranch":true,"mergedPullRequest":false,"planWorkflowSucceeded":false,"boundPlanDigest":true,"authorizedDispatch":false,"ownerApprovalEnvelope":false,"environmentReviewers":"not_enforced","independentReview":"not_enforced"},
"executionMode":"controller","controller":{"state":"running"},"readAt":"t"}`
}

func frRun(t *testing.T, c *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	fleetResJSON, fleetResKey, fleetResResume, fleetResCommit, fleetResSourceAttempt = false, "", false, "", ""
	fleetResResource, fleetResReason, fleetResEvidenceFile, fleetResReleaseMode, fleetResExpectedGen, fleetResProviderAttempt = "", "", "", "", -1, ""
	c.Flags().VisitAll(func(f *pflag.Flag) { _ = f.Value.Set(f.DefValue) })
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	if err := c.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	err := c.RunE(c, c.Flags().Args())
	return out.String(), errb.String(), err
}

func TestFleetStatusShowsResourceView(t *testing.T) {
	f := &frAPI{resource: frResource()}
	f.start(t)
	out, _, err := frRun(t, fleetStatusCmd, "staging")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mode=controller", "gen 3", "gen 2", "run: norn fleet converge staging --resume", "next     approve_converge", "basis=github-protected-main", "controller=running", frAttempt, "RuntimeReady=Unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	out, _, err = frRun(t, fleetStatusCmd)
	if err != nil || !strings.Contains(out, "staging") || !strings.Contains(out, "controller: running") {
		t.Fatalf("list: %q %v", out, err)
	}
	out, _, err = frRun(t, fleetStatusCmd, "staging", "--json")
	var back api.FleetResource
	if err != nil || json.Unmarshal([]byte(out), &back) != nil || back.Revision != 4 {
		t.Fatalf("json: %q %v", out, err)
	}
}

func TestFleetConvergeRescanAndResume(t *testing.T) {
	f := &frAPI{resource: frResource()}
	f.start(t)
	_, errb, err := frRun(t, fleetConvergeCmd, "staging")
	if err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	if c.Method != "POST" || c.Path != "/api/v1/fleet/resources/staging/converge" || c.Body != nil || !strings.HasPrefix(c.Key, "norn-fleet-converge-") || !strings.Contains(errb, c.Key) {
		t.Fatalf("rescan call %+v err=%q", c, errb)
	}
	f.calls = nil
	out, errb, err := frRun(t, fleetConvergeCmd, "staging", "--resume", "--idempotency-key", "k-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || f.calls[0].Method != "GET" {
		t.Fatalf("calls %+v", f.calls)
	}
	c = f.calls[1]
	if c.Path != "/api/v1/fleet/plans/"+frPlan+"/converge/approve" || c.Key != "k-1" ||
		c.Body["commitSha"] != frSHA || c.Body["resume"] != true || c.Body["sourceAttemptId"] != frAttempt || len(c.Body) != 3 {
		t.Fatalf("resume call %+v", c)
	}
	if !strings.Contains(out, "op-1") || !strings.Contains(errb, "--source-attempt "+frAttempt+" --commit "+frSHA+" --idempotency-key k-1") {
		t.Fatalf("out %q err %q", out, errb)
	}

	// The desired revision moved on while the fence is held: the holder's
	// commit comes from desiredHistory.
	f.resource = strings.Replace(frResource(), `"desired":{"generation":3,"planId":"`+frPlan+`","commitSha":"`+frSHA+`"`,
		`"desiredHistory":[{"generation":3,"planId":"`+frPlan+`","commitSha":"`+frSHA+`"}],"desired":{"generation":4,"planId":"p-new","commitSha":"cccc"`, 1)
	f.calls = nil
	if _, _, err := frRun(t, fleetConvergeCmd, "staging", "--resume"); err != nil {
		t.Fatal(err)
	}
	if c := f.calls[1]; c.Body["commitSha"] != frSHA || c.Body["sourceAttemptId"] != frAttempt {
		t.Fatalf("history resume %+v", c)
	}

	// No resume-needed blocker: refuse rather than guess an attempt.
	f.resource = strings.Replace(frResource(), `"reason":"approval_required"`, `"reason":"destructive_requires_github_lane"`, 1)
	f.calls = nil
	if _, _, err := frRun(t, fleetConvergeCmd, "staging", "--resume"); err == nil || len(f.calls) != 1 {
		t.Fatalf("resume without blocker: err=%v calls=%+v", err, f.calls)
	}
}

func TestFleetApproveLooksUpCommitAndSendsPinnedBody(t *testing.T) {
	f := &frAPI{resource: frResource()}
	f.start(t)
	if _, _, err := frRun(t, fleetApproveCmd, frPlan); err != nil {
		t.Fatal(err)
	}
	c := f.calls[len(f.calls)-1]
	if c.Path != "/api/v1/fleet/plans/"+frPlan+"/converge/approve" || c.Body["commitSha"] != frSHA || len(c.Body) != 1 || !strings.HasPrefix(c.Key, "norn-fleet-approve-") {
		t.Fatalf("approve call %+v", c)
	}
	if _, _, err := frRun(t, fleetApproveCmd, frPlan, "--resume", "--commit", frSHA); err == nil {
		t.Fatal("--resume without --source-attempt accepted")
	}
}

func TestFleetModeUsesRevisionAndValidates(t *testing.T) {
	f := &frAPI{resource: frResource()}
	f.start(t)
	if _, _, err := frRun(t, fleetModeCmd, "staging", "bogus"); err == nil {
		t.Fatal("bad mode accepted")
	}
	if _, _, err := frRun(t, fleetModeCmd, "staging", "github"); err != nil {
		t.Fatal(err)
	}
	c := f.calls[len(f.calls)-1]
	if c.Path != "/api/v1/fleet/resources/staging/execution-mode" || c.Body["mode"] != "github" || c.Body["expectedRevision"] != float64(4) || len(c.Body) != 2 || c.Key == "" {
		t.Fatalf("mode call %+v", c)
	}
	// A retry with the printed revision and key sends the same body without a read.
	f.calls = nil
	if _, errb, err := frRun(t, fleetModeCmd, "staging", "github", "--expected-revision", "4", "--idempotency-key", "k-2"); err != nil || strings.Contains(errb, "expected revision") {
		t.Fatal(err, errb)
	}
	if len(f.calls) != 1 || f.calls[0].Body["expectedRevision"] != float64(4) || f.calls[0].Key != "k-2" {
		t.Fatalf("retry calls %+v", f.calls)
	}
}

func frEvidenceFile(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "ev.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const frEvidence = `{"provider":"digitalocean","dropletId":"123","status":"off","observedAt":"2026-10-05T10:00:00Z","reference":"console"}`

func TestFleetProviderEvidenceReleaseAndAbandon(t *testing.T) {
	f := &frAPI{resource: frResource()}
	f.start(t)
	file := frEvidenceFile(t, frEvidence)

	if _, _, err := frRun(t, fleetProviderEvidenceCmd, frPlan, "--source-attempt", frAttempt, "--provider-evidence", file); err != nil {
		t.Fatal(err)
	}
	c := f.calls[len(f.calls)-1]
	ev, _ := c.Body["providerEvidence"].(map[string]any)
	if c.Path != "/api/v1/fleet/plans/"+frPlan+"/provider-evidence" || c.Key == "" || c.Body["sourceAttemptId"] != frAttempt || ev["dropletId"] != "123" || ev["status"] != "off" {
		t.Fatalf("evidence call %+v", c)
	}

	f.calls = nil
	if _, _, err := frRun(t, fleetReleaseCmd, frTarget, "--mode", "abandon"); err == nil || len(f.calls) != 0 {
		t.Fatalf("release without --expected-generation: err=%v calls=%+v", err, f.calls)
	}
	if _, _, err := frRun(t, fleetReleaseCmd, frTarget, "--mode", "abandon", "--expected-generation", "7", "--reason", "host gone", "--provider-evidence", file); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls %+v", f.calls)
	}
	c = f.calls[0]
	if c.Path != "/api/v1/fleet/targets/"+frTarget+"/fence/release" || c.Body["mode"] != "abandon" || c.Body["expectedGeneration"] != float64(7) || c.Body["reason"] != "host gone" || c.Body["providerEvidence"] == nil || len(c.Body) != 4 || c.Key == "" {
		t.Fatalf("release call %+v", c)
	}

	f.calls = nil
	if _, _, err := frRun(t, fleetAbandonCmd, frPlan); err != nil {
		t.Fatal(err)
	}
	c = f.calls[0]
	if c.Path != "/api/v1/fleet/targets/abandon-plan" || c.Body["planId"] != frPlan || len(c.Body) != 1 || c.Key == "" {
		t.Fatalf("abandon call %+v", c)
	}
	bad := frEvidenceFile(t, `{"provider":"digitalocean","dropletId":"1","status":"running","observedAt":"x"}`)
	if _, _, err := frRun(t, fleetAbandonCmd, frPlan, "--provider-evidence", bad); err == nil {
		t.Fatal("bad evidence status accepted")
	}
}

func TestFleetResourceCommandsExistWithBlockerNames(t *testing.T) {
	names := map[string]string{}
	for _, c := range fleetCmd.Commands() {
		names[c.Name()] = c.Long + c.Short
	}
	for _, n := range []string{"status", "targets", "fence", "converge", "approve", "mode", "provider-evidence", "release", "abandon"} {
		if _, ok := names[n]; !ok {
			t.Errorf("fleet %s missing", n)
		}
	}
	for _, n := range []string{"converge", "approve", "mode", "provider-evidence", "release", "abandon"} {
		if !strings.Contains(names[n], "Permission:") {
			t.Errorf("fleet %s help lacks permission", n)
		}
	}
	if fleetConvergeCmd.Flags().Lookup("resume") == nil {
		t.Error("converge --resume missing")
	}
}

func TestFleetTargetsAndFenceAreReadOnly(t *testing.T) {
	f := &frAPI{resource: frResource()}
	f.start(t)
	out, _, err := frRun(t, fleetFenceCmd, frTarget)
	if err != nil || !strings.Contains(out, "generation 7") || !strings.Contains(out, "held by "+frPlan) || !strings.Contains(out, "NoLiveAttempt") {
		t.Fatalf("fence %q %v", out, err)
	}
	out, _, err = frRun(t, fleetTargetsCmd, frTarget)
	if err != nil || !strings.Contains(out, "staging/nyc3") || !strings.Contains(out, "generation 7") {
		t.Fatalf("target %q %v", out, err)
	}
	out, _, err = frRun(t, fleetTargetsCmd)
	if err != nil || !strings.Contains(out, frTarget) || !strings.Contains(out, "held gen 7") {
		t.Fatalf("targets %q %v", out, err)
	}
	for _, c := range f.calls {
		if c.Method != http.MethodGet || c.Key != "" {
			t.Fatalf("non-read call %+v", c)
		}
	}
	if f.calls[0].Path != "/api/v1/fleet/targets/"+frTarget+"/fence" || f.calls[1].Path != "/api/v1/fleet/targets/"+frTarget {
		t.Fatalf("paths %+v", f.calls)
	}
}
