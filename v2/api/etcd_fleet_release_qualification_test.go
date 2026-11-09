package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type qualificationIdentity struct {
	value store.OperationRequestIdentity
}

type qualificationTestStore struct {
	authority   string
	deployment  *model.Deployment
	source      store.AcceptedOperation
	acceptances map[qualificationIdentity]store.AcceptedOperation
	byOperation map[string]store.AcceptedOperation
	sourceReads int
	acceptCalls int
}

func newQualificationTestStore(deployment *model.Deployment, source store.AcceptedOperation) *qualificationTestStore {
	return &qualificationTestStore{authority: uuid.NewString(), deployment: deployment, source: source,
		acceptances: map[qualificationIdentity]store.AcceptedOperation{}, byOperation: map[string]store.AcceptedOperation{source.Operation.ID: source}}
}

func (s *qualificationTestStore) Authority(context.Context) (string, error) { return s.authority, nil }

func (s *qualificationTestStore) Accept(_ context.Context, input store.OperationAcceptance) (store.AcceptedOperation, error) {
	s.acceptCalls++
	key := qualificationIdentity{input.Identity}
	if existing, ok := s.acceptances[key]; ok {
		if existing.Intent.Fingerprint != input.Fingerprint {
			return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: input.Identity}
		}
		existing.Replayed = true
		return existing, nil
	}
	accepted := store.AcceptedOperation{Operation: input.Operation, Intent: store.SignedAcceptanceIntent{Fingerprint: input.Fingerprint}}
	s.acceptances[key] = accepted
	s.byOperation[accepted.Operation.ID] = accepted
	return accepted, nil
}

func (s *qualificationTestStore) Resolve(_ context.Context, identity store.OperationRequestIdentity, fingerprint store.RequestFingerprint) (store.AcceptedOperation, error) {
	accepted, err := s.ResolveIdentity(context.Background(), identity)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if accepted.Intent.Fingerprint != fingerprint {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: identity}
	}
	return accepted, nil
}

func (s *qualificationTestStore) ResolveIdentity(_ context.Context, identity store.OperationRequestIdentity) (store.AcceptedOperation, error) {
	accepted, ok := s.acceptances[qualificationIdentity{identity}]
	if !ok {
		return store.AcceptedOperation{}, &store.AcceptanceNotFoundError{Identity: identity}
	}
	accepted.Replayed = true
	return accepted, nil
}

func (s *qualificationTestStore) ResolveAcceptedOperationByID(_ context.Context, id string) (store.AcceptedOperation, error) {
	s.sourceReads++
	accepted, ok := s.byOperation[id]
	if !ok {
		return store.AcceptedOperation{}, errors.New("accepted operation missing")
	}
	return accepted, nil
}

func (s *qualificationTestStore) GetDeployment(_ context.Context, id string) (*model.Deployment, error) {
	if s.deployment == nil || s.deployment.ID != id {
		return nil, errors.New("deployment missing")
	}
	copy := *s.deployment
	return &copy, nil
}

type qualificationTestIdentityStore struct{ store.IdentityStore }

func (qualificationTestIdentityStore) RootAccessToken(context.Context, string) (string, error) {
	return "root-operator", nil
}

func TestEtcdFleetQualificationIssuerUsesSignedV3SourceAndReplaysBeforeSourceRead(t *testing.T) {
	cfg, key := qualificationTestConfig(t)
	deploymentID := uuid.NewString()
	deployment, source := qualifiedV3DeploymentFixture(t, deploymentID)
	operations := newQualificationTestStore(deployment, source)
	principal := handler.AccessPrincipal{TokenID: "rotated-token", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeAPIWrite}}
	identity, err := etcdFleetQualificationIdentity(context.Background(), cfg, operations, qualificationTestIdentityStore{}, principal, deployment.App, "qualify-1")
	if err != nil {
		t.Fatal(err)
	}
	receipt, replayed, err := issueEtcdFleetReleaseQualification(context.Background(), cfg, operations, identity, principal, deployment.App, deploymentID)
	if err != nil || replayed || receipt.DeploymentID != deploymentID || receipt.SourceSHA != deployment.CommitSHA || receipt.Artifact != deployment.ImageTag {
		t.Fatalf("first issue receipt=%+v replayed=%t err=%v", receipt, replayed, err)
	}
	trusted, err := handler.ReleaseQualificationVerificationKeys(nil, key)
	if err != nil || model.VerifyReleaseQualificationSignature(trusted, receipt) != nil {
		t.Fatalf("issued receipt did not verify: err=%v", err)
	}
	if operations.sourceReads != 1 || operations.acceptCalls != 1 {
		t.Fatalf("source reads=%d accepts=%d", operations.sourceReads, operations.acceptCalls)
	}
	replayedReceipt, replayed, err := issueEtcdFleetReleaseQualification(context.Background(), cfg, operations, identity, principal, deployment.App, deploymentID)
	if err != nil || !replayed || replayedReceipt.ID != receipt.ID || operations.sourceReads != 1 || operations.acceptCalls != 1 {
		t.Fatalf("retry receipt=%+v replayed=%t sourceReads=%d accepts=%d err=%v", replayedReceipt, replayed, operations.sourceReads, operations.acceptCalls, err)
	}
	if _, _, err := issueEtcdFleetReleaseQualification(context.Background(), cfg, operations, identity, principal, deployment.App, uuid.NewString()); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("reused idempotency key for another deployment: %v", err)
	}
}

func TestEtcdFleetQualificationIssuerRejectsUnsuccessfulOrUnboundSource(t *testing.T) {
	cfg, _ := qualificationTestConfig(t)
	deploymentID := uuid.NewString()
	deployment, source := qualifiedV3DeploymentFixture(t, deploymentID)
	identity := store.OperationRequestIdentity{Authority: uuid.NewString(), Actor: store.OperationActor{Issuer: "operator", Subject: "root"},
		Kind: "release.qualification", Resource: "app/" + deployment.App, Key: "qualify"}
	principal := handler.AccessPrincipal{TokenID: "operator", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeAPIWrite}}
	for name, mutate := range map[string]func(*model.Deployment, *store.AcceptedOperation){
		"queued deployment":   func(_ *model.Deployment, op *store.AcceptedOperation) { op.Operation.Status = model.OperationQueued },
		"wrong artifact":      func(d *model.Deployment, _ *store.AcceptedOperation) { d.ImageTag = "registry.example.test/app:latest" },
		"missing spec digest": func(d *model.Deployment, _ *store.AcceptedOperation) { d.SpecDigest = "" },
		"missing candidate":   func(_ *model.Deployment, op *store.AcceptedOperation) { delete(op.Operation.Payload, "candidate") },
	} {
		t.Run(name, func(t *testing.T) {
			copyDeployment, copySource := *deployment, source
			copyDeployment.Regions = append([]model.DeploymentRegion(nil), deployment.Regions...)
			copySource.Operation.Payload = clonePayload(source.Operation.Payload)
			mutate(&copyDeployment, &copySource)
			operations := newQualificationTestStore(&copyDeployment, copySource)
			if _, _, err := issueEtcdFleetReleaseQualification(context.Background(), cfg, operations, identity, principal, deployment.App, deploymentID); err == nil {
				t.Fatal("ineligible V3 deployment was qualified")
			}
			if operations.acceptCalls != 0 {
				t.Fatal("ineligible source reached acceptance")
			}
		})
	}
	ciPrincipal := handler.AccessPrincipal{TokenID: "ci-token", Source: handler.AccessPrincipalSourceManagedToken,
		App: deployment.App, Environment: "staging", Scopes: []string{handler.ScopeReleaseQualify},
		CI: &handler.CIIdentity{Provider: "github-actions", Repository: "acme/demo", RepositoryID: "100", RepositoryOwnerID: "200",
			RepositoryVisibility: "public", RunID: "different-run", RunAttempt: "1", WorkflowRef: "acme/demo/.github/workflows/release.yml@refs/heads/main",
			WorkflowSHA: strings.Repeat("c", 40), JobWorkflowRef: "acme/reusable/.github/workflows/release.yml@" + strings.Repeat("d", 40),
			JobWorkflowSHA: strings.Repeat("d", 40), Ref: "refs/heads/main", SHA: strings.Repeat("b", 40), Intent: "qualify"}}
	operations := newQualificationTestStore(deployment, source)
	ciIdentity := identity
	ciIdentity.Actor = store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: "100:different-run:1"}
	if _, _, err := issueEtcdFleetReleaseQualification(context.Background(), cfg, operations, ciIdentity, ciPrincipal, deployment.App, deploymentID); err == nil || operations.acceptCalls != 0 {
		t.Fatalf("mismatched CI identity issued qualification: accepts=%d err=%v", operations.acceptCalls, err)
	}
}

func TestEtcdFleetQualificationActorSurvivesManagedTokenRotation(t *testing.T) {
	cfg, _ := qualificationTestConfig(t)
	operations := &qualificationTestStore{authority: uuid.NewString()}
	identities := qualificationTestIdentityStore{}
	first := handler.AccessPrincipal{TokenID: "token-before-rotation", Source: handler.AccessPrincipalSourceManagedToken}
	second := handler.AccessPrincipal{TokenID: "token-after-rotation", Source: handler.AccessPrincipalSourceManagedToken}
	a, err := etcdFleetQualificationIdentity(context.Background(), cfg, operations, identities, first, "demo", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	b, err := etcdFleetQualificationIdentity(context.Background(), cfg, operations, identities, second, "demo", "same-key")
	if err != nil || a != b {
		t.Fatalf("actor identity changed on token rotation: first=%+v second=%+v err=%v", a, b, err)
	}
}

func TestEtcdFleetQualificationHTTPRejectsUnknownOrTrailingJSON(t *testing.T) {
	cfg, _ := qualificationTestConfig(t)
	deploymentID := uuid.NewString()
	deployment, source := qualifiedV3DeploymentFixture(t, deploymentID)
	operations := newQualificationTestStore(deployment, source)
	principal := &handler.AccessPrincipal{TokenID: "operator", Source: handler.AccessPrincipalSourceManagedToken,
		Scopes: []string{handler.ScopeAPIWrite}}
	router := chi.NewRouter()
	router.Post("/api/v1/apps/{id}/qualifications", etcdFleetReleaseQualification(cfg, operations, qualificationTestIdentityStore{}))
	for _, body := range []string{`{"deploymentId":"` + deploymentID + `","extra":true}`, `{"deploymentId":"` + deploymentID + `"}{}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/qualifications", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "qualify")
		req = handler.WithAccessPrincipal(req, principal)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s returned %d: %s", body, response.Code, response.Body.String())
		}
	}
	if operations.acceptCalls != 0 {
		t.Fatal("invalid JSON reached qualification admission")
	}
}

func qualificationTestConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	seed := sha256.Sum256([]byte("v3-qualification-issuer-test-key"))
	private := ed25519.NewKeyFromSeed(seed[:])
	return &config.Config{Environment: "staging", QualificationSigningKey: base64.RawStdEncoding.EncodeToString(private)},
		base64.RawStdEncoding.EncodeToString(private)
}

func qualifiedV3DeploymentFixture(t *testing.T, id string) (*model.Deployment, store.AcceptedOperation) {
	t.Helper()
	image := "registry.example.test/demo@sha256:" + strings.Repeat("a", 64)
	sha := strings.Repeat("b", 40)
	candidate := model.ReleaseCandidate{Provider: "github-actions", Repository: "acme/demo", RepositoryID: "100", OwnerID: "200",
		RepositoryVisibility: "public", RunID: "17", RunAttempt: "1", WorkflowRef: "acme/demo/.github/workflows/release.yml@refs/heads/main",
		WorkflowSHA: strings.Repeat("c", 40), SignerWorkflowRef: "acme/reusable/.github/workflows/release.yml@" + strings.Repeat("d", 40),
		SignerWorkflowSHA: strings.Repeat("d", 40), Ref: "refs/heads/main", Attestation: model.ReleaseAttestationIdentity{
			Mode: "github-public", Verifier: "Sigstore public-good", Issuer: "https://token.actions.githubusercontent.com",
			SubjectDigest: releaseArtifactDigest(image), MaterialSHA: sha}}
	region := model.DeploymentRegion{DeploymentID: id, Region: "nyc3", NomadRegion: "global", Status: model.StatusDeployed, DesiredWeight: 100, ActiveWeight: 100}
	deployment := &model.Deployment{ID: id, App: "demo", CommitSHA: sha, ImageTag: image, SpecDigest: "sha256:" + strings.Repeat("e", 64),
		Environment: "staging", SagaID: uuid.NewString(), Status: model.StatusDeployed, SourceKind: "release", FinishedAt: timePtr(time.Now().UTC()), Regions: []model.DeploymentRegion{region}}
	candidateJSON, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	var candidateMap map[string]interface{}
	if err := json.Unmarshal(candidateJSON, &candidateMap); err != nil {
		t.Fatal(err)
	}
	op := model.Operation{ID: id, Kind: "app.deploy", App: "demo", Status: model.OperationSucceeded, Source: "release-control-api",
		Payload: map[string]interface{}{"deploymentId": id, "sourceSha": sha, "artifact": image, "specDigest": deployment.SpecDigest, "candidate": candidateMap}}
	return deployment, store.AcceptedOperation{Operation: op}
}

func clonePayload(payload map[string]interface{}) map[string]interface{} {
	encoded, _ := json.Marshal(payload)
	var cloned map[string]interface{}
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}

func timePtr(value time.Time) *time.Time { return &value }
