package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

type workloadReleaseAdmissionStoreFake struct {
	mu                    sync.Mutex
	authority             string
	prior                 *store.AcceptedOperation
	identity              store.OperationRequestIdentity
	qualification         model.ReleaseQualification
	targetInfo            etcdstore.FleetAppTarget
	policyInfo            store.WorkloadReleasePolicy
	routeInfo             etcdstore.WorkloadReleaseServingRoute
	deployment            *model.Deployment
	accepted              *store.AcceptedOperation
	acceptedInput         store.OperationAcceptance
	acceptedQualification model.ReleaseQualification
	acceptedPrivateInput  []byte
	resolve               int
	targetReads           int
	policyReads           int
	route                 int
	qualify               int
	deploymentReads       int
	accept                int
	initialReplayBarrier  chan struct{}
	initialReplayMisses   int
	acceptedOperation     *store.AcceptedOperation
	acceptedIdentity      store.OperationRequestIdentity
	acceptedRequestDigest string
}

func (s *workloadReleaseAdmissionStoreFake) Authority(context.Context) (string, error) {
	return s.authority, nil
}

func (s *workloadReleaseAdmissionStoreFake) ResolvePrivateWorkloadReleaseReplay(_ context.Context,
	identity store.OperationRequestIdentity, operation model.Operation) (store.AcceptedOperation, error) {
	s.mu.Lock()
	s.resolve++
	if s.prior == nil && s.initialReplayBarrier != nil {
		s.initialReplayMisses++
		barrier := s.initialReplayBarrier
		if s.initialReplayMisses == 2 {
			close(barrier)
		}
		s.mu.Unlock()
		select {
		case <-barrier:
			return store.AcceptedOperation{}, store.ErrAcceptanceNotFound
		case <-time.After(5 * time.Second):
			return store.AcceptedOperation{}, errors.New("concurrent replay lookup barrier timed out")
		}
	}
	if s.prior == nil {
		s.mu.Unlock()
		return store.AcceptedOperation{}, store.ErrAcceptanceNotFound
	}
	requestDigest, _ := operation.Metadata["workloadReleaseRequestDigest"].(string)
	priorDigest, _ := s.prior.Operation.Metadata["workloadReleaseRequestDigest"].(string)
	if identity != s.identity || operation.Kind != s.prior.Operation.Kind || operation.App != s.prior.Operation.App || requestDigest != priorDigest {
		s.mu.Unlock()
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: identity}
	}
	prior := *s.prior
	prior.Replayed = true
	s.mu.Unlock()
	return prior, nil
}

func (s *workloadReleaseAdmissionStoreFake) AcceptPrivateWorkloadRelease(_ context.Context,
	input store.OperationAcceptance, qualification model.ReleaseQualification, _ []string, privateInput []byte,
	_ *store.WorkloadReleaseInputKeyRing, _ nomad.ActiveDeploymentLineageObserver) (store.AcceptedOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accept++
	requestDigest, _ := input.Semantics["workloadReleaseRequestDigest"].(string)
	if s.acceptedOperation != nil {
		if input.Identity != s.acceptedIdentity || requestDigest != s.acceptedRequestDigest {
			return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
		}
		accepted := *s.acceptedOperation
		accepted.Replayed = true
		return accepted, nil
	}
	s.acceptedInput = input
	s.acceptedQualification = qualification
	s.acceptedPrivateInput = append([]byte(nil), privateInput...)
	accepted := store.AcceptedOperation{Operation: input.Operation, Intent: store.SignedAcceptanceIntent{CanonicalDigest: "sha256:accepted"}}
	s.accepted = &accepted
	s.acceptedOperation = &accepted
	s.acceptedIdentity = input.Identity
	s.acceptedRequestDigest = requestDigest
	return accepted, nil
}

func (*workloadReleaseAdmissionStoreFake) OpenPrivateWorkloadRelease(context.Context, model.Operation, *store.WorkloadReleaseInputKeyRing) ([]byte, error) {
	return nil, errors.New("not used by admission handler")
}

func (*workloadReleaseAdmissionStoreFake) RequiredWorkloadReleaseInputKeys(context.Context) ([]string, error) {
	return nil, nil
}

func (s *workloadReleaseAdmissionStoreFake) CurrentFleetAppTarget(context.Context, string, string) (etcdstore.FleetAppTarget, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targetReads++
	if s.targetInfo.App == "" {
		return etcdstore.FleetAppTarget{}, 0, errors.New("not expected in replay test")
	}
	return s.targetInfo, 7, nil
}

func (s *workloadReleaseAdmissionStoreFake) CurrentWorkloadReleasePolicy(context.Context, string, string) (store.WorkloadReleasePolicy, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policyReads++
	if s.policyInfo.App == "" {
		return store.WorkloadReleasePolicy{}, 0, errors.New("not expected in replay test")
	}
	return s.policyInfo, 8, nil
}

func (s *workloadReleaseAdmissionStoreFake) CurrentWorkloadReleaseServingRoute(context.Context, string, string, string) (etcdstore.WorkloadReleaseServingRoute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.route++
	if s.routeInfo.DeploymentID == "" {
		return etcdstore.WorkloadReleaseServingRoute{}, errors.New("not expected in replay test")
	}
	return s.routeInfo, nil
}

func (s *workloadReleaseAdmissionStoreFake) ResolveWorkloadReleaseQualification(context.Context, string, string, []string, time.Time) (model.ReleaseQualification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.qualify++
	if s.qualification.ID == "" {
		return model.ReleaseQualification{}, errors.New("not expected in replay test")
	}
	return s.qualification, nil
}

func (s *workloadReleaseAdmissionStoreFake) GetDeployment(context.Context, string) (*model.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deploymentReads++
	if s.deployment == nil {
		return nil, errors.New("not expected in replay test")
	}
	return s.deployment, nil
}

type workloadReleaseAdmissionLineageFake struct {
	mu      sync.Mutex
	lineage nomad.ActiveDeploymentLineage
	calls   int
}

func (f *workloadReleaseAdmissionLineageFake) ObserveActiveDeploymentLineage(context.Context, string, string, string, string,
	string, string, string, string, string, string) (nomad.ActiveDeploymentLineage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.lineage.DeploymentID == "" {
		return nomad.ActiveDeploymentLineage{}, errors.New("not expected in replay test")
	}
	return f.lineage, nil
}

type workloadReleaseAdmissionFixture struct {
	cfg                      *config.Config
	operations               *workloadReleaseAdmissionStoreFake
	lineage                  *workloadReleaseAdmissionLineageFake
	trustedQualificationKeys []string
	encryptedInputs          string
	secretPath               string
	policy                   store.WorkloadReleasePolicy
	route                    etcdstore.WorkloadReleaseServingRoute
}

func newWorkloadReleaseAdmissionFixture(t *testing.T) workloadReleaseAdmissionFixture {
	t.Helper()
	cfg, signingKey := qualificationTestConfig(t)
	appsDir := t.TempDir()
	if err := os.Chmod(appsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(appsDir, "demo")
	if err := os.Mkdir(appDir, 0o750); err != nil {
		t.Fatal(err)
	}
	specYAML := `name: demo
deploy: true
nomadNamespace: default
processes:
  web:
    port: 8080
    command: ./server
    health:
      path: /ready
    nomadVariables:
      uid: 1000
      gid: 1000
      files:
        - key: API_TOKEN
          destination: api-token
endpoints:
  - url: https://demo.example.test
    region: local
    process: web
    trafficProbe:
      path: /health/
      bodySHA256: ` + strings.Repeat("a", 64) + `
    restrictedIngress:
      publicPaths: [/health/]
      retryGETPaths: [/health/]
      privateReadinessPath: /ready
`
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte(specYAML), 0o640); err != nil {
		t.Fatal(err)
	}
	const encryptedInputs = "encrypted-sops-fixture"
	secretPath := filepath.Join(appDir, "secrets.enc.yaml")
	if err := os.WriteFile(secretPath, []byte(encryptedInputs), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.AppsDir = appsDir
	spec, err := model.LoadInfraSpec(filepath.Join(appDir, "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	targetSpecDigest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	deploymentID := uuid.NewString()
	deployment, acceptedDeployment := qualifiedV3DeploymentFixture(t, deploymentID)
	deployment.SpecDigest = targetSpecDigest
	var candidate model.ReleaseCandidate
	candidateJSON, err := json.Marshal(acceptedDeployment.Operation.Payload["candidate"])
	if err != nil || json.Unmarshal(candidateJSON, &candidate) != nil {
		t.Fatalf("decode qualified candidate: %v", err)
	}
	now := time.Now().UTC()
	qualification := model.ReleaseQualification{SchemaVersion: model.ReleaseQualificationSchema, ID: "qualification-1",
		App: "demo", Environment: "staging", DeploymentID: deploymentID, SourceSHA: deployment.CommitSHA,
		Artifact: deployment.ImageTag, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Candidate: candidate}
	if err := handler.SignReleaseQualification(signingKey, &qualification); err != nil {
		t.Fatal(err)
	}
	trustedQualificationKeys, err := handler.ReleaseQualificationVerificationKeys(nil, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	region := spec.ResolvedRegions()[0]
	target := etcdstore.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: spec.App, ControlEnvironment: "staging",
		Cluster: "norn-staging", FleetEnvironment: "staging/local", Region: region.Name, NomadRegion: region.NomadRegion,
		NomadNamespace: spec.NomadNamespace, Datacenters: append([]string(nil), region.Datacenters...), Generation: 4}
	policy := store.WorkloadReleasePolicy{App: spec.App, ControlEnvironment: "staging", FleetTargetGeneration: target.Generation,
		SchemaVersion: store.WorkloadReleasePolicySchema, ApprovalID: "11111111-1111-4111-8111-111111111111", ApprovedBy: "owner",
		ApprovedAt: now.Add(-time.Minute), Mode: store.WorkloadReleasePolicyStatelessOverlap, MinReplicas: 1, MaxReplicas: 4, SurgeLimit: 1,
		RouteSteps: []store.WorkloadReleasePolicyStep{{OldWeight: 100}, {OldWeight: 90, TargetWeight: 10}, {TargetWeight: 100}}}
	digest := func(seed string) string {
		sum := sha256.Sum256([]byte(seed))
		return "sha256:" + hex.EncodeToString(sum[:])
	}
	route := etcdstore.WorkloadReleaseServingRoute{IntentID: "route-old", DeploymentID: "old-deployment", OperationID: "old-operation",
		Generation: 8, JobDigest: digest("old-job"), SpecDigest: digest("old-spec"),
		ImageTag: "registry.example.test/demo@" + digest("old-image"), Process: "web"}
	lineage := &workloadReleaseAdmissionLineageFake{lineage: nomad.ActiveDeploymentLineage{App: spec.App,
		DeploymentID: route.DeploymentID, OperationID: route.OperationID, Region: region.Name, NomadRegion: region.NomadRegion,
		Namespace: spec.NomadNamespace, SpecDigest: route.SpecDigest, ImageTag: route.ImageTag, JobID: "demo-web-old",
		JobDigest: strings.TrimPrefix(route.JobDigest, "sha256:"), JobVersion: 7, ModifyIndex: 55, Process: route.Process,
		Replicas: 1, AllocationIDs: []string{"old-allocation"}}}
	operations := &workloadReleaseAdmissionStoreFake{authority: "authority-1", qualification: qualification,
		targetInfo: target, policyInfo: policy, routeInfo: route, deployment: deployment}
	return workloadReleaseAdmissionFixture{cfg: cfg, operations: operations, lineage: lineage,
		trustedQualificationKeys: trustedQualificationKeys, encryptedInputs: encryptedInputs, secretPath: secretPath,
		policy: policy, route: route}
}

func TestEtcdWorkloadReleaseAdmissionResolvesReplayBeforeMutableEvidence(t *testing.T) {
	const app, qualificationID, key = "demo", "qualification-1", "retry-key"
	digest, err := pipeline.WorkloadReleaseRequestDigest(qualificationID, 2)
	if err != nil {
		t.Fatal(err)
	}
	identity := store.OperationRequestIdentity{Authority: "authority-1", Actor: store.OperationActor{Issuer: "norn://managed-token", Subject: "root-operator"},
		Kind: "app.workload-release", Resource: "app/" + app, Key: key}
	prior := store.AcceptedOperation{Intent: store.SignedAcceptanceIntent{CanonicalDigest: "sha256:prior"},
		Operation: model.Operation{ID: "accepted-release", Kind: identity.Kind, App: app, Status: model.OperationQueued,
			Metadata: map[string]interface{}{"workloadReleaseRequestDigest": digest}}}
	operations := &workloadReleaseAdmissionStoreFake{authority: identity.Authority, prior: &prior, identity: identity}
	response := serveWorkloadReleaseAdmission(t, operations, `{"qualificationId":"qualification-1","targetReplicas":2}`, key, false)
	if response.Code != http.StatusOK || operations.resolve != 1 || operations.accept != 0 || operations.targetReads+operations.policyReads+operations.route+operations.qualify != 0 || !strings.Contains(response.Body.String(), `"replayed":true`) {
		t.Fatalf("exact retry read mutable admission state: code=%d body=%s store=%+v", response.Code, response.Body.String(), operations)
	}
}

func TestEtcdWorkloadReleaseAdmissionRejectsCIPrincipal(t *testing.T) {
	operations := &workloadReleaseAdmissionStoreFake{authority: "authority-1"}
	response := serveWorkloadReleaseAdmission(t, operations, `{"qualificationId":"qualification-1","targetReplicas":2}`, "key", true)
	if response.Code != http.StatusForbidden || operations.resolve != 0 || operations.accept != 0 {
		t.Fatalf("CI principal reached owner-operated admission: code=%d body=%s", response.Code, response.Body.String())
	}
}

func TestEtcdWorkloadReleaseAdmissionRejectsMissingControlEvidenceInOrder(t *testing.T) {
	target := etcdstore.FleetAppTarget{App: "demo", ControlEnvironment: "staging", Region: "local", Generation: 4}
	policy := store.WorkloadReleasePolicy{App: "demo", ControlEnvironment: "staging", FleetTargetGeneration: target.Generation,
		MinReplicas: 1, MaxReplicas: 4}
	cases := []struct {
		name            string
		qualification   model.ReleaseQualification
		target          etcdstore.FleetAppTarget
		policy          store.WorkloadReleasePolicy
		problem         string
		wantTargetReads int
		wantPolicyReads int
		wantRouteReads  int
	}{
		{name: "qualification", problem: "workload_release_qualification_unavailable"},
		{name: "fleet target", qualification: model.ReleaseQualification{ID: "qualification-1"},
			problem: "workload_release_target_unavailable", wantTargetReads: 1},
		{name: "approved policy", qualification: model.ReleaseQualification{ID: "qualification-1"}, target: target,
			problem: "workload_release_policy_unavailable", wantTargetReads: 1, wantPolicyReads: 1},
		{name: "active serving route", qualification: model.ReleaseQualification{ID: "qualification-1"}, target: target, policy: policy,
			problem: "workload_release_route_unavailable", wantTargetReads: 1, wantPolicyReads: 1, wantRouteReads: 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			operations := &workloadReleaseAdmissionStoreFake{authority: "authority-1", qualification: test.qualification,
				targetInfo: test.target, policyInfo: test.policy}
			response := serveWorkloadReleaseAdmission(t, operations,
				`{"qualificationId":"qualification-1","targetReplicas":2}`, "missing-"+strings.ReplaceAll(test.name, " ", "-"), false)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), test.problem) {
				t.Fatalf("missing control evidence response=%d %s, want conflict %q", response.Code, response.Body.String(), test.problem)
			}
			if operations.resolve != 1 || operations.qualify != 1 || operations.targetReads != test.wantTargetReads ||
				operations.policyReads != test.wantPolicyReads || operations.route != test.wantRouteReads ||
				operations.deploymentReads != 0 || operations.accept != 0 {
				t.Fatalf("admission read past the first missing evidence: store=%+v", operations)
			}
		})
	}
}

func TestEtcdWorkloadReleaseAdmissionRejectsMissingServingLineage(t *testing.T) {
	fixture := newWorkloadReleaseAdmissionFixture(t)
	fixture.lineage.lineage = nomad.ActiveDeploymentLineage{}
	response := serveWorkloadReleaseAdmissionWithConfig(t, fixture.cfg, fixture.operations, fixture.lineage,
		fixture.trustedQualificationKeys, `{"qualificationId":"qualification-1","targetReplicas":2}`, "missing-lineage", false)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "workload_release_lineage_unavailable") {
		t.Fatalf("missing serving lineage response=%d %s", response.Code, response.Body.String())
	}
	if fixture.lineage.calls != 1 || fixture.operations.accept != 0 || len(fixture.operations.acceptedPrivateInput) != 0 {
		t.Fatalf("missing serving lineage crossed the acceptance boundary: lineage=%+v operations=%+v", fixture.lineage, fixture.operations)
	}
}

func TestEtcdWorkloadReleaseAdmissionRejectsMissingEncryptedSnapshot(t *testing.T) {
	fixture := newWorkloadReleaseAdmissionFixture(t)
	if err := os.Remove(fixture.secretPath); err != nil {
		t.Fatal(err)
	}
	response := serveWorkloadReleaseAdmissionWithConfig(t, fixture.cfg, fixture.operations, fixture.lineage,
		fixture.trustedQualificationKeys, `{"qualificationId":"qualification-1","targetReplicas":2}`, "missing-snapshot", false)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "workload_release_secret_snapshot_unavailable") {
		t.Fatalf("missing encrypted snapshot response=%d %s", response.Code, response.Body.String())
	}
	if fixture.lineage.calls != 1 || fixture.operations.accept != 0 || len(fixture.operations.acceptedPrivateInput) != 0 {
		t.Fatalf("missing encrypted snapshot crossed the acceptance boundary: lineage=%+v operations=%+v", fixture.lineage, fixture.operations)
	}
}

func TestEtcdWorkloadReleaseAdmissionConcurrentSameKeyConverges(t *testing.T) {
	fixture := newWorkloadReleaseAdmissionFixture(t)
	cfg, operations, lineage := fixture.cfg, fixture.operations, fixture.lineage
	trustedQualificationKeys, encryptedInputs := fixture.trustedQualificationKeys, fixture.encryptedInputs
	qualification := operations.qualification
	policy, route := fixture.policy, fixture.route
	operations.initialReplayBarrier = make(chan struct{})
	type admissionResponse struct {
		status int
		body   string
	}
	responses := make(chan admissionResponse, 2)
	var requests sync.WaitGroup
	for range 2 {
		requests.Add(1)
		go func() {
			defer requests.Done()
			response := serveWorkloadReleaseAdmissionWithConfig(t, cfg, operations, lineage, trustedQualificationKeys,
				`{"qualificationId":"qualification-1","targetReplicas":3}`, "fresh-key", false)
			responses <- admissionResponse{status: response.Code, body: response.Body.String()}
		}()
	}
	requests.Wait()
	close(responses)
	var responseBodies []string
	var acceptedIDs []string
	var freshResponses, replayResponses int
	for response := range responses {
		responseBodies = append(responseBodies, response.body)
		if response.status != http.StatusCreated && response.status != http.StatusOK {
			t.Fatalf("concurrent admission failed: status=%d body=%s", response.status, response.body)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(response.body), &result); err != nil {
			t.Fatal(err)
		}
		operationID, ok := result["operationId"].(string)
		if !ok || operationID == "" {
			t.Fatalf("concurrent admission omitted operation ID: %s", response.body)
		}
		acceptedIDs = append(acceptedIDs, operationID)
		if result["replayed"] == true {
			if response.status != http.StatusOK {
				t.Fatalf("replay response status=%d, want %d: %s", response.status, http.StatusOK, response.body)
			}
			replayResponses++
		} else {
			if response.status != http.StatusCreated {
				t.Fatalf("fresh response status=%d, want %d: %s", response.status, http.StatusCreated, response.body)
			}
			freshResponses++
		}
	}
	if freshResponses != 1 || replayResponses != 1 || len(acceptedIDs) != 2 || acceptedIDs[0] != acceptedIDs[1] ||
		operations.resolve != 2 || operations.qualify != 2 || operations.targetReads != 2 || operations.policyReads != 2 ||
		operations.route != 2 || operations.deploymentReads != 2 || lineage.calls != 2 || operations.accept != 2 ||
		operations.acceptedOperation == nil || operations.acceptedOperation.Operation.ID != acceptedIDs[0] {
		t.Fatalf("simultaneous requests did not converge on one trusted acceptance: fresh=%d replay=%d ids=%v responses=%v store=%+v lineage=%+v",
			freshResponses, replayResponses, acceptedIDs, responseBodies, operations, lineage)
	}
	for _, response := range responseBodies {
		if strings.Contains(response, encryptedInputs) {
			t.Fatalf("admission response leaked private inputs: %s", response)
		}
	}
	if len(operations.acceptedPrivateInput) == 0 {
		t.Fatal("private encrypted inputs were not persisted with the acceptance")
	}
	var plan pipeline.WorkloadReleasePlan
	planJSON, ok := operations.acceptedInput.Operation.Payload["workloadReleasePlan"].(string)
	if !ok || json.Unmarshal([]byte(planJSON), &plan) != nil || plan.Intent.TargetReplicas != 3 ||
		plan.Intent.PolicyApprovalID != policy.ApprovalID || plan.Intent.PolicyDigest == "" ||
		plan.Intent.OldJobVersion != lineage.lineage.JobVersion || plan.Intent.OldJobModifyIndex != lineage.lineage.ModifyIndex ||
		plan.Intent.FirstRouteGeneration != route.Generation+1 || operations.acceptedQualification.ID != qualification.ID {
		t.Fatalf("accepted plan did not bind trusted current inputs: plan=%+v acceptance=%+v", plan, operations.acceptedInput)
	}
}

func TestDeriveWorkloadReleasePlanUsesObservedLineageAndApprovedPolicy(t *testing.T) {
	digest := func(seed string) string {
		sum := sha256.Sum256([]byte(seed))
		return "sha256:" + hex.EncodeToString(sum[:])
	}
	spec := &model.InfraSpec{App: "demo", NomadNamespace: "default", Processes: map[string]model.Process{"web": {
		Port: 8080, Command: "./server", Health: &model.HealthSpec{Path: "/ready"}, NomadVariables: &model.NomadVariableFiles{UID: 1000, GID: 1000,
			Files: []model.NomadVariableFile{{Key: "API_TOKEN", Destination: "api-token"}}},
	}}, Endpoints: []model.Endpoint{{URL: "https://demo.example.test", Region: "local", Process: "web",
		TrafficProbe:      &model.TrafficProbeSpec{Path: "/health/", BodySHA256: strings.Repeat("a", 64)},
		RestrictedIngress: &model.RestrictedIngressSpec{PublicPaths: []string{"/health/"}, RetryGETPaths: []string{"/health/"}, PrivateReadinessPath: "/ready"}}}}
	region := spec.ResolvedRegions()[0]
	target := etcdstore.FleetAppTarget{SchemaVersion: store.FleetAppTargetSchema, App: spec.App, ControlEnvironment: "staging",
		Cluster: "norn-staging", FleetEnvironment: "staging/local", Region: region.Name, NomadRegion: region.NomadRegion,
		NomadNamespace: spec.NomadNamespace, Datacenters: append([]string(nil), region.Datacenters...), Generation: 4}
	policy := store.WorkloadReleasePolicy{App: spec.App, ControlEnvironment: "staging", FleetTargetGeneration: target.Generation,
		SchemaVersion: store.WorkloadReleasePolicySchema, ApprovalID: "11111111-1111-4111-8111-111111111111", ApprovedBy: "owner",
		ApprovedAt: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), Mode: store.WorkloadReleasePolicyStatelessOverlap,
		MinReplicas: 1, MaxReplicas: 4, SurgeLimit: 1,
		RouteSteps: []store.WorkloadReleasePolicyStep{{OldWeight: 100}, {OldWeight: 90, TargetWeight: 10}, {TargetWeight: 100}}}
	policyDigest, err := etcdstore.WorkloadReleasePolicyDigest(policy)
	if err != nil {
		t.Fatal(err)
	}
	route := etcdstore.WorkloadReleaseServingRoute{IntentID: "route-old", DeploymentID: "old-deployment", OperationID: "old-operation",
		Generation: 8, JobDigest: digest("old-job"), SpecDigest: digest("old-spec"),
		ImageTag: "registry.example.test/demo@" + digest("old-image"), Process: "web"}
	lineage := nomad.ActiveDeploymentLineage{App: spec.App, DeploymentID: route.DeploymentID, OperationID: route.OperationID,
		Region: region.Name, NomadRegion: region.NomadRegion, Namespace: spec.NomadNamespace, SpecDigest: route.SpecDigest,
		ImageTag: route.ImageTag, JobID: "demo-web-old", JobDigest: strings.TrimPrefix(route.JobDigest, "sha256:"),
		JobVersion: 7, ModifyIndex: 55, Process: "web", Replicas: 1, AllocationIDs: []string{"old-allocation"}}
	endpoint, err := model.ResolveFleetRouteEndpoint(spec, region.Name)
	if err != nil {
		t.Fatal(err)
	}
	imageDigest := digest("target-image")
	qualification := model.ReleaseQualification{ID: uuid.NewString(), App: spec.App, SourceSHA: strings.Repeat("b", 40),
		Artifact: "registry.example.test/demo@" + imageDigest, Candidate: model.ReleaseCandidate{Attestation: model.ReleaseAttestationIdentity{SubjectDigest: imageDigest}}}
	plan, err := deriveWorkloadReleasePlan(target, policy, policyDigest, route, lineage, endpoint, qualification, 3, spec)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent.OldJobVersion != lineage.JobVersion || plan.Intent.OldJobModifyIndex != lineage.ModifyIndex ||
		!strings.EqualFold(strings.Join(plan.Intent.OldAllocationIDs, ","), "old-allocation") || plan.Intent.TargetReplicas != 3 ||
		plan.Intent.SurgeLimit != policy.SurgeLimit || plan.Intent.PolicyDigest != policyDigest ||
		plan.Intent.FirstRouteGeneration != route.Generation+1 || plan.Intent.TargetJobDigest == "" || plan.Intent.TargetExecutionID == "" {
		t.Fatalf("derived plan did not preserve observed and approved inputs: %+v", plan.Intent)
	}
}

func serveWorkloadReleaseAdmission(t *testing.T, operations workloadReleaseAdmissionStore, body, idempotencyKey string, ci bool) *httptest.ResponseRecorder {
	t.Helper()
	return serveWorkloadReleaseAdmissionWithConfig(t, &config.Config{Environment: "staging"}, operations,
		&workloadReleaseAdmissionLineageFake{}, []string{"trusted-test-key"}, body, idempotencyKey, ci)
}

func serveWorkloadReleaseAdmissionWithConfig(t *testing.T, cfg *config.Config, operations workloadReleaseAdmissionStore,
	lineage *workloadReleaseAdmissionLineageFake, trustedKeys []string, body, idempotencyKey string, ci bool) *httptest.ResponseRecorder {
	t.Helper()
	keys, err := store.NewWorkloadReleaseInputKeyRing("release-input-v1", map[string][]byte{"release-input-v1": []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Post("/api/v1/apps/{id}/workload-releases", etcdWorkloadReleaseAdmission(cfg, operations,
		qualificationTestIdentityStore{}, keys, lineage, trustedKeys))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/workload-releases", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", idempotencyKey)
	principal := handler.AccessPrincipal{TokenID: "operator-token", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeReleaseStage}}
	if ci {
		principal.CI = &handler.CIIdentity{Provider: "github-actions"}
	}
	request = handler.WithAccessPrincipal(request, &principal)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
