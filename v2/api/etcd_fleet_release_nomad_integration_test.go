package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/effect"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

// This opt-in rehearsal starts with the normal staging HTTP acceptance and
// crosses the claimed worker's real Nomad effect. It deliberately stops before
// ingress: allocation health alone must not complete positive traffic.
func TestEtcdFleetStagingReleaseHTTPToDisposableNomad(t *testing.T) {
	endpoints, address, image := os.Getenv("NORN_TEST_ETCD_ENDPOINTS"), os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_DEPLOYMENT_IMAGE")
	if endpoints == "" || address == "" || image == "" {
		t.Skip("set disposable NORN_TEST_ETCD_ENDPOINTS, NORN_TEST_NOMAD_ADDR and NORN_TEST_DEPLOYMENT_IMAGE")
	}
	for _, endpoint := range strings.Split(endpoints, ",") {
		parsed, err := url.Parse(strings.TrimSpace(endpoint))
		if err != nil || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() {
			t.Fatal("release deployment rehearsal requires loopback etcd")
		}
	}
	parsed, err := url.Parse(address)
	if err != nil || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || !model.IsContentAddressedImage(image) {
		t.Fatal("release deployment rehearsal requires loopback Nomad and a content-addressed image")
	}
	if !strings.HasPrefix(image, "docker.io/library/") {
		t.Fatal("release deployment rehearsal image must use docker.io/library registry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	etcd, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer etcd.Close()
	prefix := "/norn-tests/fleet-release-nomad/" + uuid.NewString()
	defer etcd.Delete(context.Background(), prefix, clientv3.WithPrefix())
	signer, err := store.NewHMACAcceptanceSigner("fleet-release-nomad-test-signing-key-000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := etcdstore.NewV3OperationStore(etcd, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	app := "qual-" + uuid.NewString()[:8]
	appsDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(appsDir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := "schemaVersion: norn.app/v2\nname: " + app + "\ndeploy: true\nrepo:\n  url: https://github.com/acme/demo\nbuild:\n  image: " + image + "\nprocesses:\n  web:\n    command: sleep 60\n    port: 8080\nendpoints:\n  - url: https://" + app + ".example.test\n    region: local\n    process: web\n    trafficProbe:\n      path: /ready\n      bodySHA256: " + strings.Repeat("d", 64) + "\n"
	if err := os.WriteFile(filepath.Join(appsDir, app, "infraspec.yaml"), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	target := store.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: app, ControlEnvironment: "staging",
		Cluster: "norn-staging", FleetEnvironment: "staging/local", Region: "local", NomadRegion: "global",
		Datacenters: []string{"dc1"}, Generation: 1}
	if _, err := operations.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	sha, workflowSHA, signerSHA := strings.Repeat("a", 40), strings.Repeat("c", 40), strings.Repeat("d", 40)
	principal := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, App: app, Environment: "staging", TokenID: "token-a",
		Scopes: []string{handler.ScopeReleaseStage}, CI: &handler.CIIdentity{Provider: "github-actions", Repository: "acme/demo",
			RepositoryID: "11", RepositoryOwnerID: "22", RepositoryVisibility: "public", RunID: "33", RunAttempt: "1",
			WorkflowRef: "acme/demo/.github/workflows/release.yml@refs/heads/main", WorkflowSHA: workflowSHA,
			JobWorkflowRef: "acme/norn/.github/workflows/norn-app-release.yml@" + signerSHA, JobWorkflowSHA: signerSHA,
			Ref: "refs/heads/main", RefProtected: true, EventName: "push", Environment: "staging", Intent: "stage", SHA: sha}}
	cfg := &config.Config{Environment: "staging", EnvironmentExplicit: true, AppsDir: appsDir, RegistryURL: "docker.io/library",
		GitHubActionsDefaultBranch: "main", ReleaseAttestationTrustMode: "github-public"}
	// The synthetic verifier exercises the release binding and claim path;
	// production artifact trust remains a separate qualification gate.
	verified := 0
	verifier := &pipeline.Pipeline{RegistryURL: cfg.RegistryURL, ReleaseAdmissionMode: "keyed", ReleaseAttestationTrustMode: "github-public",
		VerifyArtifact: func(context.Context, string) error { verified++; return nil }, VerifySignature: func(context.Context, string) error { return nil },
		ScanArtifact: func(context.Context, string) error { return nil }}
	router := chi.NewRouter()
	router.Post("/api/v1/apps/{id}/releases/deployments", etcdFleetReleaseDeployment(cfg, operations, verifier))
	body, _ := json.Marshal(map[string]string{"sourceSha": sha, "artifact": image})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/apps/"+app+"/releases/deployments", strings.NewReader(string(body)))
	request.Header.Set("Idempotency-Key", "release-once")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, handler.WithAccessPrincipal(request, &principal))
	if response.Code != http.StatusAccepted || verified != 1 {
		t.Fatalf("release acceptance status=%d verified=%d body=%s", response.Code, verified, response.Body.String())
	}
	var accepted model.Operation
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || accepted.ID == "" {
		t.Fatalf("accepted operation=%+v err=%v", accepted, err)
	}
	claimed, claim, err := operations.ClaimNextOperation(ctx, "local-fleet-worker", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil || claimed.ID != accepted.ID {
		t.Fatalf("claimed accepted operation=%+v err=%v", claimed, err)
	}
	source, err := worker.LoadClaimedFleetDeploymentSource(ctx, operations, *claimed, appsDir)
	if err != nil {
		t.Fatal(err)
	}
	nomadClient, err := nomad.NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := worker.PrepareClaimedFleetDeploymentJob(ctx, source, claim, authority, nomadClient, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		api, err := nomadapi.NewClient(&nomadapi.Config{Address: address})
		if err == nil {
			_, _, _ = api.Jobs().Deregister(plan.Input.JobID, true, &nomadapi.WriteOptions{Region: plan.Input.NomadRegion})
		}
	})
	effects, err := etcdstore.NewV3DeploymentEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	var health effect.Token
	var lastPending error
	for {
		health, err = worker.AdvanceClaimedFleetDeploymentJob(ctx, effects, nomadClient, plan)
		if err == nil {
			break
		}
		var pending *effect.PendingError
		if !errors.As(err, &pending) {
			t.Fatal(err)
		}
		lastPending = err
		select {
		case <-ctx.Done():
			api, apiErr := nomadapi.NewClient(&nomadapi.Config{Address: address})
			if apiErr == nil {
				job, _, jobErr := api.Jobs().Info(plan.Input.JobID, &nomadapi.QueryOptions{Region: plan.Input.NomadRegion})
				evaluations, _, evalErr := api.Jobs().Evaluations(plan.Input.JobID, &nomadapi.QueryOptions{Region: plan.Input.NomadRegion})
				allocations, _, allocErr := api.Jobs().Allocations(plan.Input.JobID, true, &nomadapi.QueryOptions{Region: plan.Input.NomadRegion})
				var jobStatus string
				if job != nil && job.Status != nil {
					jobStatus = *job.Status
				}
				var evaluationStates []string
				for _, evaluation := range evaluations {
					full, _, infoErr := api.Evaluations().Info(evaluation.ID, nil)
					if infoErr == nil && full != nil {
						for group, metric := range full.FailedTGAllocs {
							evaluationStates = append(evaluationStates, evaluation.Status+": "+evaluation.StatusDescription+" group="+group+" metric="+fmt.Sprintf("%+v", *metric))
						}
					} else {
						evaluationStates = append(evaluationStates, evaluation.Status+": "+evaluation.StatusDescription)
					}
				}
				t.Fatalf("Nomad health timed out after %v: jobStatus=%s jobErr=%v evaluations=%v evalErr=%v allocations=%d allocErr=%v", lastPending, jobStatus, jobErr, evaluationStates, evalErr, len(allocations), allocErr)
			}
			t.Fatalf("Nomad health timed out after %v: clientErr=%v", lastPending, apiErr)
		case <-time.After(time.Second):
		}
	}
	if health.EffectID == "" || health.Generation == 0 {
		t.Fatalf("healthy Nomad effect token=%+v", health)
	}
	operation, err := operations.GetOperation(ctx, accepted.ID)
	if err != nil || operation.Status.Terminal() {
		t.Fatalf("Nomad health completed deployment without ingress proof: operation=%+v err=%v", operation, err)
	}
}
