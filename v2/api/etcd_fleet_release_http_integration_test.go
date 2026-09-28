package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

func TestEtcdFleetStagingReleaseHTTPAcceptsAndReplaysVerifiedSource(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx := context.Background()
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	prefix := "/norn-tests/fleet-release-http/" + uuid.NewString()
	defer client.Delete(ctx, prefix, clientv3.WithPrefix())
	signer, err := store.NewHMACAcceptanceSigner("fleet-release-http-test-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	appsDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(appsDir, "demo"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := "name: demo\ndeploy: true\nrepo:\n  url: https://github.com/acme/demo\nprocesses:\n  web:\n    command: sleep 1\n    port: 8080\nendpoints:\n  - url: https://demo.example.test\n    region: local\n    process: web\n"
	if err := os.WriteFile(filepath.Join(appsDir, "demo", "infraspec.yaml"), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	target := store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: "demo", ControlEnvironment: "staging",
		Cluster: "norn-staging", FleetEnvironment: "staging/local", Region: "local", NomadRegion: "global",
		Datacenters: []string{"dc1"}, Generation: 1}
	if _, err := operations.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	sha, workflowSHA, signerSHA := strings.Repeat("a", 40), strings.Repeat("c", 40), strings.Repeat("d", 40)
	artifact := "ghcr.io/acme/demo@sha256:" + strings.Repeat("b", 64)
	principal := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, App: "demo", Environment: "staging", TokenID: "token-a",
		Scopes: []string{handler.ScopeReleaseStage}, CI: &handler.CIIdentity{Provider: "github-actions", Repository: "acme/demo",
			RepositoryID: "11", RepositoryOwnerID: "22", RepositoryVisibility: "public", RunID: "33", RunAttempt: "1",
			WorkflowRef: "acme/demo/.github/workflows/release.yml@refs/heads/main", WorkflowSHA: workflowSHA,
			JobWorkflowRef: "acme/norn/.github/workflows/norn-app-release.yml@" + signerSHA, JobWorkflowSHA: signerSHA,
			Ref: "refs/heads/main", RefProtected: true, EventName: "push", Environment: "staging", Intent: "stage", SHA: sha}}
	cfg := &config.Config{Environment: "staging", EnvironmentExplicit: true, AppsDir: appsDir, RegistryURL: "ghcr.io/acme",
		GitHubActionsDefaultBranch: "main", ReleaseAttestationTrustMode: "github-public"}
	verified := 0
	verifier := &pipeline.Pipeline{RegistryURL: cfg.RegistryURL, ReleaseAdmissionMode: "keyed", ReleaseAttestationTrustMode: "github-public",
		VerifyArtifact:  func(context.Context, string) error { verified++; return nil },
		VerifySignature: func(context.Context, string) error { return nil },
		ScanArtifact:    func(context.Context, string) error { return nil }}
	router := chi.NewRouter()
	router.Post("/api/v1/apps/{id}/releases/deployments", etcdFleetReleaseDeployment(cfg, operations, verifier))
	router.Get("/api/v1/operations/{id}", etcdFleetOperation(operations, false, cfg))
	requestBody, _ := json.Marshal(map[string]interface{}{"sourceSha": sha, "artifact": artifact,
		"candidate": map[string]interface{}{"provider": "forged", "repository": "forged/other"}})
	post := func(p handler.AccessPrincipal, key string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/releases/deployments", strings.NewReader(string(body)))
		req.Header.Set("Idempotency-Key", key)
		req = handler.WithAccessPrincipal(req, &p)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	first := post(principal, "release-once", requestBody)
	if first.Code != http.StatusAccepted || verified != 1 {
		t.Fatalf("first release status=%d verified=%d body=%s", first.Code, verified, first.Body.String())
	}
	var accepted struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil || accepted.ID == "" {
		t.Fatalf("accepted operation=%+v err=%v", accepted, err)
	}
	read := func(p handler.AccessPrincipal) *httptest.ResponseRecorder {
		t.Helper()
		req := handler.WithAccessPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+accepted.ID, nil), &p)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	if response := read(principal); response.Code != http.StatusOK {
		t.Fatalf("bound release status read=%d body=%s", response.Code, response.Body.String())
	}
	otherRun := principal
	otherCI := *principal.CI
	otherCI.RunID = "other-run"
	otherRun.CI = &otherCI
	if response := read(otherRun); response.Code != http.StatusNotFound {
		t.Fatalf("unbound release status read=%d body=%s", response.Code, response.Body.String())
	}
	principal.TokenID = "token-rotated"
	target.Generation = 2
	current, revision, err := operations.CurrentFleetAppTarget(ctx, "demo", "staging")
	if err != nil || current.Generation != 1 {
		t.Fatalf("initial target=%+v revision=%d err=%v", current, revision, err)
	}
	if _, err := operations.ConfigureFleetAppTarget(ctx, target, revision); err == nil {
		t.Fatal("Fleet target replacement passed while the release was active")
	}
	replayed := post(principal, "release-once", requestBody)
	if replayed.Code != http.StatusOK || verified != 1 || !strings.Contains(replayed.Body.String(), accepted.ID) {
		t.Fatalf("replay status=%d verified=%d body=%s", replayed.Code, verified, replayed.Body.String())
	}
	changed := strings.Replace(string(requestBody), strings.Repeat("b", 64), strings.Repeat("e", 64), 1)
	if response := post(principal, "release-once", []byte(changed)); response.Code != http.StatusConflict {
		t.Fatalf("changed release status=%d body=%s", response.Code, response.Body.String())
	}
	if verified != 1 {
		t.Fatalf("changed release invoked artifact verifier %d times", verified)
	}
	claimed, claim, err := operations.ClaimNextOperation(ctx, "local-fleet-worker", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil || claimed.ID != accepted.ID {
		t.Fatalf("normal accepted operation claim=%+v err=%v", claimed, err)
	}
	bound, err := worker.LoadClaimedFleetDeploymentSource(ctx, operations, *claimed, appsDir)
	if err != nil || bound.Managed.Accepted.Deployment == nil || bound.Route.Endpoint != "https://demo.example.test" {
		t.Fatalf("claimed signed release source=%+v err=%v", bound, err)
	}
	plan, err := worker.BuildClaimedFleetDeploymentJobPlan(bound, claim, authority)
	if err != nil || plan.Job == nil || plan.Input.DeploymentID != bound.Managed.Accepted.Deployment.ID {
		t.Fatalf("claimed Fleet job plan=%+v err=%v", plan, err)
	}
}
