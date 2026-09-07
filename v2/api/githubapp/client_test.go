package githubapp

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
		Environment:  "production",
		ConfigPath:   "environments/production/nyc3/cluster.yaml",
		PlanWorkflow: "plan.yml", ApplyWorkflow: "apply.yml", APIBaseURL: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	return client
}

func TestPrivateKeyRejectsSymlinkAndNonOwnerOnlyPermissions(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "key.pem")
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "key-link.pem")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Client{cfg: Config{PrivateKeyFile: link}}).privateKey(); err == nil {
		t.Fatal("symlinked Fleet GitHub App key accepted")
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Client{cfg: Config{PrivateKeyFile: path}}).privateKey(); err == nil {
		t.Fatal("group-readable Fleet GitHub App key accepted")
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
	if !status.Configured || !status.Connected || status.Repository != "acme/norn-fleet" || status.Environment != "production" {
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

func TestCreatePullRequestNoDesiredChangeWritesImmutableReceipt(t *testing.T) {
	document := []byte(`apiVersion: norn.dev/fleet/v1
kind: Cluster
metadata:
  repository: acme/norn-fleet
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
	proposed := fleet.NodePool{Size: "s-4vcpu-8gb", Min: 2, Desired: 2, Max: 8, Replacement: fleet.Replacement{Strategy: "blueGreen", RequireCapacityHeadroom: true, RequireReadiness: true}}
	planDigest := "sha256:" + strings.Repeat("a", 64)
	sourceDigest := fleet.Digest(document)
	receipts := map[string][]byte{}
	branches := map[string]bool{}
	pulls := map[string]int{}
	var configWrites int
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(w, r, map[string]string{"contents": "write", "pull_requests": "write"}) {
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/environments/production/nyc3/cluster.yaml"):
			fmt.Fprintf(w, `{"content":%q,"encoding":"base64","sha":"file-sha","size":%d}`, base64.StdEncoding.EncodeToString(document), len(document))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/.norn/fleet-plan-receipts/"):
			content, ok := receipts[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"content":%q,"encoding":"base64","sha":"receipt-sha","size":%d}`, base64.StdEncoding.EncodeToString(content), len(content))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/main"):
			fmt.Fprint(w, `{"object":{"sha":"base-sha"}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			branch := strings.TrimPrefix(body["ref"], "refs/heads/")
			if branches[branch] {
				http.Error(w, "exists", http.StatusUnprocessableEntity)
				return
			}
			branches[branch] = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/contents/.norn/fleet-plan-receipts/"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["sha"]; ok {
				http.Error(w, "new receipt must not send a source file SHA", http.StatusBadRequest)
				return
			}
			content, _ := base64.StdEncoding.DecodeString(body["content"])
			var receipt struct {
				SchemaVersion string         `json:"schemaVersion"`
				PlanID        string         `json:"planID"`
				PlanDigest    string         `json:"planDigest"`
				SourceDigest  string         `json:"sourceDigest"`
				Proposed      fleet.NodePool `json:"proposed"`
			}
			if err := json.Unmarshal(content, &receipt); err != nil || receipt.SchemaVersion != "norn.fleet-plan-review-receipt/v1" || receipt.PlanDigest != planDigest || receipt.SourceDigest != sourceDigest || !reflect.DeepEqual(receipt.Proposed, proposed) {
				http.Error(w, "invalid immutable receipt", http.StatusBadRequest)
				return
			}
			receipts[r.URL.Path] = content
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/contents/"):
			configWrites++
			http.Error(w, "no-op capacity plan must not rewrite the cluster", http.StatusBadRequest)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
			branch := strings.TrimPrefix(r.URL.Query().Get("head"), "acme:")
			if number := pulls[branch]; number > 0 {
				fmt.Fprintf(w, `[{"number":%d,"html_url":"https://github.com/acme/norn-fleet/pull/%d","state":"open","merged_at":null}]`, number, number)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			branch, _ := body["head"].(string)
			pulls[branch] = len(pulls) + 1
			fmt.Fprintf(w, `{"number":%d,"html_url":"https://github.com/acme/norn-fleet/pull/%d","state":"open"}`, pulls[branch], pulls[branch])
		default:
			http.NotFound(w, r)
		}
	}))
	firstPlanID := "33333333-3333-4333-8333-333333333333"
	secondPlanID := "44444444-4444-4444-8444-444444444444"
	first, err := client.CreatePullRequest(context.Background(), firstPlanID, planDigest, "app", "scale", proposed, sourceDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreatePullRequest(context.Background(), firstPlanID, planDigest, "app", "scale", proposed, sourceDigest); err != nil {
		t.Fatalf("durable reuse of same no-op plan failed: %v", err)
	}
	second, err := client.CreatePullRequest(context.Background(), secondPlanID, planDigest, "app", "scale", proposed, sourceDigest)
	if err != nil {
		t.Fatal(err)
	}
	if first.Number == second.Number || len(receipts) != 2 || configWrites != 0 {
		t.Fatalf("receipt PRs=%#v/%#v receipts=%d configWrites=%d", first, second, len(receipts), configWrites)
	}
	if _, err := client.CreatePullRequest(context.Background(), "55555555-5555-4555-8555-555555555555", planDigest, "app", "scale", proposed, "sha256:"+strings.Repeat("0", 64)); !errorsIs(err, ErrStalePlan) {
		t.Fatalf("no-op stale source error = %v", err)
	}
	if len(receipts) != 2 {
		t.Fatalf("stale no-op plan wrote a receipt")
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
			fmt.Fprintf(w, `{"id":93,"html_url":"https://github.com/acme/norn-fleet/actions/runs/93","event":"workflow_dispatch","head_sha":%q,"head_branch":"main","path":".github/workflows/apply.yml@main","name":"apply","display_title":%q,"actor":{"login":"norn[bot]","type":"Bot"}}`, headSHA, "Apply production/nyc3 Norn plan "+planID+" nonce "+nonce)
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
	if result.RunID != 93 || result.ApprovedHeadSHA != headSHA || inputs["fleet_environment"] != "production/nyc3" || inputs["plan_run_id"] != "91" || inputs["plan_sha256"] != planSHA || inputs["norn_plan_id"] != planID || inputs["allow_destructive"] != "true" || inputs["dispatch_nonce"] != nonce || dispatched["return_run_details"] != true {
		t.Fatalf("result=%#v dispatch=%#v", result, dispatched)
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
			fmt.Fprintf(w, `{"id":99,"html_url":"https://github.com/acme/norn-fleet/actions/runs/99","event":"workflow_dispatch","head_sha":%q,"head_branch":"main","path":".github/workflows/apply.yml@refs/heads/main","name":"apply","display_title":%q,"actor":{"login":"norn[bot]","type":"Bot"}}`, headSHA, "Apply production/nyc3 Norn plan "+planID+" nonce "+nonce)
			return
		}
		http.NotFound(w, r)
	}))
	run, err := client.findApplyRun(context.Background(), "installation-token", planID, "production/nyc3", approved, nonce, "norn[bot]")
	if err != nil || run == nil || run.RunID != 99 {
		t.Fatalf("environment-bound apply run = %#v, %v", run, err)
	}
}

func TestDispatchBoundPlanPrefersPersistedExactRun(t *testing.T) {
	planID := "44444444-4444-4444-8444-444444444444"
	nonce := strings.Repeat("a", 64)
	headSHA := strings.Repeat("b", 40)
	workflowURL := "https://github.com/acme/norn-fleet/actions/runs/77"
	approved := &Dispatch{RunID: 77, URL: workflowURL, PlanRunID: 71, PlanSHA: strings.Repeat("c", 64), ApprovedHeadSHA: headSHA}
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app" {
			fmt.Fprint(w, `{"slug":"norn"}`)
			return
		}
		if tokenResponse(w, r, map[string]string{"actions": "write", "contents": "read", "pull_requests": "read"}) {
			return
		}
		if r.URL.Path == "/repos/acme/norn-fleet/actions/runs/77" {
			fmt.Fprintf(w, `{"id":77,"html_url":%q,"event":"workflow_dispatch","head_sha":%q,"head_branch":"main","path":".github/workflows/apply.yml@main","name":"apply","display_title":%q,"actor":{"login":"norn[bot]","type":"Bot"}}`, workflowURL, headSHA, "Apply production/nyc3 Norn plan "+planID+" nonce "+nonce)
			return
		}
		t.Fatalf("unexpected recovery request: %s %s", r.Method, r.URL.Path)
	}))
	result, err := client.DispatchBoundPlan(context.Background(), planID, "production/nyc3", true, approved, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Existing || result.RunID != 77 || result.URL != workflowURL {
		t.Fatalf("persisted dispatch recovery = %#v", result)
	}
}

func TestDispatchLostResponseFindsExactRunWithoutSecondPost(t *testing.T) {
	planID := "55555555-5555-4555-8555-555555555555"
	nonce := strings.Repeat("a", 64)
	headSHA := strings.Repeat("b", 40)
	approved := &Dispatch{PlanRunID: 91, PlanSHA: strings.Repeat("c", 64), ApprovedHeadSHA: headSHA}
	postCount, listCount := 0, 0
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
			listCount++
			if listCount < 3 {
				fmt.Fprint(w, `{"workflow_runs":[]}`)
			} else {
				fmt.Fprint(w, `{"workflow_runs":[{"id":93}]}`)
			}
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/actions/workflows/apply.yml/dispatches"):
			postCount++
			http.Error(w, "temporary gateway failure", http.StatusBadGateway)
		case r.URL.Path == "/repos/acme/norn-fleet/actions/runs/93":
			fmt.Fprintf(w, `{"id":93,"html_url":"https://github.com/acme/norn-fleet/actions/runs/93","event":"workflow_dispatch","head_sha":%q,"head_branch":"main","path":".github/workflows/apply.yml@main","name":"apply","display_title":%q,"actor":{"login":"norn[bot]","type":"Bot"}}`, headSHA, "Apply production/nyc3 Norn plan "+planID+" nonce "+nonce)
		default:
			http.NotFound(w, r)
		}
	}))
	client.pause = func(time.Duration) {}
	result, err := client.DispatchBoundPlan(context.Background(), planID, "production/nyc3", true, approved, nonce)
	if err != nil || result.RunID != 93 || !result.Existing || postCount != 1 {
		t.Fatalf("lost response result=%#v err=%v posts=%d", result, err, postCount)
	}
}

func TestDispatchLostResponseWithoutExactRunFailsClosed(t *testing.T) {
	planID := "66666666-6666-4666-8666-666666666666"
	nonce := strings.Repeat("a", 64)
	approved := &Dispatch{PlanRunID: 91, PlanSHA: strings.Repeat("c", 64), ApprovedHeadSHA: strings.Repeat("b", 40)}
	postCount := 0
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app" {
			fmt.Fprint(w, `{"slug":"norn"}`)
			return
		}
		if tokenResponse(w, r, map[string]string{"actions": "write", "contents": "read", "pull_requests": "read"}) {
			return
		}
		if strings.Contains(r.URL.Path, "/actions/workflows/apply.yml/runs") {
			fmt.Fprint(w, `{"workflow_runs":[]}`)
			return
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/actions/workflows/apply.yml/dispatches") {
			postCount++
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		http.NotFound(w, r)
	}))
	client.pause = func(time.Duration) {}
	if _, err := client.DispatchBoundPlan(context.Background(), planID, "production/nyc3", true, approved, nonce); !errors.Is(err, ErrDispatchAmbiguous) || postCount != 1 {
		t.Fatalf("ambiguous response err=%v posts=%d", err, postCount)
	}
}

func TestRecoverBoundPlanFindsHashMatchedRunAfterCrashWithoutPosting(t *testing.T) {
	planID := "77777777-7777-4777-8777-777777777777"
	nonce := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(nonce))
	nonceHash := hex.EncodeToString(sum[:])
	headSHA := strings.Repeat("b", 40)
	approved := &Dispatch{PlanRunID: 91, PlanSHA: strings.Repeat("c", 64), ApprovedHeadSHA: headSHA}
	listCount, postCount := 0, 0
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
			listCount++
			if listCount == 1 {
				fmt.Fprint(w, `{"workflow_runs":[]}`)
			} else {
				fmt.Fprint(w, `{"workflow_runs":[{"id":92},{"id":93}]}`)
			}
		case r.URL.Path == "/repos/acme/norn-fleet/actions/runs/92":
			fmt.Fprintf(w, `{"id":92,"html_url":"https://github.com/acme/norn-fleet/actions/runs/92","event":"workflow_dispatch","head_sha":%q,"head_branch":"main","path":".github/workflows/apply.yml@main","name":"apply","display_title":%q,"actor":{"login":"norn[bot]","type":"Bot"}}`, headSHA, "Apply production/nyc3 Norn plan "+planID+" nonce "+strings.Repeat("d", 64))
		case r.URL.Path == "/repos/acme/norn-fleet/actions/runs/93":
			fmt.Fprintf(w, `{"id":93,"html_url":"https://github.com/acme/norn-fleet/actions/runs/93","event":"workflow_dispatch","head_sha":%q,"head_branch":"main","path":".github/workflows/apply.yml@main","name":"apply","display_title":%q,"actor":{"login":"norn[bot]","type":"Bot"}}`, headSHA, "Apply production/nyc3 Norn plan "+planID+" nonce "+nonce)
		case r.Method == http.MethodPost:
			postCount++
			t.Fatal("hash-only recovery attempted a second workflow POST")
		default:
			http.NotFound(w, r)
		}
	}))
	client.pause = func(time.Duration) {}
	result, err := client.RecoverBoundPlan(context.Background(), planID, "production/nyc3", approved, nonceHash)
	if err != nil || result.RunID != 93 || !result.Existing || postCount != 0 || listCount < 2 {
		t.Fatalf("crash recovery result=%#v err=%v posts=%d lists=%d", result, err, postCount, listCount)
	}
}

func TestRecoverBoundPlanRejectsMultipleHashMatches(t *testing.T) {
	planID := "88888888-8888-4888-8888-888888888888"
	nonce := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(nonce))
	nonceHash := hex.EncodeToString(sum[:])
	headSHA := strings.Repeat("b", 40)
	approved := &Dispatch{PlanRunID: 91, PlanSHA: strings.Repeat("c", 64), ApprovedHeadSHA: headSHA}
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
			fmt.Fprint(w, `{"workflow_runs":[{"id":93},{"id":94}]}`)
		case r.URL.Path == "/repos/acme/norn-fleet/actions/runs/93", r.URL.Path == "/repos/acme/norn-fleet/actions/runs/94":
			id := strings.TrimPrefix(r.URL.Path, "/repos/acme/norn-fleet/actions/runs/")
			fmt.Fprintf(w, `{"id":%s,"html_url":"https://github.com/acme/norn-fleet/actions/runs/%s","event":"workflow_dispatch","head_sha":%q,"head_branch":"main","path":".github/workflows/apply.yml@main","name":"apply","display_title":%q,"actor":{"login":"norn[bot]","type":"Bot"}}`, id, id, headSHA, "Apply production/nyc3 Norn plan "+planID+" nonce "+nonce)
		case r.Method == http.MethodPost:
			t.Fatal("hash-only recovery attempted a workflow POST")
		default:
			http.NotFound(w, r)
		}
	}))
	client.pause = func(time.Duration) {}
	if _, err := client.RecoverBoundPlan(context.Background(), planID, "production/nyc3", approved, nonceHash); !errors.Is(err, ErrDispatchAmbiguous) {
		t.Fatalf("multiple hash matches err=%v", err)
	}
}

func TestConfigRejectsTraversalAndNonGitHubProductionAPI(t *testing.T) {
	_, err := New(Config{AppID: "1", InstallationID: 2, PrivateKeyFile: "key", Repository: "acme/fleet", Environment: "staging", ConfigPath: "../secret.yaml"}, nil)
	if err == nil {
		t.Fatal("path traversal was accepted")
	}
	_, err = New(Config{AppID: "1", InstallationID: 2, PrivateKeyFile: "key", Repository: "acme/fleet", Environment: "production", ConfigPath: "fleet.yaml", APIBaseURL: "https://example.test", Production: true}, nil)
	if err == nil {
		t.Fatal("non-GitHub production API was accepted")
	}
}

func TestConfigRequiresExplicitFleetEnvironment(t *testing.T) {
	_, err := New(Config{AppID: "1", InstallationID: 2, PrivateKeyFile: "key", Repository: "acme/fleet", ConfigPath: "fleet.yaml"}, nil)
	if err == nil {
		t.Fatal("fleet GitHub configuration without an explicit environment was accepted")
	}
	_, err = New(Config{AppID: "1", InstallationID: 2, PrivateKeyFile: "key", Repository: "acme/fleet", Environment: "development", ConfigPath: "fleet.yaml"}, nil)
	if err == nil {
		t.Fatal("unsupported fleet GitHub environment was accepted")
	}
}

func TestConfigAdmitsOnlyCanonicalRunBoundDisposableFleet(t *testing.T) {
	valid := Config{AppID: "1", InstallationID: 2, PrivateKeyFile: "key", Repository: "acme/fleet", Environment: "staging", PilotRunID: "pilot20260907", ConfigPath: "environments/disposable/fleet/nyc3/cluster.yaml"}
	client, err := New(valid, nil)
	if err != nil || client.fleetRoot() != "disposable/fleet/nyc3" {
		t.Fatalf("canonical disposable config rejected: client=%v err=%v", client, err)
	}
	valid.PilotRunID = "bad-run-id"
	if _, err := New(valid, nil); err == nil {
		t.Fatal("noncanonical disposable run ID accepted")
	}
	valid.PilotRunID = "pilot20260907"
	valid.ConfigPath = "environments/disposable/other/nyc3/cluster.yaml"
	if _, err := New(valid, nil); err == nil {
		t.Fatal("arbitrary disposable root accepted")
	}
}

func TestPilotPlanArtifactRefusesSameHeadDifferentRunBinding(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	sha, _ := writer.Create("fleet-plan.sha256")
	_, _ = sha.Write([]byte(strings.Repeat("a", 64) + "\n"))
	pilot, _ := writer.Create("pilot-run-id.txt")
	_, _ = pilot.Write([]byte("pilot20260908\n"))
	_ = writer.Close()
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/norn-fleet/actions/runs/91/artifacts":
			fmt.Fprint(w, `{"artifacts":[{"id":92,"name":"fleet-plan-disposable-fleet-nyc3-91","expired":false}]}`)
		case "/repos/acme/norn-fleet/actions/artifacts/92/zip":
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	client.cfg.PilotRunID = "pilot20260907"
	if _, err := client.planArtifactSHA(context.Background(), "token", 91, "disposable/fleet/nyc3"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("wrong pilot artifact binding err=%v, want ErrNotReady", err)
	}
}

func errorsIs(err, target error) bool {
	return err == target || (err != nil && strings.Contains(err.Error(), target.Error()))
}
