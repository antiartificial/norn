package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/privateattestation"
	"norn/v2/api/store"
)

type privateRouteSigner struct {
	private ed25519.PrivateKey
	keyID   string
}

func newPrivateRouteSigner(t *testing.T) *privateRouteSigner {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &privateRouteSigner{private: private, keyID: privateattestation.KeyID(public)}
}

func (s *privateRouteSigner) KeyID() string { return s.keyID }
func (s *privateRouteSigner) Sign(_ context.Context, message []byte) (model.DSSESignature, error) {
	return model.DSSESignature{KeyID: s.keyID, Sig: base64.RawStdEncoding.EncodeToString(ed25519.Sign(s.private, message))}, nil
}

func privateRoutePrincipal(app string) AccessPrincipal {
	sha := strings.Repeat("a", 40)
	workflowSHA := strings.Repeat("c", 40)
	return AccessPrincipal{
		Subject: "repo:personal-owner/private-repo:ref:refs/heads/main", TokenID: "private-route-token", Scopes: []string{ScopeReleaseAttest}, App: app, Environment: "staging",
		CI: &CIIdentity{Provider: "github-actions", Repository: "personal-owner/private-repo", RepositoryID: "101", RepositoryOwnerID: "202", RepositoryVisibility: "private", RunID: "303", RunAttempt: "1", WorkflowRef: "personal-owner/private-repo/.github/workflows/release.yml@" + workflowSHA, WorkflowSHA: workflowSHA, JobWorkflowRef: "personal-owner/norn/.github/workflows/norn-app-release.yml@" + workflowSHA, JobWorkflowSHA: workflowSHA, Ref: "refs/heads/main", RefType: "branch", EventName: "push", Environment: "staging", SHA: sha, RefProtected: true, Intent: "attest"},
	}
}

func privateRouteRequestBody(app string, padding int) []byte {
	request := privateAttestationRequest{
		SourceSHA: strings.Repeat("a", 40),
		Artifact:  "registry.example.test/norn/" + app + "@sha256:" + strings.Repeat("b", 64),
		SBOM:      json.RawMessage(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","documentNamespace":"https://example.invalid/spdx/private-route","padding":"` + strings.Repeat("x", padding) + `"}`),
	}
	encoded, _ := json.Marshal(request)
	return encoded
}

func writePrivateRouteSpec(t *testing.T, appsDir, app, repository string) {
	t.Helper()
	dir := filepath.Join(appsDir, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := "name: " + app + "\ndeploy: true\nrepo:\n  url: https://github.com/" + repository + ".git\nbuild:\n  dockerfile: Dockerfile\nprocesses: {}\n"
	if err := os.WriteFile(filepath.Join(dir, "infraspec.yaml"), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
}

func privateRouteHandler(t *testing.T, database *store.DB, environment, mode, app, repository string) *Handler {
	t.Helper()
	appsDir := t.TempDir()
	writePrivateRouteSpec(t, appsDir, app, repository)
	h := &Handler{
		db:                   database,
		cfg:                  &config.Config{Environment: environment, AppsDir: appsDir, ReleaseAttestationTrustMode: mode, GitHubActionsDefaultBranch: "main"},
		pipeline:             &pipeline.Pipeline{RegistryURL: "registry.example.test/norn"},
		privateReleaseSigner: newPrivateRouteSigner(t),
	}
	return h
}

func invokePrivateRoute(h *Handler, app string, principal AccessPrincipal, body []byte, idempotencyKey string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Post("/api/v1/apps/{id}/private-attestations", h.CreatePrivateReleaseAttestation)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/apps/"+app+"/private-attestations", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	request = WithAccessPrincipal(request, &principal)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func requirePrivateRouteProblem(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var problem Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != code {
		t.Fatalf("code=%q want=%q body=%s", problem.Code, code, recorder.Body.String())
	}
}

func TestCreatePrivateReleaseAttestationRejectsWrongEnvironmentAndMode(t *testing.T) {
	const app = "private-route"
	principal := privateRoutePrincipal(app)
	body := privateRouteRequestBody(app, 0)
	for _, test := range []struct {
		name, environment, mode string
	}{
		{name: "development", environment: "development", mode: "norn-signed-private"},
		{name: "production", environment: "production", mode: "norn-signed-private"},
		{name: "GitHub adapter", environment: "staging", mode: "github-private"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := privateRouteHandler(t, nil, test.environment, test.mode, app, "personal-owner/private-repo")
			requirePrivateRouteProblem(t, invokePrivateRoute(h, app, principal, body, "wrong-lane"), http.StatusConflict, "private_attestation_unavailable")
		})
	}
}

func TestQualificationResponsesDisableCaching(t *testing.T) {
	h := &Handler{cfg: &config.Config{Environment: "staging"}}
	readRequest := WithAccessPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/apps/private-route/qualifications", nil), &AccessPrincipal{Scopes: []string{ScopeAPIRead}})
	readRecorder := httptest.NewRecorder()
	h.ListReleaseQualifications(readRecorder, readRequest)
	if readRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("qualification list cache policy=%q", readRecorder.Header().Get("Cache-Control"))
	}
	createRequest := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/apps/private-route/qualifications", nil), &AccessPrincipal{Scopes: []string{ScopeReleaseQualify}, App: "private-route", Environment: "staging"})
	createRecorder := httptest.NewRecorder()
	h.CreateReleaseQualification(createRecorder, createRequest)
	if createRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("qualification create cache policy=%q", createRecorder.Header().Get("Cache-Control"))
	}
	rollbackRecorder := httptest.NewRecorder()
	h.QueueReleaseRollback(rollbackRecorder, httptest.NewRequest(http.MethodPost, "/api/v1/apps/private-route/rollbacks", nil))
	if rollbackRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("rollback cache policy=%q", rollbackRecorder.Header().Get("Cache-Control"))
	}
}

func TestQualificationListIsRecentBoundedAndUnexpired(t *testing.T) {
	now := time.Now().UTC()
	filter := releaseQualificationListFilter("private-route")
	if filter.Limit != maxQualificationListReceipts || !filter.UnexpiredQualification || filter.Kind != "release.qualification" {
		t.Fatalf("qualification store filter does not apply unexpired-before-limit policy: %+v", filter)
	}
	operations := make([]model.Operation, 0, maxQualificationListReceipts+3)
	for index := 0; index < maxQualificationListReceipts+3; index++ {
		receipt := testQualification(t, releaseTestKey(byte('a'+index)))
		receipt.ID = uuid.NewString()
		receipt.ExpiresAt = now.Add(time.Hour)
		if index < maxQualificationListReceipts {
			receipt.ExpiresAt = now.Add(-time.Minute)
		}
		operations = append(operations, model.Operation{Kind: "release.qualification", Payload: qualificationToMap(receipt)})
	}
	qualifications := recentUnexpiredQualifications(operations, now)
	if len(qualifications) != maxQualificationListReceipts {
		t.Fatalf("qualification list count=%d want=%d", len(qualifications), maxQualificationListReceipts)
	}
	for _, receipt := range qualifications {
		if !receipt.ExpiresAt.After(now) {
			t.Fatalf("expired receipt returned: %s", receipt.ID)
		}
	}
}

func TestProductionQualificationListUsesTrustedPromotionEvidence(t *testing.T) {
	key := releaseTestKey('p')
	receipt := testQualification(t, key)
	receipt.Candidate.Repository = "personal-owner/private-repo"
	receipt.Candidate.RepositoryVisibility = "private"
	receipt.Candidate.Attestation.Mode = "norn-signed-private"
	receipt.Candidate.Attestation.Verifier = "Norn private DSSE"
	if err := signReleaseQualification(key, &receipt); err != nil {
		t.Fatal(err)
	}

	trusted := trustedProductionQualifications([]model.ReleaseQualification{receipt}, receipt.App, "norn-signed-private", []string{releaseTestPublicKey('p')})
	if len(trusted) != 1 || trusted[0].ID != receipt.ID {
		t.Fatalf("trusted promoted qualification omitted: %+v", trusted)
	}
	if got := trustedProductionQualifications([]model.ReleaseQualification{receipt}, "another-app", "norn-signed-private", []string{releaseTestPublicKey('p')}); len(got) != 0 {
		t.Fatal("promotion qualification crossed application boundary")
	}
	if got := trustedProductionQualifications([]model.ReleaseQualification{receipt}, receipt.App, "norn-signed-private", []string{releaseTestPublicKey('q')}); len(got) != 0 {
		t.Fatal("promotion qualification signed by an untrusted staging key was displayed")
	}
	if got := trustedProductionQualifications([]model.ReleaseQualification{receipt}, receipt.App, "github-private", []string{releaseTestPublicKey('p')}); len(got) != 0 {
		t.Fatal("promotion qualification from a different trust backend was displayed")
	}
}

func TestCreatePrivateReleaseAttestationRequiresScopedStagingCI(t *testing.T) {
	const app = "private-route"
	body := privateRouteRequestBody(app, 0)
	h := privateRouteHandler(t, nil, "staging", "norn-signed-private", app, "personal-owner/private-repo")

	missingCI := privateRoutePrincipal(app)
	missingCI.CI = nil
	requirePrivateRouteProblem(t, invokePrivateRoute(h, app, missingCI, body, "missing-ci"), http.StatusForbidden, "private_attestation_identity_invalid")

	wrongScope := privateRoutePrincipal(app)
	wrongScope.Scopes = []string{ScopeReleaseStage}
	requirePrivateRouteProblem(t, invokePrivateRoute(h, app, wrongScope, body, "wrong-scope"), http.StatusForbidden, "insufficient_scope")

	wrongTokenApp := privateRoutePrincipal("another-app")
	requirePrivateRouteProblem(t, invokePrivateRoute(h, app, wrongTokenApp, body, "wrong-app"), http.StatusForbidden, "release_token_binding_invalid")

	for name, mutate := range map[string]func(*AccessPrincipal){
		"token environment": func(value *AccessPrincipal) { value.Environment = "production" },
		"CI environment":    func(value *AccessPrincipal) { value.CI.Environment = "production" },
		"intent":            func(value *AccessPrincipal) { value.CI.Intent = "stage" },
		"unprotected ref":   func(value *AccessPrincipal) { value.CI.RefProtected = false },
		"feature ref":       func(value *AccessPrincipal) { value.CI.Ref = "refs/heads/feature" },
		"event":             func(value *AccessPrincipal) { value.CI.EventName = "workflow_dispatch" },
	} {
		t.Run(name, func(t *testing.T) {
			principal := privateRoutePrincipal(app)
			mutate(&principal)
			requirePrivateRouteProblem(t, invokePrivateRoute(h, app, principal, body, "bad-ci-"+strings.ReplaceAll(name, " ", "-")), http.StatusForbidden, "private_attestation_lane_invalid")
		})
	}
}

func TestCreatePrivateReleaseAttestationRejectsSourceAndRepositoryMismatch(t *testing.T) {
	const app = "private-route"
	principal := privateRoutePrincipal(app)
	h := privateRouteHandler(t, nil, "staging", "norn-signed-private", app, "personal-owner/private-repo")

	request := privateAttestationRequest{SourceSHA: strings.Repeat("d", 40), Artifact: "registry.example.test/norn/" + app + "@sha256:" + strings.Repeat("b", 64), SBOM: json.RawMessage(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","documentNamespace":"https://example.invalid/spdx"}`)}
	body, _ := json.Marshal(request)
	requirePrivateRouteProblem(t, invokePrivateRoute(h, app, principal, body, "wrong-source"), http.StatusBadRequest, "invalid_private_attestation_request")

	h = privateRouteHandler(t, nil, "staging", "norn-signed-private", app, "another-owner/another-repo")
	requirePrivateRouteProblem(t, invokePrivateRoute(h, app, principal, privateRouteRequestBody(app, 0), "wrong-repo"), http.StatusForbidden, "release_binding_mismatch")
}

func TestCreatePrivateReleaseAttestationRejectsOversizedBody(t *testing.T) {
	const app = "private-route"
	h := privateRouteHandler(t, nil, "staging", "norn-signed-private", app, "personal-owner/private-repo")
	recorder := invokePrivateRoute(h, app, privateRoutePrincipal(app), bytesOfSize(maxPrivateAttestationJSONBody+1), "oversized")
	requirePrivateRouteProblem(t, recorder, http.StatusBadRequest, "invalid_private_attestation_request")
}

func TestNearLimitPrivateEvidenceFitsQualificationTransport(t *testing.T) {
	const app = "private-route"
	principal := privateRoutePrincipal(app)
	digest := "sha256:" + strings.Repeat("b", 64)
	artifact := "registry.example.test/norn/" + app + "@" + digest
	candidate := releaseCandidateFromCI(*principal.CI, principal.CI.SHA, artifact, "norn-signed-private")
	prefix := `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","documentNamespace":"https://example.invalid/spdx/near-limit","padding":"`
	suffix := `"}`
	sbom := json.RawMessage(prefix + strings.Repeat("x", privateattestation.MaxSPDXDocumentBytes-len(prefix)-len(suffix)) + suffix)
	signer := newPrivateRouteSigner(t)
	bundle, err := privateattestation.Issue(context.Background(), signer, app, principal.CI.SHA, artifact, sbom, candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Attestation.Bundle = bundle
	verifier, err := privateattestation.NewVerifier([]string{base64.RawStdEncoding.EncodeToString(signer.private.Public().(ed25519.PublicKey))})
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(context.Background(), artifact, principal.CI.SHA, app, candidate); err != nil {
		t.Fatalf("near-limit portable evidence did not verify: %v", err)
	}
	now := time.Now().UTC()
	qualification := model.ReleaseQualification{SchemaVersion: releaseQualificationSchema, ID: uuid.NewString(), App: app, Environment: "staging", DeploymentID: uuid.NewString(), SourceSHA: principal.CI.SHA, Artifact: artifact, IssuedAt: now, ExpiresAt: now.Add(time.Hour), Candidate: candidate}
	if err := signReleaseQualification(releaseTestKey('q'), &qualification); err != nil {
		t.Fatal(err)
	}
	promotion, err := json.Marshal(promotionRequest{SourceSHA: principal.CI.SHA, Artifact: artifact, Qualification: qualification})
	if err != nil {
		t.Fatal(err)
	}
	if len(promotion) <= 6<<20 {
		t.Fatalf("test no longer exercises the former 6 MiB limit: %d bytes", len(promotion))
	}
	if len(promotion) > maxReleaseEvidenceJSONBody {
		t.Fatalf("near-limit 2 MiB SPDX amplification is %d bytes, over %d-byte release transport limit", len(promotion), maxReleaseEvidenceJSONBody)
	}
}

func TestHandlerRollbackPassesServerOwnedAppToNornVerifier(t *testing.T) {
	sourceSHA := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("b", 64)
	artifact := "registry.example.test/norn/private-route@" + digest
	signerSHA := strings.Repeat("c", 40)
	signerRef := "personal-owner/norn/.github/workflows/norn-app-release.yml@" + signerSHA
	candidate := model.ReleaseCandidate{Repository: "personal-owner/private-repo", RepositoryVisibility: "private", SignerWorkflowRef: signerRef, SignerWorkflowSHA: signerSHA, Attestation: model.ReleaseAttestationIdentity{Mode: "norn-signed-private", Issuer: githubActionsOIDCIssuer, SubjectDigest: digest, MaterialSHA: sourceSHA}}
	p := &pipeline.Pipeline{
		ReleaseAdmissionMode: "attested", ReleaseAttestationTrustMode: "norn-signed-private", ReleaseAttestationIssuer: githubActionsOIDCIssuer, ReleaseAttestationRepositories: []string{candidate.Repository}, ReleaseAttestationWorkflowRefs: []string{signerRef}, ReleaseRequireSBOM: true,
		VerifyArtifact: func(context.Context, string) error { return nil }, ScanArtifact: func(context.Context, string) error { return nil },
		VerifyNornPrivateAttestations: func(_ context.Context, _ string, _ string, app string, _ model.ReleaseCandidate) error {
			if app != "private-route" {
				return errors.New("signed app identity mismatch")
			}
			return nil
		},
	}
	h := &Handler{pipeline: p}
	target := &model.Deployment{CommitSHA: sourceSHA, ImageTag: artifact}
	if err := h.verifyRollbackReleaseArtifact(context.Background(), &model.InfraSpec{App: "private-route"}, target, candidate); err != nil {
		t.Fatalf("handler rejected valid Norn-private rollback: %v", err)
	}
	if err := h.verifyRollbackReleaseArtifact(context.Background(), &model.InfraSpec{App: "another-app"}, target, candidate); err == nil {
		t.Fatal("handler accepted Norn-private rollback for the wrong app")
	}
}

func bytesOfSize(size int) []byte {
	value := make([]byte, size)
	for index := range value {
		value[index] = 'x'
	}
	return value
}

func TestCreatePrivateReleaseAttestationSuccessReplayAndConflict(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	database, err := store.Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := store.Migrate(database); err != nil {
		t.Fatal(err)
	}
	app := "private-route-" + strings.ToLower(strings.ReplaceAll(uuid.NewString(), "-", ""))
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM operations WHERE app=$1 AND kind='release.attestation'`, app)
	})
	h := privateRouteHandler(t, database, "staging", "norn-signed-private", app, "personal-owner/private-repo")
	principal := privateRoutePrincipal(app)
	principal.TokenID = uuid.NewString()
	body := privateRouteRequestBody(app, 0)

	created := invokePrivateRoute(h, app, principal, body, "stable-build")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var response struct {
		SchemaVersion string                 `json:"schemaVersion"`
		Candidate     model.ReleaseCandidate `json:"candidate"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SchemaVersion != model.NornPrivateAttestationSchema || response.Candidate.Attestation.Mode != "norn-signed-private" || response.Candidate.Attestation.Bundle == nil {
		t.Fatalf("unexpected signed response: %#v", response)
	}
	if created.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("signed evidence response cache policy=%q", created.Header().Get("Cache-Control"))
	}

	replayed := invokePrivateRoute(h, app, principal, body, "stable-build")
	if replayed.Code != http.StatusOK {
		t.Fatalf("replay status=%d", replayed.Code)
	}
	// PostgreSQL JSONB reconstructs candidate objects as maps; JSON member
	// order is not evidence identity. Compare every value, including the exact
	// signed payload and signature strings, rather than serialization order.
	var createdJSON, replayedJSON any
	if err := json.Unmarshal(created.Body.Bytes(), &createdJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(replayed.Body.Bytes(), &replayedJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(createdJSON, replayedJSON) {
		t.Fatal("replayed evidence differs from the original signed response")
	}
	if replayed.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("replayed evidence response cache policy=%q", replayed.Header().Get("Cache-Control"))
	}
	conflict := invokePrivateRoute(h, app, principal, privateRouteRequestBody(app, 1), "stable-build")
	requirePrivateRouteProblem(t, conflict, http.StatusConflict, "idempotency_key_reused")
}
