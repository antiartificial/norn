package githubapp

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"norn/v2/api/fleet"
)

func testClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "github-app.pem")
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(Config{
		AppID: "1234", InstallationID: 5678, PrivateKeyFile: keyPath,
		Repository: "acme/norn-fleet", DefaultBranch: "main",
		ConfigPath:   "environments/production/nyc3/cluster.yaml",
		PlanWorkflow: "plan.yml", ApplyWorkflow: "apply.yml", APIBaseURL: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	return client
}

func TestApplyRunDisplayTitleUsesNonceContract(t *testing.T) {
	if got := fmt.Sprintf(applyRunDisplayTitleFormat, "production/nyc3", "plan", strings.Repeat("a", 64)); got != "Apply production/nyc3 Norn plan plan nonce "+strings.Repeat("a", 64) {
		t.Fatalf("server run-name format=%q", got)
	}
}

func tokenResponse(w http.ResponseWriter, r *http.Request, permissions map[string]string) bool {
	if r.URL.Path != "/app/installations/5678/access_tokens" {
		return false
	}
	var claims jwt.MapClaims
	_, _, err := jwt.NewParser().ParseUnverified(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &claims)
	if err != nil || claims["iss"] != "1234" {
		http.Error(w, "bad app jwt", http.StatusUnauthorized)
		return true
	}
	var request struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	_ = json.NewDecoder(r.Body).Decode(&request)
	if len(request.Repositories) != 1 || request.Repositories[0] != "norn-fleet" {
		http.Error(w, "repository token was not narrowed", http.StatusBadRequest)
		return true
	}
	if len(request.Permissions) != len(permissions) {
		http.Error(w, "permission token included an unexpected scope", http.StatusBadRequest)
		return true
	}
	for name, value := range permissions {
		if request.Permissions[name] != value {
			http.Error(w, "permission token was not narrowed", http.StatusBadRequest)
			return true
		}
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"token":"installation-token","expires_at":"2027-01-15T09:00:00Z"}`)
	return true
}

func TestStatusUsesRepositoryScopedShortLivedAuthentication(t *testing.T) {
	tokenRequests := 0
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/5678/access_tokens" {
			tokenRequests++
		}
		if tokenResponse(w, r, map[string]string{"metadata": "read"}) {
			return
		}
		if r.URL.Path == "/repos/acme/norn-fleet" && r.Header.Get("Authorization") == "Bearer installation-token" {
			fmt.Fprint(w, `{"full_name":"acme/norn-fleet"}`)
			return
		}
		http.NotFound(w, r)
	}))
	status := client.Status(context.Background())
	if !status.Configured || !status.Connected || status.Repository != "acme/norn-fleet" {
		t.Fatalf("status = %#v", status)
	}
	if second := client.Status(context.Background()); !second.Connected || tokenRequests != 1 {
		t.Fatalf("status token was not safely reused: connected=%v requests=%d", second.Connected, tokenRequests)
	}
}

func TestCreatePullRequestBindsSourceDigestAndUsesDeterministicBranch(t *testing.T) {
	document := []byte(`apiVersion: norn.dev/fleet/v1
kind: Cluster
metadata:
  repository: acme/norn-fleet
  environment: production
cluster:
  name: production-nyc3
  provider: digitalocean
  region: nyc3
nodePools:
  app:
    size: s-4vcpu-8gb
    min: 2
    desired: 2
    max: 8
    replacement:
      strategy: blueGreen
      requireCapacityHeadroom: true
      requireReadiness: true
`)
	planID := "11111111-1111-4111-8111-111111111111"
	branch := "norn/plan-" + planID
	var updatedDesired int
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(w, r, map[string]string{"contents": "write", "pull_requests": "write"}) {
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			fmt.Fprintf(w, `{"content":%q,"encoding":"base64","sha":"file-sha","size":%d}`, base64.StdEncoding.EncodeToString(document), len(document))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/main"):
			fmt.Fprint(w, `{"object":{"sha":"base-sha"}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["ref"] != "refs/heads/"+branch || body["sha"] != "base-sha" {
				http.Error(w, "bad deterministic ref", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/contents/"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			decoded, _ := base64.StdEncoding.DecodeString(body["content"])
			parsed, _ := fleet.ParseAndValidate(decoded)
			updatedDesired = parsed.NodePools["app"].Desired
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
			fmt.Fprint(w, `[]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			fmt.Fprint(w, `{"number":7,"html_url":"https://github.com/acme/norn-fleet/pull/7","state":"open"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	proposed := fleet.NodePool{Size: "s-4vcpu-8gb", Min: 2, Desired: 3, Max: 8, Replacement: fleet.Replacement{Strategy: "blueGreen", RequireCapacityHeadroom: true, RequireReadiness: true}}
	result, err := client.CreatePullRequest(context.Background(), planID, "sha256:plan", "app", "scale", proposed, fleet.Digest(document))
	if err != nil {
		t.Fatal(err)
	}
	if result.Number != 7 || result.Branch != branch || updatedDesired != 3 {
		t.Fatalf("result=%#v desired=%d", result, updatedDesired)
	}
	_, err = client.CreatePullRequest(context.Background(), planID, "sha256:plan", "app", "scale", proposed, "sha256:"+strings.Repeat("0", 64))
	if !errorsIs(err, ErrStalePlan) {
		t.Fatalf("stale plan error = %v", err)
	}
}

func TestDispatchDiscoversMergedReviewAndBoundPlanArtifact(t *testing.T) {
	planID := "22222222-2222-4222-8222-222222222222"
	planSHA := strings.Repeat("a", 64)
	headSHA := strings.Repeat("b", 40)
	nonce := strings.Repeat("c", 64)
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	entry, _ := zipWriter.Create("fleet-plan.sha256")
	_, _ = entry.Write([]byte(planSHA + "\n"))
	_ = zipWriter.Close()
	var dispatched map[string]any
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app" {
			fmt.Fprint(w, `{"slug":"norn"}`)
			return
		}
		if tokenResponse(w, r, map[string]string{"actions": "write", "contents": "read", "pull_requests": "read"}) {
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/actions/workflows/apply.yml/runs"):
			fmt.Fprint(w, `{"workflow_runs":[]}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
			fmt.Fprint(w, `[{"number":8,"html_url":"https://github.com/acme/norn-fleet/pull/8","state":"closed","merged_at":"2027-01-15T08:00:00Z"}]`)
		case r.URL.Path == "/repos/acme/norn-fleet/pulls/8":
			fmt.Fprintf(w, `{"merge_commit_sha":%q,"merged_at":"2027-01-15T08:00:00Z"}`, headSHA)
		case strings.Contains(r.URL.Path, "/actions/workflows/plan.yml/runs"):
			fmt.Fprintf(w, `{"workflow_runs":[{"id":91,"head_sha":%q,"status":"completed","conclusion":"success"}]}`, headSHA)
		case r.URL.Path == "/repos/acme/norn-fleet/actions/runs/91/artifacts":
			fmt.Fprint(w, `{"artifacts":[{"id":92,"name":"fleet-plan-production-nyc3-91","expired":false}]}`)
		case r.URL.Path == "/repos/acme/norn-fleet/actions/artifacts/92/zip":
			_, _ = w.Write(archive.Bytes())
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/actions/workflows/apply.yml/dispatches"):
			_ = json.NewDecoder(r.Body).Decode(&dispatched)
			fmt.Fprint(w, `{"workflow_run_id":93,"html_url":"https://github.com/acme/norn-fleet/actions/runs/93"}`)
		case r.URL.Path == "/repos/acme/norn-fleet/actions/runs/93":
			fmt.Fprintf(w, `{"id":93,"html_url":"https://github.com/acme/norn-fleet/actions/runs/93","event":"workflow_dispatch","head_sha":%q,"head_branch":"main","path":".github/workflows/apply.yml","name":"apply","display_title":%q,"inputs":{"fleet_environment":"production/nyc3","plan_run_id":"91","plan_sha256":%q,"norn_plan_id":%q,"allow_destructive":"true","dispatch_nonce":%q},"actor":{"login":"norn[bot]","type":"Bot"}}`, headSHA, "Apply production/nyc3 Norn plan "+planID+" nonce "+nonce, planSHA, planID, nonce)
		default:
			http.NotFound(w, r)
		}
	}))
	approved, err := client.ResolveApprovedPlan(context.Background(), planID, "production/nyc3")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.DispatchBoundPlan(context.Background(), planID, "production/nyc3", true, approved, nonce)
	if err != nil {
		t.Fatal(err)
	}
	inputs := dispatched["inputs"].(map[string]any)
	if result.RunID != 93 || inputs["fleet_environment"] != "production/nyc3" || inputs["plan_run_id"] != "91" || inputs["plan_sha256"] != planSHA || inputs["norn_plan_id"] != planID || inputs["allow_destructive"] != "true" || inputs["dispatch_nonce"] != nonce || dispatched["return_run_details"] != true {
		t.Fatalf("result=%#v dispatch=%#v", result, dispatched)
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

func TestFindApplyRunRequiresFullBoundWorkflowIdentity(t *testing.T) {
	planID := "33333333-3333-4333-8333-333333333333"
	nonce := strings.Repeat("d", 64)
	headSHA := strings.Repeat("e", 40)
	approved := &Dispatch{PlanRunID: 91, PlanSHA: strings.Repeat("f", 64), ApprovedHeadSHA: headSHA}
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/actions/workflows/apply.yml/runs") {
			fmt.Fprint(w, `{"workflow_runs":[{"id":99}]}`)
			return
		}
		if r.URL.Path == "/repos/acme/norn-fleet/actions/runs/99" {
			fmt.Fprintf(w, `{"id":99,"html_url":"https://github.com/acme/norn-fleet/actions/runs/99","event":"workflow_dispatch","head_sha":%q,"head_branch":"main","path":".github/workflows/apply.yml","name":"apply","display_title":%q,"inputs":{"fleet_environment":"production/nyc3","plan_run_id":"91","plan_sha256":%q,"norn_plan_id":%q,"allow_destructive":"true","dispatch_nonce":%q},"actor":{"login":"norn[bot]","type":"Bot"}}`, headSHA, "Apply production/nyc3 Norn plan "+planID+" nonce "+nonce, approved.PlanSHA, planID, nonce)
			return
		}
		http.NotFound(w, r)
	}))
	run, err := client.findApplyRun(context.Background(), "installation-token", planID, "production/nyc3", true, approved, nonce, "norn[bot]")
	if err != nil || run == nil || run.RunID != 99 {
		t.Fatalf("environment-bound apply run = %#v, %v", run, err)
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

func TestConfigRejectsTraversalAndNonGitHubProductionAPI(t *testing.T) {
	_, err := New(Config{AppID: "1", InstallationID: 2, PrivateKeyFile: "key", Repository: "acme/fleet", ConfigPath: "../secret.yaml"}, nil)
	if err == nil {
		t.Fatal("path traversal was accepted")
	}
	_, err = New(Config{AppID: "1", InstallationID: 2, PrivateKeyFile: "key", Repository: "acme/fleet", ConfigPath: "fleet.yaml", APIBaseURL: "https://example.test", Production: true}, nil)
	if err == nil {
		t.Fatal("non-GitHub production API was accepted")
	}
}

func errorsIs(err, target error) bool {
	return err == target || (err != nil && strings.Contains(err.Error(), target.Error()))
}
