package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	authority string
	prior     *store.AcceptedOperation
	identity  store.OperationRequestIdentity
	resolve   int
	target    int
	policy    int
	route     int
	qualify   int
	accept    int
}

func (s *workloadReleaseAdmissionStoreFake) Authority(context.Context) (string, error) {
	return s.authority, nil
}

func (s *workloadReleaseAdmissionStoreFake) ResolvePrivateWorkloadReleaseReplay(_ context.Context,
	identity store.OperationRequestIdentity, operation model.Operation) (store.AcceptedOperation, error) {
	s.resolve++
	if s.prior == nil {
		return store.AcceptedOperation{}, store.ErrAcceptanceNotFound
	}
	requestDigest, _ := operation.Metadata["workloadReleaseRequestDigest"].(string)
	priorDigest, _ := s.prior.Operation.Metadata["workloadReleaseRequestDigest"].(string)
	if identity != s.identity || operation.Kind != s.prior.Operation.Kind || operation.App != s.prior.Operation.App || requestDigest != priorDigest {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: identity}
	}
	prior := *s.prior
	prior.Replayed = true
	return prior, nil
}

func (s *workloadReleaseAdmissionStoreFake) AcceptPrivateWorkloadRelease(_ context.Context,
	input store.OperationAcceptance, _ model.ReleaseQualification, _ []string, _ []byte,
	_ *store.WorkloadReleaseInputKeyRing, _ nomad.ActiveDeploymentLineageObserver) (store.AcceptedOperation, error) {
	s.accept++
	return store.AcceptedOperation{Operation: input.Operation, Intent: store.SignedAcceptanceIntent{CanonicalDigest: "sha256:accepted"}}, nil
}

func (*workloadReleaseAdmissionStoreFake) OpenPrivateWorkloadRelease(context.Context, model.Operation, *store.WorkloadReleaseInputKeyRing) ([]byte, error) {
	return nil, errors.New("not used by admission handler")
}

func (*workloadReleaseAdmissionStoreFake) RequiredWorkloadReleaseInputKeys(context.Context) ([]string, error) {
	return nil, nil
}

func (s *workloadReleaseAdmissionStoreFake) CurrentFleetAppTarget(context.Context, string, string) (etcdstore.FleetAppTarget, int64, error) {
	s.target++
	return etcdstore.FleetAppTarget{}, 0, errors.New("not expected in replay test")
}

func (s *workloadReleaseAdmissionStoreFake) CurrentWorkloadReleasePolicy(context.Context, string, string) (store.WorkloadReleasePolicy, int64, error) {
	s.policy++
	return store.WorkloadReleasePolicy{}, 0, errors.New("not expected in replay test")
}

func (s *workloadReleaseAdmissionStoreFake) CurrentWorkloadReleaseServingRoute(context.Context, string, string, string) (etcdstore.WorkloadReleaseServingRoute, error) {
	s.route++
	return etcdstore.WorkloadReleaseServingRoute{}, errors.New("not expected in replay test")
}

func (s *workloadReleaseAdmissionStoreFake) ResolveWorkloadReleaseQualification(context.Context, string, string, []string, time.Time) (model.ReleaseQualification, error) {
	s.qualify++
	return model.ReleaseQualification{}, errors.New("not expected in replay test")
}

func (*workloadReleaseAdmissionStoreFake) GetDeployment(context.Context, string) (*model.Deployment, error) {
	return nil, errors.New("not expected in replay test")
}

type workloadReleaseAdmissionLineageFake struct{}

func (workloadReleaseAdmissionLineageFake) ObserveActiveDeploymentLineage(context.Context, string, string, string, string,
	string, string, string, string, string, string) (nomad.ActiveDeploymentLineage, error) {
	return nomad.ActiveDeploymentLineage{}, errors.New("not expected in replay test")
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
	if response.Code != http.StatusOK || operations.resolve != 1 || operations.accept != 0 || operations.target+operations.policy+operations.route+operations.qualify != 0 || !strings.Contains(response.Body.String(), `"replayed":true`) {
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
	keys, err := store.NewWorkloadReleaseInputKeyRing("release-input-v1", map[string][]byte{"release-input-v1": []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Post("/api/v1/apps/{id}/workload-releases", etcdWorkloadReleaseAdmission(&config.Config{Environment: "staging"}, operations,
		qualificationTestIdentityStore{}, keys, workloadReleaseAdmissionLineageFake{}, []string{"trusted-test-key"}))
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
