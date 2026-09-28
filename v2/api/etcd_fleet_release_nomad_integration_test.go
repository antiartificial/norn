package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	"norn/v2/api/fleetdeploy"
	"norn/v2/api/handler"
	"norn/v2/api/ingress"
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
	spec := "schemaVersion: norn.app/v2\nname: " + app + "\ndeploy: true\nrepo:\n  url: https://github.com/acme/demo\nbuild:\n  image: " + image + "\nprocesses:\n  web:\n    command: mkdir -p /www; printf ready > /www/ready; exec httpd -f -p 8080 -h /www\n    port: 8080\nendpoints:\n  - url: https://" + app + ".example.test\n    region: local\n    process: web\n    trafficProbe:\n      path: /ready\n      bodySHA256: b24d6d33736ecd5604a4b17bc9c6481039fac362bb7df044ef1c10a2bfd21db6\n"
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
	// The optional registry mode uses the release verifier's real OCI digest
	// lookup. Signature and vulnerability policy remain synthetic here;
	// production artifact trust is a separate qualification gate.
	verified := 0
	var verifyArtifact func(context.Context, string) error
	if os.Getenv("NORN_TEST_VERIFY_REGISTRY") != "1" {
		verifyArtifact = func(context.Context, string) error { verified++; return nil }
	}
	verifier := &pipeline.Pipeline{RegistryURL: cfg.RegistryURL, ReleaseAdmissionMode: "keyed", ReleaseAttestationTrustMode: "github-public",
		VerifyArtifact: verifyArtifact, VerifySignature: func(context.Context, string) error { return nil },
		ScanArtifact: func(context.Context, string) error { return nil }}
	router := chi.NewRouter()
	router.Post("/api/v1/apps/{id}/releases/deployments", etcdFleetReleaseDeployment(cfg, operations, verifier))
	body, _ := json.Marshal(map[string]string{"sourceSha": sha, "artifact": image})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/apps/"+app+"/releases/deployments", strings.NewReader(string(body)))
	request.Header.Set("Idempotency-Key", "release-once")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, handler.WithAccessPrincipal(request, &principal))
	expectedSyntheticVerifications := 1
	if verifyArtifact == nil {
		expectedSyntheticVerifications = 0
	}
	if response.Code != http.StatusAccepted || verified != expectedSyntheticVerifications {
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
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("app lock acquired=%v err=%v", acquired, err)
	}
	defer lock.Release()
	listenerCalled := false
	executor := &fleetdeploy.ClaimedFleetDeploymentExecutor{
		Store: operations, Nomad: nomadClient, AppsDir: appsDir, Authority: authority,
		Route: fleetdeploy.ClaimedFleetRouteTransport{
			Listen:       func() (net.Listener, error) { listenerCalled = true; return nil, fmt.Errorf("unexpected listener") },
			ObserverPort: 18082, EndpointPort: 443,
		},
	}
	if _, err := executor.ExecuteOperationWithAppLock(ctx, claimed, claim, lock); err == nil || !strings.Contains(err.Error(), "ingress inventory is unavailable") {
		t.Fatalf("normal executor accepted release without completed ingress inventory: %v", err)
	}
	if listenerCalled {
		t.Fatal("normal executor opened route listener before ingress inventory preflight")
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
	firstAttempt, err := worker.EnsureDeploymentJobEffect(ctx, effects, nomadClient, plan.Reservation, plan.Job)
	if err != nil || !firstAttempt.Attempted || firstAttempt.EffectID == "" {
		t.Fatalf("initial Nomad submission attempt=%+v err=%v", firstAttempt, err)
	}
	if err := operations.DeferClaimedOperation(ctx, claim, "worker stopped after Nomad submit", time.Now().Add(-time.Second), nil); err != nil {
		t.Fatal(err)
	}
	successor, nextClaim, err := operations.ClaimNextOperation(ctx, "successor-fleet-worker", time.Minute, []string{"app.deploy"})
	if err != nil || successor == nil || successor.ID != accepted.ID || nextClaim.Generation() <= claim.Generation() {
		t.Fatalf("successor claim=%+v generation=%d err=%v", successor, nextClaim.Generation(), err)
	}
	source, err = worker.LoadClaimedFleetDeploymentSource(ctx, operations, *successor, appsDir)
	if err != nil {
		t.Fatal(err)
	}
	nextPlan, err := worker.PrepareClaimedFleetDeploymentJob(ctx, source, nextClaim, authority, nomadClient, nil, nil)
	if err != nil || nextPlan.Input.JobDigest != plan.Input.JobDigest || nextPlan.Reservation.SupervisorExecutionID != plan.Reservation.SupervisorExecutionID {
		t.Fatalf("successor reconstructed a different Nomad effect: err=%v", err)
	}
	plan, claim = nextPlan, nextClaim
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
	api, err := nomadapi.NewClient(&nomadapi.Config{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	registeredJob, _, err := api.Jobs().Info(plan.Input.JobID, &nomadapi.QueryOptions{Region: plan.Input.NomadRegion})
	if err != nil || registeredJob == nil || registeredJob.Version == nil || *registeredJob.Version != 0 {
		t.Fatalf("successor resubmitted the Nomad job: version=%v err=%v", registeredJob, err)
	}
	allocations, _, err := api.Jobs().Allocations(plan.Input.JobID, false, &nomadapi.QueryOptions{Region: plan.Input.NomadRegion})
	if err != nil || len(allocations) != 1 || allocations[0].ClientStatus != "running" {
		t.Fatalf("healthy app allocation missing: allocations=%+v err=%v", allocations, err)
	}
	allocation, _, err := api.Allocations().Info(allocations[0].ID, nil)
	if err != nil || allocation == nil || allocation.AllocatedResources == nil {
		t.Fatalf("allocated app resources missing: allocation=%+v err=%v", allocation, err)
	}
	port := 0
	for _, mapping := range allocation.AllocatedResources.Shared.Ports {
		if mapping.Label == "web-http" {
			port = mapping.Value
		}
	}
	if port < 1 {
		t.Fatalf("allocated web-http port missing: %+v", allocation.AllocatedResources.Shared.Ports)
	}
	probeClient := &http.Client{Timeout: 3 * time.Second}
	probe, err := probeClient.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/ready")
	if err != nil {
		t.Fatalf("allocated app endpoint unavailable: %v", err)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(probe.Body, 16))
	_ = probe.Body.Close()
	if readErr != nil || probe.StatusCode != http.StatusOK || string(responseBody) != "ready" {
		t.Fatalf("allocated app endpoint status=%d body=%q err=%v", probe.StatusCode, responseBody, readErr)
	}
	if rawNodes := os.Getenv("NORN_TEST_TRAEFIK_NODES"); rawNodes != "" {
		entries := strings.Split(rawNodes, ";")
		if len(entries) != 2 {
			t.Fatal("Traefik rehearsal requires two loopback ingress nodes")
		}
		nodes := make([]ingress.IngressNode, 0, 2)
		probeNodes := make([]ingress.RouteProbeNode, 0, 2)
		directories := make([]string, 0, 2)
		for index, entry := range entries {
			parts := strings.Split(entry, ",")
			if len(parts) != 3 {
				t.Fatal("Traefik rehearsal node is incomplete")
			}
			nodeID := fmt.Sprintf("local-ingress-%d", index)
			nodes = append(nodes, ingress.IngressNode{ID: nodeID, APIURL: "http://127.0.0.1:" + parts[1]})
			probeNodes = append(probeNodes, ingress.RouteProbeNode{ID: nodeID, DialAddress: "127.0.0.1:" + parts[0]})
			directories = append(directories, parts[2])
		}
		route, err := ingress.RenderWeightedRoute(ingress.WeightedRoute{App: app, Process: "web", Region: "local",
			Endpoint: "https://" + app + ".example.test", Backends: []ingress.WeightedBackend{{DeploymentID: source.Managed.Accepted.Deployment.ID, Weight: 100}}})
		if err != nil {
			t.Fatal(err)
		}
		observe := func(targets []ingress.IngressNode) error {
			_, err := ingress.ObserveRenderedRoute(ctx, &http.Client{Timeout: 3 * time.Second}, targets, route)
			return err
		}
		publish := func(directory string) {
			prior, err := ingress.ReadPublishedRouteRevision(directory, route.RouterName)
			if err != nil {
				t.Fatal(err)
			}
			if err := ingress.PublishRenderedRoute(directory, route, prior, 1); err != nil {
				t.Fatal(err)
			}
		}
		waitObserved := func(targets []ingress.IngressNode) {
			var observedErr error
			for attempt := 0; attempt < 40; attempt++ {
				if observedErr = observe(targets); observedErr == nil {
					return
				}
				time.Sleep(250 * time.Millisecond)
			}
			t.Fatalf("Traefik did not load the deployment route: %v", observedErr)
		}
		publish(directories[0])
		waitObserved(nodes[:1])
		if err := observe(nodes); err == nil {
			t.Fatal("partial ingress publication appeared complete")
		}
		publish(directories[1])
		waitObserved(nodes)
		caPEM, err := os.ReadFile(os.Getenv("NORN_TEST_TRAEFIK_CA_FILE"))
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(caPEM) {
			t.Fatal("Traefik rehearsal certificate is invalid")
		}
		if results, err := ingress.ProbeRenderedRouteNodes(ctx, probeNodes, route, "/ready",
			"b24d6d33736ecd5604a4b17bc9c6481039fac362bb7df044ef1c10a2bfd21db6", roots); err != nil || len(results) != len(nodes) {
			t.Fatalf("two-ingress deployment endpoint proof=%+v err=%v", results, err)
		}
		withdraw := func(directory string) {
			prior, err := ingress.ReadPublishedRouteRevision(directory, route.RouterName)
			if err != nil || !prior.Present {
				t.Fatalf("published route unavailable for withdrawal: %+v err=%v", prior, err)
			}
			if err := ingress.WithdrawPublishedRoute(directory, route.RouterName, prior, 2); err != nil {
				t.Fatal(err)
			}
		}
		observeWithdrawn := func(targets []ingress.IngressNode) error {
			return ingress.ObserveWithdrawnRoute(ctx, &http.Client{Timeout: 3 * time.Second}, targets, route)
		}
		waitWithdrawn := func(targets []ingress.IngressNode) {
			var observedErr error
			for attempt := 0; attempt < 40; attempt++ {
				if observedErr = observeWithdrawn(targets); observedErr == nil {
					return
				}
				time.Sleep(250 * time.Millisecond)
			}
			t.Fatalf("Traefik retained the deployment route after withdrawal: %v", observedErr)
		}
		withdraw(directories[0])
		waitWithdrawn(nodes[:1])
		if err := observeWithdrawn(nodes); err == nil {
			t.Fatal("partial ingress withdrawal appeared complete")
		}
		withdraw(directories[1])
		waitWithdrawn(nodes)
		for _, node := range probeNodes {
			address := node.DialAddress
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
				}}
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+app+".example.test/ready", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatalf("withdrawn ingress %s probe failed: %v", node.ID, err)
			}
			_ = response.Body.Close()
			transport.CloseIdleConnections()
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("withdrawn ingress %s returned HTTP %d, want 404", node.ID, response.StatusCode)
			}
		}
	}
	operation, err := operations.GetOperation(ctx, accepted.ID)
	if err != nil || operation.Status.Terminal() {
		t.Fatalf("Nomad health completed deployment without ingress proof: operation=%+v err=%v", operation, err)
	}
}
