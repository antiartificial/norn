package githubapp

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"norn/v2/api/fleet"
)

func TestApplyRunDisplayTitleUsesNonceContract(t *testing.T) {
	if got := fmt.Sprintf(applyRunDisplayTitleFormat, "production/nyc3", "plan", strings.Repeat("a", 64)); got != "Apply production/nyc3 Norn plan plan nonce "+strings.Repeat("a", 64) {
		t.Fatalf("server run-name format=%q", got)
	}
	if got := applyRunDisplayTitle("disposable/external-mac/nyc3", "pilot20260907", "plan", strings.Repeat("a", 64)); got != "Apply disposable/external-mac/nyc3 pilot pilot20260907 Norn plan plan nonce "+strings.Repeat("a", 64) {
		t.Fatalf("pilot run-name format=%q", got)
	}
}

func TestVerifyPilotApplyRunRequiresExactConfiguredTitleInRawAndNonceHashModes(t *testing.T) {
	planID := "11111111-1111-4111-8111-111111111111"
	nonce := strings.Repeat("a", 64)
	nonceSum := sha256.Sum256([]byte(nonce))
	nonceHash := hex.EncodeToString(nonceSum[:])
	headSHA := strings.Repeat("b", 40)
	environment := "disposable/external-mac/nyc3"
	pilotRunID := "pilot20260907"
	approved := &Dispatch{ApprovedHeadSHA: headSHA, PilotRunID: pilotRunID}
	client := testClient(t, http.NotFoundHandler())
	client.cfg.PilotRunID = pilotRunID
	validTitle := applyRunDisplayTitle(environment, pilotRunID, planID, nonce)
	validRun := func(title string) *applyRun {
		run := &applyRun{ID: 93, HTMLURL: "https://github.com/acme/norn-fleet/actions/runs/93", Event: "workflow_dispatch", HeadSHA: headSHA, HeadBranch: "main", Path: ".github/workflows/apply.yml@main", Name: title, DisplayTitle: title}
		run.Actor.Login, run.Actor.Type = "norn[bot]", "Bot"
		return run
	}
	if err := client.verifyApplyRun(validRun(validTitle), planID, environment, approved, nonce, "norn[bot]"); err != nil {
		t.Fatalf("raw verification rejected correct pilot title: %v", err)
	}
	if err := client.verifyApplyRunByNonceHash(validRun(validTitle), planID, environment, approved, nonceHash, "norn[bot]"); err != nil {
		t.Fatalf("nonce-hash verification rejected correct pilot title: %v", err)
	}

	for name, title := range map[string]string{
		"wrong pilot":   applyRunDisplayTitle(environment, "pilot20260908", planID, nonce),
		"missing pilot": applyRunDisplayTitle(environment, "", planID, nonce),
		"extra pilot":   applyRunDisplayTitle(environment, pilotRunID+" extra", planID, nonce),
		"environment":   applyRunDisplayTitle("disposable/fleet/nyc3", pilotRunID, planID, nonce),
		"plan":          applyRunDisplayTitle(environment, pilotRunID, "other-plan", nonce),
		"nonce":         applyRunDisplayTitle(environment, pilotRunID, planID, strings.Repeat("c", 64)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := client.verifyApplyRun(validRun(title), planID, environment, approved, nonce, "norn[bot]"); err == nil {
				t.Fatal("raw verification accepted tampered pilot title")
			}
			if err := client.verifyApplyRunByNonceHash(validRun(title), planID, environment, approved, nonceHash, "norn[bot]"); err == nil {
				t.Fatal("nonce-hash verification accepted tampered pilot title")
			}
		})
	}
}

func TestVerifyApplyRunAcceptsWorkflowRunNameAndRetainsProtectedIdentity(t *testing.T) {
	planID := "11111111-1111-4111-8111-111111111111"
	nonce := strings.Repeat("a", 64)
	headSHA := strings.Repeat("b", 40)
	title := fmt.Sprintf(applyRunDisplayTitleFormat, "production/nyc3", planID, nonce)
	approved := &Dispatch{ApprovedHeadSHA: headSHA}
	client := testClient(t, http.NotFoundHandler())
	run := &applyRun{
		ID: 93, HTMLURL: "https://github.com/acme/norn-fleet/actions/runs/93",
		Event: "workflow_dispatch", HeadSHA: headSHA, HeadBranch: "main",
		Path: ".github/workflows/apply.yml@main", Name: title, DisplayTitle: title,
	}
	run.Actor.Login, run.Actor.Type = "norn[bot]", "Bot"
	if err := client.verifyApplyRun(run, planID, "production/nyc3", approved, nonce, "norn[bot]"); err != nil {
		t.Fatalf("live workflow run-name rejected: %v", err)
	}

	for name, mutate := range map[string]func(*applyRun){
		"workflow path and ref": func(candidate *applyRun) { candidate.Path = ".github/workflows/apply.yml@feature" },
		"GitHub App actor":      func(candidate *applyRun) { candidate.Actor.Login = "other[bot]" },
		"plan": func(candidate *applyRun) {
			candidate.DisplayTitle = fmt.Sprintf(applyRunDisplayTitleFormat, "production/nyc3", "other-plan", nonce)
			candidate.Name = candidate.DisplayTitle
		},
		"nonce": func(candidate *applyRun) {
			candidate.DisplayTitle = fmt.Sprintf(applyRunDisplayTitleFormat, "production/nyc3", planID, strings.Repeat("c", 64))
			candidate.Name = candidate.DisplayTitle
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *run
			mutate(&candidate)
			if err := client.verifyApplyRun(&candidate, planID, "production/nyc3", approved, nonce, "norn[bot]"); err == nil {
				t.Fatal("tampered workflow run was accepted")
			}
		})
	}
}

func TestObserveApplyRunBindsProtectedIdentityAndTerminalState(t *testing.T) {
	planID := "11111111-1111-4111-8111-111111111111"
	nonce := strings.Repeat("a", 64)
	bound := &Dispatch{RunID: 93, PlanRunID: 91, PlanSHA: strings.Repeat("b", 64), ApprovedHeadSHA: strings.Repeat("c", 40)}
	status, conclusion, attempt := "completed", "cancelled", int64(2)
	latestAttempt := int64(2)
	latestStatus := ""
	responseID := int64(93)
	workflowPath := ".github/workflows/apply.yml@main"
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(w, r, map[string]string{"actions": "read"}) {
			return
		}
		switch r.URL.Path {
		case "/app":
			fmt.Fprint(w, `{"slug":"norn"}`)
		case "/repos/acme/norn-fleet/actions/runs/93", "/repos/acme/norn-fleet/actions/runs/93/attempts/2":
			responseAttempt := attempt
			responseStatus := status
			if r.URL.Path == "/repos/acme/norn-fleet/actions/runs/93" {
				responseAttempt = latestAttempt
				if latestStatus != "" {
					responseStatus = latestStatus
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": responseID, "run_attempt": responseAttempt, "status": responseStatus, "conclusion": conclusion,
				"html_url": fmt.Sprintf("https://github.com/acme/norn-fleet/actions/runs/%d", responseID),
				"event":    "workflow_dispatch", "head_sha": bound.ApprovedHeadSHA,
				"head_branch": "main", "path": workflowPath,
				"name": "apply", "display_title": fmt.Sprintf(applyRunDisplayTitleFormat, "production/nyc3", planID, nonce),
				"inputs": map[string]string{"fleet_environment": "production/nyc3", "plan_run_id": "91", "plan_sha256": bound.PlanSHA, "norn_plan_id": planID, "allow_destructive": "true", "dispatch_nonce": nonce},
				"actor":  map[string]string{"login": "norn[bot]", "type": "Bot"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	observe := func() (*ApplyRunObservation, error) {
		return client.ObserveApplyRun(context.Background(), planID, "production/nyc3", true, bound, nonce)
	}
	observed, err := observe()
	if err != nil || observed.RunID != 93 || observed.RunAttempt != 2 || observed.Status != "completed" || observed.Conclusion != "cancelled" || observed.ObservedAt.IsZero() {
		t.Fatalf("terminal observation=%+v err=%v", observed, err)
	}
	observed, err = client.ObserveApplyRunAttempt(context.Background(), planID, "production/nyc3", true, bound, nonce, 2)
	if err != nil || observed.RunAttempt != 2 || observed.Conclusion != "cancelled" {
		t.Fatalf("numbered attempt observation=%+v err=%v", observed, err)
	}
	latestStatus = "in_progress"
	if _, err := client.ObserveApplyRunAttempt(context.Background(), planID, "production/nyc3", true, bound, nonce, 2); err == nil {
		t.Fatal("disagreeing latest run state was accepted")
	}
	latestStatus = ""
	latestAttempt = 3
	if _, err := client.ObserveApplyRunAttempt(context.Background(), planID, "production/nyc3", true, bound, nonce, 2); err == nil {
		t.Fatal("older attempt was accepted after a rerun")
	}
	latestAttempt = 2
	status, conclusion = "in_progress", ""
	observed, err = observe()
	if err != nil || observed.Status != "in_progress" || observed.Conclusion != "" {
		t.Fatalf("active observation=%+v err=%v", observed, err)
	}
	status = "unrecognized"
	if _, err := observe(); err == nil {
		t.Fatal("unrecognized workflow status was accepted")
	}
	if _, err := client.ObserveApplyRunAttempt(context.Background(), planID, "production/nyc3", true, bound, nonce, 2); err == nil {
		t.Fatal("unrecognized numbered-attempt status was accepted")
	}
	status, conclusion = "completed", "unrecognized"
	if _, err := observe(); err == nil {
		t.Fatal("unrecognized workflow conclusion was accepted")
	}
	status, conclusion, attempt, latestAttempt = "completed", "failure", 0, 0
	if _, err := observe(); err == nil {
		t.Fatal("run without a numbered attempt was accepted")
	}
	attempt, latestAttempt = 2, 2
	if _, err := client.ObserveApplyRun(context.Background(), planID, "production/nyc3", true, bound, strings.Repeat("d", 64)); err == nil {
		t.Fatal("run with a different dispatch nonce was accepted")
	}
	workflowPath = ".github/workflows/apply.yml@feature"
	if _, err := observe(); err == nil {
		t.Fatal("run from another workflow ref was accepted")
	}
	workflowPath = ".github/workflows/apply.yml@main"
	responseID = 94
	if _, err := observe(); err == nil {
		t.Fatal("run returned under a different ID was accepted")
	}
	if _, err := client.ObserveApplyRunAttempt(context.Background(), planID, "production/nyc3", true, bound, nonce, 2); err == nil {
		t.Fatal("numbered attempt returned under a different ID was accepted")
	}
}

func TestReconcilePullRequestClassifiesObservedRemoteState(t *testing.T) {
	const planID = "11111111-1111-4111-8111-111111111111"
	for _, test := range []struct {
		name, pulls string
		refStatus   int
		want        string
	}{
		{name: "verified no write", pulls: `[]`, refStatus: http.StatusNotFound, want: "verified-no-write"},
		{name: "branch without pull request is ambiguous", pulls: `[]`, refStatus: http.StatusOK, want: "ambiguous"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tokenResponse(w, r, map[string]string{"contents": "read", "pull_requests": "read"}) {
					return
				}
				switch {
				case r.URL.Path == "/repos/acme/norn-fleet/pulls":
					fmt.Fprint(w, test.pulls)
				case strings.HasPrefix(r.URL.Path, "/repos/acme/norn-fleet/git/ref/heads/"):
					if test.refStatus == http.StatusOK {
						fmt.Fprint(w, `{"object":{"sha":"0123456789012345678901234567890123456789"}}`)
					} else {
						http.NotFound(w, r)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			got, err := client.ReconcilePullRequest(context.Background(), planID, "sha256:plan", "app", "scale", fleet.NodePool{}, "sha256:source")
			if err != nil || got.Outcome != test.want {
				t.Fatalf("reconciliation=%+v err=%v, want %s", got, err, test.want)
			}
		})
	}
}

func TestReconcilePullRequestRejectsTamperedBranchAndAbsenceRace(t *testing.T) {
	document := []byte("apiVersion: norn.dev/fleet/v1\nkind: Cluster\nmetadata:\n  repository: acme/norn-fleet\n  environment: production\ncluster:\n  name: production-nyc3\n  provider: digitalocean\n  region: nyc3\nnodePools:\n  app:\n    size: s-4vcpu-8gb\n    min: 2\n    desired: 2\n    max: 8\n    replacement:\n      strategy: blueGreen\n      requireCapacityHeadroom: true\n      requireReadiness: true\n")
	parsed, report := fleet.ParseAndValidate(document)
	if parsed == nil || report == nil || !report.Valid {
		t.Fatal("test fleet document invalid")
	}
	const planID = "11111111-1111-4111-8111-111111111111"
	planDigest := "sha256:" + strings.Repeat("a", 64)
	receiptPath, expectedBranch, err := planReviewReceipt(planID, planDigest, fleet.Digest(document), "app", "scale", parsed.NodePools["app"])
	if err != nil {
		t.Fatal(err)
	}
	expectedTitle := "Fleet: scale app"
	expectedBody := "Norn capacity plan `" + planID + "`\n\nDigest: `" + planDigest + "`\n\nDesired topology is already exactly the proposed configuration. This pull request adds the immutable Norn review receipt only; it does not manufacture a topology change. Provider and state credentials remain in the protected runner."
	for _, tc := range []struct {
		name           string
		tampered, race bool
		want           string
	}{{"valid", false, false, "remote-success"}, {"tampered", true, false, "ambiguous"}, {"absence race", false, true, "ambiguous"}} {
		t.Run(tc.name, func(t *testing.T) {
			pullReads := 0
			client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tokenResponse(w, r, map[string]string{"contents": "read", "pull_requests": "read"}) {
					return
				}
				switch {
				case r.URL.Path == "/repos/acme/norn-fleet/pulls":
					pullReads++
					if tc.race && pullReads < 3 {
						fmt.Fprint(w, `[]`)
					} else if tc.race {
						fmt.Fprint(w, `[{"number":42,"state":"open"}]`)
					} else {
						_ = json.NewEncoder(w).Encode([]map[string]interface{}{{"number": 42, "html_url": "https://github.com/acme/norn-fleet/pull/42", "state": "open", "title": expectedTitle, "body": expectedBody, "head": map[string]string{"ref": "norn/plan-" + planID, "sha": "0123456789012345678901234567890123456789"}, "base": map[string]string{"ref": "main"}}})
					}
				case strings.Contains(r.URL.Path, "/contents/"):
					content := document
					if r.URL.Query().Get("ref") != "main" {
						if !strings.Contains(r.URL.Path, receiptPath) {
							http.NotFound(w, r)
							return
						}
						content = expectedBranch
					}
					if tc.tampered && r.URL.Query().Get("ref") != "main" {
						content = []byte("tampered")
					}
					fmt.Fprintf(w, `{"content":%q,"encoding":"base64","sha":"blob","size":%d}`, base64.StdEncoding.EncodeToString(content), len(content))
				case strings.HasPrefix(r.URL.Path, "/repos/acme/norn-fleet/git/ref/heads/"):
					if tc.race {
						http.NotFound(w, r)
					} else {
						fmt.Fprint(w, `{"object":{"sha":"0123456789012345678901234567890123456789"}}`)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			got, err := client.ReconcilePullRequest(context.Background(), planID, planDigest, "app", "scale", parsed.NodePools["app"], fleet.Digest(document))
			if err != nil || got.Outcome != tc.want {
				t.Fatalf("reconciliation=%+v err=%v, want %s", got, err, tc.want)
			}
		})
	}
}

func TestCreatePullRequestClassifiesPrewriteAndExistingBranchFailures(t *testing.T) {
	valid := []byte("apiVersion: norn.dev/fleet/v1\nkind: Cluster\nmetadata:\n  repository: acme/norn-fleet\n  environment: production\ncluster:\n  name: production-nyc3\n  provider: digitalocean\n  region: nyc3\nnodePools:\n  app:\n    size: s-4vcpu-8gb\n    min: 1\n    desired: 1\n    max: 2\n    replacement:\n      strategy: blueGreen\n      requireCapacityHeadroom: true\n      requireReadiness: true\n")
	planID := "44444444-4444-4444-8444-444444444444"
	proposed := fleet.NodePool{Size: "s-4vcpu-8gb", Min: 1, Desired: 2, Max: 2, Replacement: fleet.Replacement{Strategy: "blueGreen", RequireCapacityHeadroom: true, RequireReadiness: true}}
	document, report := fleet.ParseAndValidate(valid)
	if report == nil || !report.Valid {
		t.Fatal("fixture is invalid")
	}
	document.NodePools["app"] = proposed
	updated, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		main         []byte
		branchStatus int
		branch       []byte
		pulls        string
		want         error
		writes       int
	}{
		{"invalid-prewrite", []byte("invalid"), 0, nil, "", ErrPermanentNoWrite, 0},
		{"transient-branch-read", valid, http.StatusInternalServerError, nil, "", nil, 1},
		{"branch-mismatch", valid, http.StatusOK, []byte("different"), "", ErrPermanentAfterMutation, 1},
		{"closed-pr", valid, http.StatusOK, updated, `[{"number":7,"state":"closed","merged_at":null}]`, ErrPermanentAfterMutation, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			pulls := 0
			client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tokenResponse(w, r, map[string]string{"contents": "write", "pull_requests": "write"}) {
					return
				}
				if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
					writes++
				}
				switch {
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/") && strings.Contains(r.URL.RawQuery, "ref=norn%2Fplan-"):
					if tc.branchStatus != http.StatusOK {
						http.Error(w, "temporary", tc.branchStatus)
						return
					}
					fmt.Fprintf(w, `{"content":%q,"encoding":"base64","sha":"x"}`, base64.StdEncoding.EncodeToString(tc.branch))
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
					fmt.Fprintf(w, `{"content":%q,"encoding":"base64","sha":"x"}`, base64.StdEncoding.EncodeToString(tc.main))
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/"):
					fmt.Fprint(w, `{"object":{"sha":"base"}}`)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
					http.Error(w, "exists", http.StatusUnprocessableEntity)
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
					pulls++
					fmt.Fprint(w, tc.pulls)
				default:
					http.NotFound(w, r)
				}
			}))
			_, err := client.CreatePullRequest(context.Background(), planID, "sha256:x", "app", "scale", proposed, fleet.Digest(tc.main))
			if tc.want != nil && !errorsIs(err, tc.want) {
				t.Fatalf("error=%v", err)
			}
			if tc.want == nil && (errorsIs(err, ErrPermanentNoWrite) || errorsIs(err, ErrPermanentAfterMutation)) {
				t.Fatalf("transient error classified permanent: %v", err)
			}
			if writes != tc.writes {
				t.Fatalf("writes=%d want=%d", writes, tc.writes)
			}
			if tc.name == "closed-pr" && pulls != 1 {
				t.Fatalf("closed PR lookup calls=%d", pulls)
			}
		})
	}
}

func TestFleetEnvironmentBindingIsPathAndDocumentScoped(t *testing.T) {
	if got, err := fleetEnvironmentFromConfigPath("environments/staging/nyc3/cluster.yaml"); err != nil || got != "staging/nyc3" {
		t.Fatalf("staging path = %q, %v", got, err)
	}
	if _, err := fleetEnvironmentFromConfigPath("environments/staging/sfo3/cluster.yaml"); err == nil {
		t.Fatal("unsupported fleet root accepted")
	}
	document := &fleet.Document{Metadata: fleet.Metadata{Environment: "staging"}, Cluster: fleet.Cluster{Region: "nyc3"}}
	if got, err := fleetEnvironmentFromDocument(document); err != nil || got != "staging/nyc3" {
		t.Fatalf("document root = %q, %v", got, err)
	}
	document.Metadata.Environment = "production"
	if got, err := fleetEnvironmentFromDocument(document); err != nil || got != "production/nyc3" {
		t.Fatalf("production document root = %q, %v", got, err)
	}
	if got := fleetPlanArtifactName("staging/nyc3", 91); got != "fleet-plan-staging-nyc3-91" {
		t.Fatalf("artifact name = %q", got)
	}
}

func TestPlanArtifactSHARejectsAmbiguousOrUnsafeChecksumEntries(t *testing.T) {
	planSHA := strings.Repeat("a", 64)
	for _, entries := range [][]string{{"fleet-plan.sha256", "fleet-plan.sha256"}, {".fleet-plan.sha256"}, {"nested/fleet-plan.sha256"}, {"../fleet-plan.sha256"}} {
		t.Run(strings.Join(entries, ","), func(t *testing.T) {
			var archive bytes.Buffer
			writer := zip.NewWriter(&archive)
			for _, name := range entries {
				file, err := writer.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = file.Write([]byte(planSHA + "\n"))
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/acme/norn-fleet/actions/runs/91/artifacts":
					fmt.Fprint(w, `{"artifacts":[{"id":92,"name":"fleet-plan-production-nyc3-91","expired":false}]}`)
				case "/repos/acme/norn-fleet/actions/artifacts/92/zip":
					_, _ = w.Write(archive.Bytes())
				default:
					http.NotFound(w, r)
				}
			}))
			if _, err := client.planArtifactSHA(context.Background(), "installation-token", 91, "production/nyc3"); !errorsIs(err, ErrNotReady) {
				t.Fatalf("unsafe artifact entries accepted: %v", err)
			}
		})
	}
}

func TestPlanArtifactSHARejectsDuplicateArtifactNames(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/norn-fleet/actions/runs/91/artifacts" {
			fmt.Fprint(w, `{"artifacts":[{"id":92,"name":"fleet-plan-production-nyc3-91","expired":false},{"id":93,"name":"fleet-plan-production-nyc3-91","expired":false}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	if _, err := client.planArtifactSHA(context.Background(), "installation-token", 91, "production/nyc3"); !errorsIs(err, ErrNotReady) {
		t.Fatalf("duplicate artifacts accepted: %v", err)
	}
}
