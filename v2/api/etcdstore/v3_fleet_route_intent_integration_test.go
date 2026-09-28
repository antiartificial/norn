package etcdstore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/effect"
	"norn/v2/api/fleet"
	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func routeAuthorityTestIdentity(t *testing.T, parent *x509.Certificate, signer *ecdsa.PrivateKey, template *x509.Certificate) ([]byte, []byte, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if parent == nil {
		parent, signer = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), cert, key
}

func activeFleetIngressForRouteIntent(t *testing.T, adapter *V3OperationStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	plan := model.Operation{ID: uuid.NewString(), Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Source: "test", Risk: "plan", StartedAt: now, FinishedAt: &now, MaxAttempts: 1,
		Payload: map[string]interface{}{"cluster": "norn-staging", "digest": "route-plan", "action": "scale", "current": map[string]interface{}{"desired": 2}, "proposed": map[string]interface{}{"desired": 3}}}
	plan.Payload["id"] = plan.ID
	planAcceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: plan.Kind, Resource: plan.Ref, Key: "plan-" + plan.ID}, Operation: plan, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.capacity-plan"}}
	var err error
	planAcceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(planAcceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, planAcceptance); err != nil {
		t.Fatal(err)
	}
	github := map[string]interface{}{"planId": plan.ID, "planDigest": "route-plan", "sourceDigest": "", "planRunId": int64(7), "planSha256": strings.Repeat("b", 64), "approvedHeadSha": strings.Repeat("c", 40), "fleetEnvironment": "staging/nyc3", "allowDestructive": false}
	dispatch := model.Operation{ID: uuid.NewString(), Kind: "fleet.github.apply-dispatch", Ref: plan.ID, Status: model.OperationQueued, Source: "test", Risk: "test", StartedAt: now, MaxAttempts: 1, Payload: map[string]interface{}{"fleetGitHub": github}, Metadata: map[string]interface{}{}}
	dispatchRequest := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: adapter.authority + "/fleet-github", Subject: plan.ID}, Kind: dispatch.Kind, Resource: plan.ID, Key: "protected-plan-receipt/v1"}, Operation: dispatch, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"fleetGitHub": github}}
	dispatchRequest.Fingerprint, err = store.CanonicalOperationRequestFingerprint(dispatchRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, prepared, err := adapter.AcceptFleetGitHubDispatch(ctx, dispatchRequest, FleetGitHubDispatchPreparation{PlanID: plan.ID, PlanRunID: 7, PlanSHA256: strings.Repeat("b", 64), ApprovedHeadSHA: strings.Repeat("c", 40), FleetEnvironment: "staging/nyc3"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.FinishFleetGitHubDispatch(ctx, plan.ID, prepared.DispatchNonceSHA256, 7, "https://github.com/acme/fleet/actions/runs/7"); err != nil {
		t.Fatal(err)
	}
	attemptID := uuid.NewString()
	runner := &store.FleetRunnerAttemptAdmission{PlanID: plan.ID, AttemptID: attemptID, RunnerAttemptID: "github:acme/fleet:7:1", CommitSHA: strings.Repeat("c", 40), PlanSHA256: strings.Repeat("b", 64), WorkflowURL: "https://github.com/acme/fleet/actions/runs/7", DispatchNonceSHA256: prepared.DispatchNonceSHA256, SourceDispatchRunID: "7", HeartbeatTimeoutSeconds: 120, WorkloadIntent: "apply", WorkloadRunID: "7", WorkloadSHA: strings.Repeat("c", 40)}
	runnerPayload := map[string]interface{}{"attemptId": attemptID, "planId": plan.ID, "runnerAttemptId": runner.RunnerAttemptID, "commitSha": runner.CommitSHA, "planSha256": runner.PlanSHA256, "workflowUrl": runner.WorkflowURL, "dispatchNonceSha256": runner.DispatchNonceSHA256, "sourceDispatchRunId": "7", "resume": false, "heartbeatTimeoutSeconds": 120}
	runnerRequest := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "runner:7"}, Kind: "fleet.runner-attempt", Resource: plan.ID, Key: "route-attempt"}, Operation: model.Operation{ID: uuid.NewString(), Kind: "fleet.runner-attempt", Ref: plan.ID, Status: model.OperationSucceeded, Source: "fleet-runner", Risk: "bounded protected runner lease", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: runnerPayload, Metadata: map[string]interface{}{}}, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.runner-attempt", "planId": plan.ID, "runnerAttemptId": runner.RunnerAttemptID, "commitSha": runner.CommitSHA, "planSha256": runner.PlanSHA256, "workflowUrl": runner.WorkflowURL, "dispatchNonceSha256": runner.DispatchNonceSHA256, "sourceDispatchRunId": "7", "resume": false, "heartbeatTimeoutSeconds": 120, "workload": map[string]interface{}{"intent": "apply", "runId": "7", "sha": runner.WorkloadSHA}}, FleetRunnerAttempt: runner}
	runnerRequest.Fingerprint, err = store.CanonicalOperationRequestFingerprint(runnerRequest)
	if err != nil {
		t.Fatal(err)
	}
	acceptedRunner, err := adapter.Accept(ctx, runnerRequest)
	if err != nil {
		t.Fatal(err)
	}
	attempt := *acceptedRunner.FleetRunnerAttempt
	snapshot := json.RawMessage(`{"cluster":"norn-staging","environment":"staging/nyc3","ingressNodes":[{"name":"ingress-01","privateIP":"10.43.0.21"},{"name":"ingress-02","privateIP":"10.43.0.22"}],"nodesFileSHA256":"` + strings.Repeat("c", 64) + `","schemaVersion":"norn.fleet-ingress-inventory/v1"}`)
	canonical, err := fleet.CanonicalIngressInventory(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	phases := []string{"prechange_verified", "provider_applying", "infrastructure_applied", "inventory_generated", "nodes_configured", "nodes_enrolled", "readiness_verified", "complete"}
	for index, phase := range phases[:len(phases)-1] {
		checkpoint := fleet.ReconciliationRequest{SchemaVersion: fleet.ReconciliationSchemaVersion, Phase: phase, Status: "succeeded", CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256, AttemptID: attempt.ID, EvidenceDigest: "sha256:" + strings.Repeat("d", 64), StateSerial: 7}
		if phase == "nodes_configured" {
			checkpoint.IngressInventory, checkpoint.IngressInventoryDigest = snapshot, digest
		}
		encoded, err := json.Marshal(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		checkRequest := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "runner:evidence"}, Kind: "fleet.reconciliation", Resource: plan.ID, Key: "route-" + phase}, Operation: model.Operation{ID: uuid.NewString(), Kind: "fleet.reconciliation", Ref: plan.ID, Status: model.OperationSucceeded, Source: "fleet-runner", Risk: "append-only infrastructure reconciliation evidence", StartedAt: now, FinishedAt: &now, MaxAttempts: 1, Payload: payload, Metadata: map[string]interface{}{}}, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"action": "fleet.reconciliation", "planId": plan.ID, "request": checkpoint}, FleetReconciliation: &store.FleetReconciliationAdmission{PlanID: plan.ID, AttemptID: attempt.ID, RequireActiveAttempt: true, RunnerAttemptID: attempt.RunnerAttemptID, WorkflowURL: attempt.WorkflowURL}}
		checkRequest.Fingerprint, err = store.CanonicalOperationRequestFingerprint(checkRequest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Accept(ctx, checkRequest); err != nil {
			t.Fatalf("accept %s: %v", phase, err)
		}
		updated, err := adapter.UpdateFleetRunnerAttempt(ctx, plan.ID, attempt.ID, attempt.Revision, "advance", phases[index+1])
		if err != nil {
			t.Fatalf("advance %s: %v", phase, err)
		}
		attempt = *updated
	}
	if _, err := adapter.CurrentActiveFleetIngressInventory(ctx, "norn-staging", "staging/nyc3", 18082); err != nil {
		t.Fatal(err)
	}
}

func TestInitialFleetRouteIntentIsFencedAndIdempotentEtcd(t *testing.T) {
	runInitialFleetRouteIntentEtcd(t, false)
}

func TestInitialFleetRouteProofTerminalizesSyntheticDeploymentEtcd(t *testing.T) {
	runInitialFleetRouteIntentEtcd(t, true)
}

func runInitialFleetRouteIntentEtcd(t *testing.T, complete bool) {
	adapter, client, _ := deploymentEtcdStore(t)
	ctx := context.Background()
	activeFleetIngressForRouteIntent(t, adapter)
	spec := &model.InfraSpec{App: "demo", Regions: map[string]model.RegionTarget{"west": {NomadRegion: "global", Datacenters: []string{"dc1", "dc2"}}}, Processes: map[string]model.Process{"web": {Port: 8080}}, Endpoints: []model.Endpoint{{URL: "https://demo.example.test", Region: "west", Process: "web", TrafficProbe: &model.TrafficProbeSpec{Path: "/ready", BodySHA256: strings.Repeat("d", 64)}}}}
	request := deploymentAdmissionRequest(t, adapter.authority)
	var err error
	request.Deployment.SpecDigest, err = model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := adapter.ClaimNextOperation(ctx, "route-worker", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, "demo")
	if err != nil || !acquired {
		t.Fatalf("app lock acquired=%v err=%v", acquired, err)
	}
	defer lock.Release()
	staleClaim, err := store.NewOperationClaim(claim.OperationID(), claim.OwnerID(), claim.Generation()+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.IntendInitialFleetRoute(ctx, staleClaim, lock, spec, 18082); err == nil {
		t.Fatal("stale deployment claim reserved a Fleet route")
	}
	activeKey := adapter.fleetActiveRouteKey("demo", "staging", "west")
	if _, err := client.Put(ctx, activeKey, `{"existing":"route"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.IntendInitialFleetRoute(ctx, claim, lock, spec, 18082); err == nil {
		t.Fatal("initial route bypassed a prior active route")
	}
	if _, err := client.Delete(ctx, activeKey); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.CurrentInitialFleetRouteIntent(ctx, claim, lock, spec, 18082); err == nil {
		t.Fatal("publisher read an unreserved route intent")
	}
	first, err := adapter.IntendInitialFleetRoute(ctx, claim, lock, spec, 18082)
	if err != nil || first == nil || first.Generation != 1 || first.DeploymentID != accepted.Deployment.ID || first.Inventory.ActivePointerRevision <= 0 || first.RenderedRoute.SHA256 == "" {
		t.Fatalf("route intent=%+v err=%v", first, err)
	}
	intentKey := adapter.initialFleetRouteKey("demo", "staging", "west", accepted.Deployment.ID)
	indexKey := adapter.initialFleetRouteIDKey(first.ID)
	index, err := client.Get(ctx, indexKey)
	if err != nil || len(index.Kvs) != 1 || string(index.Kvs[0].Value) != intentKey {
		t.Fatalf("intent ID index=%+v err=%v", index, err)
	}
	if _, err := client.Put(ctx, indexKey, "wrong-intent"); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.IntendInitialFleetRoute(ctx, claim, lock, spec, 18082); err == nil {
		t.Fatal("route retry accepted a mismatched intent ID index")
	}
	if _, err := client.Put(ctx, indexKey, intentKey); err != nil {
		t.Fatal(err)
	}
	replayed, err := adapter.IntendInitialFleetRoute(ctx, claim, lock, spec, 18082)
	if err != nil || replayed.ID != first.ID || replayed.Generation != first.Generation {
		t.Fatalf("route intent replay=%+v err=%v", replayed, err)
	}
	if current, err := adapter.CurrentInitialFleetRouteIntent(ctx, claim, lock, spec, 18082); err != nil || current.ID != first.ID {
		t.Fatalf("publishable route intent=%+v err=%v", current, err)
	}
	effects, err := NewV3DeploymentEffectReservations(adapter)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := effects.Reserve(ctx, deploymentEffectReservation(t, accepted, claim, adapter.authority))
	if err != nil || !reserved.Created {
		t.Fatalf("deployment effect reservation=%+v err=%v", reserved, err)
	}
	healthToken := reserved.Record.Token
	if _, err := adapter.AuthorizeInitialFleetRouteForNode(ctx, claim, lock, spec, 18082, healthToken, first.ID, "ingress-01"); err == nil {
		t.Fatal("unhealthy Nomad deployment authorized public route publication")
	}
	if attempted, err := effects.MarkSubmitAttempt(ctx, healthToken); err != nil || !attempted {
		t.Fatalf("Nomad submit attempt=%t err=%v", attempted, err)
	}
	identity := effect.ExecutionIdentity{Supervisor: reserved.Record.Reservation.Supervisor, SupervisorExecutionID: reserved.Record.Reservation.SupervisorExecutionID, RuntimeInstanceID: "nomad-job:global:demo:7"}
	if err := effects.MarkLaunched(ctx, healthToken, identity); err != nil {
		t.Fatal(err)
	}
	verification := effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: reserved.Record.Reservation.InputDigest, ResultDigest: "sha256:healthy-fixture", ResultReference: identity.RuntimeInstanceID,
		SupervisorExecutionID: identity.SupervisorExecutionID, RuntimeInstanceID: identity.RuntimeInstanceID, EvidenceSource: "nomad-job-health", EvidenceReference: identity.RuntimeInstanceID, ObservedAt: time.Now().UTC()}
	if err := effects.Complete(ctx, healthToken, effect.Completion{Outcome: effect.OutcomeSucceeded, Verification: verification}); err != nil {
		t.Fatal(err)
	}
	publication, err := adapter.AuthorizeInitialFleetRouteForNode(ctx, claim, lock, spec, 18082, healthToken, first.ID, "ingress-01")
	if err != nil || publication.IntentID != first.ID || publication.NodeID != "ingress-01" || publication.Generation != 1 || publication.Route.SHA256 != first.RenderedRoute.SHA256 {
		t.Fatalf("node publication=%+v err=%v", publication, err)
	}
	routes := t.TempDir()
	if err := ingress.PublishRenderedRoute(routes, publication.Route, publication.Expected, publication.Generation); err != nil {
		t.Fatalf("authorized first-route local publication: %v", err)
	}
	revision, err := ingress.ReadPublishedRouteRevision(routes, publication.Route.RouterName)
	if err != nil || !revision.Present || revision.Generation != 1 || revision.RouteSHA256 != first.RenderedRoute.SHA256 {
		t.Fatalf("authorized first-route readback=%+v err=%v", revision, err)
	}
	const nodeURI = "spiffe://norn.test/fleet/ingress-01"
	authority, err := adapter.NewClaimedInitialFleetRouteAuthorityHandler(claim, lock, spec, 18082, healthToken, map[string]string{nodeURI: "ingress-01"})
	if err != nil {
		t.Fatal(err)
	}
	authorityRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/v1/route-authorizations", io.NopCloser(strings.NewReader(`{"intentId":"`+first.ID+`"}`)))
		request.Header.Set("Content-Type", "application/json")
		identity, _ := url.Parse(nodeURI)
		request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{URIs: []*url.URL{identity}}}}}
		return request
	}
	authorityResponse := httptest.NewRecorder()
	authority.ServeHTTP(authorityResponse, authorityRequest())
	if authorityResponse.Code != http.StatusOK {
		t.Fatalf("claimed route authority status=%d body=%q", authorityResponse.Code, authorityResponse.Body.String())
	}
	const controlURI = "spiffe://norn.test/control/route-authority"
	now := time.Now()
	rootTemplate := x509.Certificate{SerialNumber: big.NewInt(41), Subject: pkix.Name{CommonName: "route path test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caPEM, _, ca, caKey := routeAuthorityTestIdentity(t, nil, nil, &rootTemplate)
	controlURL, _ := url.Parse(controlURI)
	serverTemplate := x509.Certificate{SerialNumber: big.NewInt(42), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, URIs: []*url.URL{controlURL}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverPEM, serverKey, _, _ := routeAuthorityTestIdentity(t, ca, caKey, &serverTemplate)
	nodeURL, _ := url.Parse(nodeURI)
	clientTemplate := x509.Certificate{SerialNumber: big.NewInt(43), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), URIs: []*url.URL{nodeURL}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientPEM, clientKey, _, _ := routeAuthorityTestIdentity(t, ca, caKey, &clientTemplate)
	privateListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stopAuthority := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() {
		served <- adapter.ServeClaimedInitialFleetRouteAuthority(workerCtx, claim, lock, spec, 18082, healthToken, map[string]string{nodeURI: "ingress-01"}, privateListener, serverPEM, serverKey, caPEM)
	}()
	t.Cleanup(func() {
		stopAuthority()
		_ = privateListener.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("claimed route authority did not stop")
		}
	})
	remoteResolve, err := ingress.NewRemoteRoutePublicationAuthority("https://"+privateListener.Addr().String(), controlURI, caPEM, clientPEM, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	remoteDecision, err := remoteResolve(ctx, first.ID, "ingress-01")
	if err != nil || remoteDecision.Route.SHA256 != first.RenderedRoute.SHA256 || remoteDecision.Generation != 1 {
		t.Fatalf("etcd-to-node mTLS decision=%+v err=%v", remoteDecision, err)
	}
	remoteRoutes := t.TempDir()
	if err := ingress.PublishRenderedRoute(remoteRoutes, remoteDecision.Route, remoteDecision.Expected, remoteDecision.Generation); err != nil {
		t.Fatalf("mTLS-authorized node publication: %v", err)
	}
	remoteRevision, err := ingress.ReadPublishedRouteRevision(remoteRoutes, remoteDecision.Route.RouterName)
	if err != nil || !remoteRevision.Present || remoteRevision.RouteSHA256 != first.RenderedRoute.SHA256 {
		t.Fatalf("mTLS-authorized file revision=%+v err=%v", remoteRevision, err)
	}
	observedRoute := &FleetIngressRouteObservation{Inventory: first.Inventory, RouteSHA256: first.RenderedRoute.SHA256, Generation: first.Generation}
	for _, node := range first.Inventory.Nodes {
		observedRoute.Nodes = append(observedRoute.Nodes, ingress.NodeObservation{NodeID: node.ID, MatchedDesiredRouteSHA256: first.RenderedRoute.SHA256, PublishedGeneration: first.Generation})
	}
	observe := func(_ context.Context, intent *InitialFleetRouteIntent) (*FleetIngressRouteObservation, error) {
		if intent.ID != first.ID || intent.RenderedRoute.SHA256 != first.RenderedRoute.SHA256 {
			t.Fatal("readback used a caller-supplied route instead of the claimed intent")
		}
		return observedRoute, nil
	}
	if current, err := adapter.observeClaimedInitialFleetRoute(ctx, claim, lock, spec, healthToken, 18082, observe); err != nil || current.RouteSHA256 != first.RenderedRoute.SHA256 {
		t.Fatalf("claimed route readback=%+v err=%v", current, err)
	}
	traffic := &FleetIngressTrafficObservation{Route: *observedRoute, ProbePath: "/ready", EndpointBodySHA256: strings.Repeat("d", 64), PublicMatched: true}
	for _, node := range first.Inventory.Nodes {
		traffic.NodeEndpoints = append(traffic.NodeEndpoints, ingress.NodeEndpointProbe{NodeID: node.ID, BodySHA256: traffic.EndpointBodySHA256})
	}
	observeTraffic := func(_ context.Context, intent *InitialFleetRouteIntent) (*FleetIngressTrafficObservation, error) {
		if intent.ID != first.ID {
			t.Fatal("traffic proof used a different intent")
		}
		return traffic, nil
	}
	withoutPublic := *traffic
	withoutPublic.PublicMatched = false
	if _, err := adapter.recordClaimedInitialFleetTrafficProof(ctx, claim, lock, spec, healthToken, 18082, func(context.Context, *InitialFleetRouteIntent) (*FleetIngressTrafficObservation, error) {
		return &withoutPublic, nil
	}); err == nil {
		t.Fatal("missing public-path observation produced durable traffic proof")
	}
	proof, err := adapter.recordClaimedInitialFleetTrafficProof(ctx, claim, lock, spec, healthToken, 18082, observeTraffic)
	if err != nil || proof.IntentID != first.ID || !proof.Observation.PublicMatched {
		t.Fatalf("durable traffic observation=%+v err=%v", proof, err)
	}
	if replay, err := adapter.recordClaimedInitialFleetTrafficProof(ctx, claim, lock, spec, healthToken, 18082, observeTraffic); err != nil || !replay.ObservedAt.Equal(proof.ObservedAt) {
		t.Fatalf("traffic proof retry=%+v err=%v", replay, err)
	}
	storedProof, err := client.Get(ctx, adapter.initialFleetTrafficProofKey(first.ID))
	if err != nil || len(storedProof.Kvs) != 1 {
		t.Fatalf("traffic proof was not stored: entries=%d err=%v", len(storedProof.Kvs), err)
	}
	terminal := *accepted.Deployment
	terminal.Status = model.StatusDeployed
	terminalRegions := []model.DeploymentRegion{{DeploymentID: terminal.ID, Region: first.Region, NomadRegion: first.NomadRegion,
		Status: model.StatusDeployed, DesiredWeight: 100, ActiveWeight: 100}}
	if err := adapter.finishClaimedDeployment(ctx, claim, lock, terminal, terminalRegions, model.OperationSucceeded, "deployed", nil); err == nil || !strings.Contains(err.Error(), "deployment-bound ingress proof") {
		t.Fatalf("observation receipt bypassed terminal traffic fence: %v", err)
	}
	corrupt := *proof
	corrupt.Observation.PublicMatched = false
	corruptBytes, err := json.Marshal(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, adapter.initialFleetTrafficProofKey(first.ID), string(corruptBytes)); err != nil {
		t.Fatal(err)
	}
	if err := adapter.finishClaimedDeployment(ctx, claim, lock, terminal, terminalRegions, model.OperationSucceeded, "deployed", nil, initialFleetCompletionSource{Spec: spec, ObserverPort: 18082}); err == nil {
		t.Fatal("invalid stored public proof authorized terminal active traffic")
	}
	proofBytes, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, adapter.initialFleetTrafficProofKey(first.ID), string(proofBytes)); err != nil {
		t.Fatal(err)
	}
	if complete {
		if err := adapter.finishClaimedDeployment(ctx, claim, lock, terminal, terminalRegions, model.OperationSucceeded, "deployed", nil, initialFleetCompletionSource{Spec: spec, ObserverPort: 18082}); err != nil {
			t.Fatalf("proof-bound terminal deployment: %v", err)
		}
		active, err := client.Get(ctx, activeKey)
		if err != nil || len(active.Kvs) != 1 {
			t.Fatalf("terminal result omitted active route: entries=%d err=%v", len(active.Kvs), err)
		}
		result, err := adapter.GetDeployment(ctx, terminal.ID)
		if err != nil || result.Status != model.StatusDeployed || len(result.Regions) != 1 || result.Regions[0].ActiveWeight != 100 {
			t.Fatalf("proof-bound deployment=%+v err=%v", result, err)
		}
		return
	}
	changedTraffic := *traffic
	changedTraffic.NodeEndpoints = append([]ingress.NodeEndpointProbe(nil), traffic.NodeEndpoints...)
	changedTraffic.EndpointBodySHA256 = strings.Repeat("e", 64)
	for i := range changedTraffic.NodeEndpoints {
		changedTraffic.NodeEndpoints[i].BodySHA256 = changedTraffic.EndpointBodySHA256
	}
	if _, err := adapter.recordClaimedInitialFleetTrafficProof(ctx, claim, lock, spec, healthToken, 18082, func(context.Context, *InitialFleetRouteIntent) (*FleetIngressTrafficObservation, error) {
		return &changedTraffic, nil
	}); err == nil {
		t.Fatal("conflicting traffic observation replaced the immutable proof")
	}
	if _, err := adapter.AuthorizeInitialFleetRouteForNode(ctx, claim, lock, spec, 18082, healthToken, first.ID, "unknown-node"); err == nil {
		t.Fatal("unlisted ingress node received route publication")
	}
	if _, err := adapter.AuthorizeInitialFleetRouteForNode(ctx, claim, lock, spec, 18082, healthToken, uuid.NewString(), "ingress-01"); err == nil {
		t.Fatal("unreserved intent ID received route publication")
	}
	if _, err := adapter.observeClaimedInitialFleetRoute(ctx, claim, lock, spec, healthToken, 18082, func(ctx context.Context, intent *InitialFleetRouteIntent) (*FleetIngressRouteObservation, error) {
		if _, err := client.Put(ctx, adapter.activeFleetIngressClusterEpochKey("norn-staging"), "replaced-plan"); err != nil {
			return nil, err
		}
		return observe(ctx, intent)
	}); err == nil {
		t.Fatal("Fleet inventory replacement during readback produced a claimed observation")
	}
	if _, err := adapter.CurrentInitialFleetRouteIntent(ctx, claim, lock, spec, 18082); err == nil {
		t.Fatal("stale Fleet inventory remained publishable after host replacement")
	}
	if err := adapter.finishClaimedDeployment(ctx, claim, lock, terminal, terminalRegions, model.OperationSucceeded, "deployed", nil, initialFleetCompletionSource{Spec: spec, ObserverPort: 18082}); err == nil {
		t.Fatal("replaced Fleet inventory authorized terminal active traffic")
	}
	if _, err := adapter.AuthorizeInitialFleetRouteForNode(ctx, claim, lock, spec, 18082, healthToken, first.ID, "ingress-01"); err == nil {
		t.Fatal("replaced Fleet inventory still authorized node publication")
	}
	if _, err := remoteResolve(ctx, first.ID, "ingress-01"); err == nil {
		t.Fatal("replaced Fleet inventory still authorized over mTLS")
	}
	authorityResponse = httptest.NewRecorder()
	authority.ServeHTTP(authorityResponse, authorityRequest())
	if authorityResponse.Code != http.StatusConflict {
		t.Fatalf("stale claimed route authority status=%d", authorityResponse.Code)
	}
	if retry, err := adapter.IntendInitialFleetRoute(ctx, claim, lock, spec, 18082); err != nil || retry.ID != first.ID {
		t.Fatalf("replacement allocated a new route generation: %+v err=%v", retry, err)
	}
	changedSpec := *spec
	changedSpec.Endpoints = []model.Endpoint{{URL: "https://forged.example.test", Region: "west", Process: "web"}}
	if _, err := adapter.IntendInitialFleetRoute(ctx, claim, lock, &changedSpec, 18082); err == nil {
		t.Fatal("changed source endpoint reused durable route intent")
	}
	encodedIntent, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	forged := *first
	forged.RenderedRoute = first.RenderedRoute
	forged.RenderedRoute.YAML = []byte("forged route")
	forgedBytes, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, intentKey, string(forgedBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.IntendInitialFleetRoute(ctx, claim, lock, spec, 18082); err == nil {
		t.Fatal("tampered route content was accepted by retry")
	}
	if _, err := client.Put(ctx, intentKey, string(encodedIntent)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, adapter.fleetRouteReservationKey("demo", "staging", "west"), `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.IntendInitialFleetRoute(ctx, claim, lock, spec, 18082); err == nil {
		t.Fatal("corrupt route reservation was accepted")
	}
}
