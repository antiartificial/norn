package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/store"
)

type fleetJWKTransport struct{ body []byte }

type fleetOIDCIdentityStore struct {
	store.AuthStore
	tokens   map[string]bool
	consumed map[string]bool
}

func (s *fleetOIDCIdentityStore) ConsumeGitHubActionsAssertion(_ context.Context, _, jti string, _ time.Time) error {
	if s.consumed == nil {
		s.consumed = map[string]bool{}
	}
	if s.consumed[jti] {
		return store.ErrGitHubActionsAssertionConsumed
	}
	s.consumed[jti] = true
	return nil
}

func (s *fleetOIDCIdentityStore) RecordAccessToken(_ context.Context, token *store.AccessToken) error {
	if s.tokens == nil {
		s.tokens = make(map[string]bool)
	}
	s.tokens[token.JTI] = true
	return nil
}

func (s *fleetOIDCIdentityStore) AccessTokenActive(_ context.Context, jti string) (bool, error) {
	return s.tokens[jti], nil
}

func (t fleetJWKTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != "https://token.actions.githubusercontent.com/.well-known/jwks" {
		return nil, http.ErrUseLastResponse
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(t.body)), Request: r}, nil
}

func TestFleetOIDCWorkStagingHTTPExchangeBindsControlEnvironment(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(map[string]interface{}{"keys": []map[string]string{{"kid": "fixture-key", "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(private.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(private.PublicKey.E)).Bytes())}}})
	if err != nil {
		t.Fatal(err)
	}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = fleetJWKTransport{body: jwks}
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	workflowSHA := strings.Repeat("a", 40)
	cfg := &config.Config{FleetAuthorityOnly: true, Environment: "staging", APIToken: strings.Repeat("k", 40), GitHubActionsOIDCAudience: "norn-fleet-staging", GitHubActionsOIDCJWKSURL: githubActionsOIDCIssuer + "/.well-known/jwks", GitHubActionsAllowedRefs: []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"workflow_dispatch"}, GitHubActionsFleetAllowedRepository: "acme/norn-fleet@101@202", GitHubActionsFleetAllowedWorkflowRefs: []string{"acme/norn-fleet/.github/workflows/apply.yml@" + workflowSHA}, GitHubActionsFleetAllowedEnvironments: []string{"work-staging"}, GitHubActionsFleetAllowedIntents: []string{"apply"}}
	now := time.Now().UTC()
	claims := githubActionsClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: githubActionsOIDCIssuer, Subject: "repo:acme/norn-fleet:environment:work-staging", Audience: jwt.ClaimStrings{"norn-fleet-staging"}, ID: uuid.NewString(), IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(4 * time.Minute))}, Repository: "acme/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202", RunID: "123", RunAttempt: "1", WorkflowRef: "acme/norn-fleet/.github/workflows/apply.yml@refs/heads/main", WorkflowSHA: workflowSHA, Ref: "refs/heads/main", RefType: "branch", EventName: "workflow_dispatch", Environment: "work-staging", SHA: workflowSHA, RefProtected: "true"}
	assertion := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	assertion.Header["kid"] = "fixture-key"
	raw, err := assertion.SignedString(private)
	if err != nil {
		t.Fatal(err)
	}
	identities := &fleetOIDCIdentityStore{}
	exchange := func(environment string) *httptest.ResponseRecorder {
		body, err := json.Marshal(githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: environment, Intent: "apply"})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/github-actions/exchange", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+raw)
		response := httptest.NewRecorder()
		ExchangeFleetGitHubActionsOIDC(cfg, identities, response, request)
		return response
	}
	if response := exchange("work-staging"); response.Code != http.StatusBadRequest {
		t.Fatalf("GitHub environment accepted as Norn control environment: %d %s", response.Code, response.Body.String())
	}
	response := exchange("staging")
	if response.Code != http.StatusCreated {
		t.Fatalf("work-staging assertion exchange: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	principal, ok := VerifyAccessTokenWithIdentityStore(cfg.APIToken, result.Token, time.Time{}, identities)
	if !ok || principal.Environment != "staging" || principal.CI == nil || principal.CI.Environment != "staging" || principal.CI.GitHubEnvironment != "work-staging" || !principal.Allows(ScopeFleetOperate) {
		t.Fatalf("issued Fleet principal did not preserve control and GitHub environments: %+v valid=%t", principal, ok)
	}
}

func TestFleetOIDCExchangeUsesEtcdReplayAndTokenRegistry(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-conf/fleet-oidc/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	identities := etcdstore.NewAuthStore(client, prefix)
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks, _ := json.Marshal(map[string]interface{}{"keys": []map[string]string{{"kid": "fixture-key", "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(private.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(private.PublicKey.E)).Bytes())}}})
	previousTransport := http.DefaultTransport
	http.DefaultTransport = fleetJWKTransport{body: jwks}
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	workflowSHA := strings.Repeat("a", 40)
	cfg := &config.Config{Environment: "staging", APIToken: strings.Repeat("k", 40), GitHubActionsOIDCAudience: "norn-fleet-staging", GitHubActionsOIDCJWKSURL: githubActionsOIDCIssuer + "/.well-known/jwks", GitHubActionsAllowedRefs: []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"workflow_dispatch"}, GitHubActionsFleetAllowedRepository: "acme/norn-fleet@101@202", GitHubActionsFleetAllowedWorkflowRefs: []string{"acme/norn-fleet/.github/workflows/apply.yml@" + workflowSHA}, GitHubActionsFleetAllowedEnvironments: []string{"staging"}, GitHubActionsFleetAllowedIntents: []string{"apply"}}
	now := time.Now().UTC()
	claims := githubActionsClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: githubActionsOIDCIssuer, Subject: "repo:acme/norn-fleet:environment:staging", Audience: jwt.ClaimStrings{"norn-fleet-staging"}, ID: uuid.NewString(), IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(4 * time.Minute))}, Repository: "acme/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202", RunID: "123", RunAttempt: "1", WorkflowRef: "acme/norn-fleet/.github/workflows/apply.yml@refs/heads/main", WorkflowSHA: workflowSHA, Ref: "refs/heads/main", RefType: "branch", EventName: "workflow_dispatch", Environment: "staging", SHA: workflowSHA, RefProtected: "true"}
	assertion := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	assertion.Header["kid"] = "fixture-key"
	raw, err := assertion.SignedString(private)
	if err != nil {
		t.Fatal(err)
	}
	exchange := func(scope string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(githubActionsExchangeRequest{Scope: scope, Environment: "staging", Intent: "apply"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/github-actions/exchange", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		ExchangeFleetGitHubActionsOIDC(cfg, identities, rec, req)
		return rec
	}
	if got := exchange(ScopeReleaseStage); got.Code != http.StatusBadRequest {
		t.Fatalf("release scope on Fleet controller: %d %s", got.Code, got.Body.String())
	}
	first := exchange(ScopeFleetOperate)
	if first.Code != http.StatusCreated {
		t.Fatalf("Fleet exchange: %d %s", first.Code, first.Body.String())
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	principal, ok := VerifyAccessTokenWithIdentityStore(cfg.APIToken, result.Token, time.Time{}, identities)
	if !ok || !principal.Allows(ScopeFleetOperate) || principal.Allows(ScopeReleaseStage) {
		t.Fatalf("issued token does not have only the Fleet scope: %+v", principal)
	}
	if got := exchange(ScopeFleetOperate); got.Code != http.StatusConflict {
		t.Fatalf("replayed assertion: %d %s", got.Code, got.Body.String())
	}
}

func TestFleetOIDCStagingReleaseExchangeIsAppBound(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks, _ := json.Marshal(map[string]interface{}{"keys": []map[string]string{{"kid": "release-key", "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(private.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(private.PublicKey.E)).Bytes())}}})
	previous := http.DefaultTransport
	http.DefaultTransport = fleetJWKTransport{body: jwks}
	t.Cleanup(func() { http.DefaultTransport = previous })
	sha := strings.Repeat("a", 40)
	cfg := &config.Config{FleetAuthorityOnly: true, Environment: "staging", APIToken: strings.Repeat("k", 40), GitHubActionsOIDCAudience: "norn-fleet-staging", GitHubActionsOIDCJWKSURL: githubActionsOIDCIssuer + "/.well-known/jwks", GitHubActionsDefaultBranch: "main", GitHubActionsAllowedRefs: []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsAllowedEnvironments: []string{"staging"}, GitHubActionsAllowedWorkflowRefs: []string{"acme/release/.github/workflows/release.yml@" + sha}, GitHubActionsReleaseBindings: []string{"hello-norn-mysql=acme/hello-norn-mysql@101@202"}}
	now := time.Now().UTC()
	claims := githubActionsClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: githubActionsOIDCIssuer, Subject: "repo:acme/hello-norn-mysql:environment:staging", Audience: jwt.ClaimStrings{"norn-fleet-staging"}, ID: uuid.NewString(), IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(4 * time.Minute))}, Repository: "acme/hello-norn-mysql", RepositoryID: "101", RepositoryOwnerID: "202", RepositoryVisibility: "public", RunID: "123", RunAttempt: "1", WorkflowRef: "acme/hello-norn-mysql/.github/workflows/release.yml@refs/heads/main", WorkflowSHA: sha, JobWorkflowRef: "acme/release/.github/workflows/release.yml@" + sha, JobWorkflowSHA: sha, Ref: "refs/heads/main", RefType: "branch", EventName: "push", Environment: "staging", SHA: sha, RefProtected: "true"}
	identities := &fleetOIDCIdentityStore{}
	exchange := func(scope, app, intent string) *httptest.ResponseRecorder {
		assertion := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		assertion.Header["kid"] = "release-key"
		raw, e := assertion.SignedString(private)
		if e != nil {
			t.Fatal(e)
		}
		body, _ := json.Marshal(githubActionsExchangeRequest{Scope: scope, App: app, Environment: "staging", Intent: intent})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/github-actions/exchange", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		ExchangeFleetGitHubActionsOIDC(cfg, identities, rec, req)
		return rec
	}
	if got := exchange(ScopeReleaseStage, "other-app", "stage"); got.Code != 403 {
		t.Fatalf("wrong app: %d", got.Code)
	}
	claims.RepositoryID = "999"
	if got := exchange(ScopeReleaseStage, "hello-norn-mysql", "stage"); got.Code != 403 {
		t.Fatalf("wrong repo: %d", got.Code)
	}
	claims.RepositoryID = "101"
	claims.Ref = "refs/heads/feature"
	claims.ID = uuid.NewString()
	if got := exchange(ScopeReleaseStage, "hello-norn-mysql", "stage"); got.Code != 403 {
		t.Fatalf("wrong branch: %d", got.Code)
	}
	claims.Ref = "refs/heads/main"
	claims.ID = uuid.NewString()
	if got := exchange(ScopeReleasePromote, "hello-norn-mysql", "promote"); got.Code != 400 {
		t.Fatalf("promote scope: %d", got.Code)
	}
	first := exchange(ScopeReleaseStage, "hello-norn-mysql", "stage")
	if first.Code != 201 {
		t.Fatalf("stage: %d %s", first.Code, first.Body)
	}
	var out struct {
		Token string `json:"token"`
	}
	if e := json.Unmarshal(first.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	principal, ok := VerifyAccessTokenWithIdentityStore(cfg.APIToken, out.Token, time.Time{}, identities)
	if !ok || !principal.Allows(ScopeReleaseStage) || principal.Allows(ScopeFleetOperate) || principal.App != "hello-norn-mysql" || principal.CI == nil || principal.CI.RepositoryID != "101" {
		t.Fatalf("principal escaped release identity")
	}
	if got := exchange(ScopeReleaseStage, "hello-norn-mysql", "stage"); got.Code != 409 {
		t.Fatalf("replay: %d", got.Code)
	}
	claims.ID = uuid.NewString()
	if got := exchange(ScopeReleaseAttest, "hello-norn-mysql", "attest"); got.Code != 201 {
		t.Fatalf("attest: %d %s", got.Code, got.Body)
	}
	claims.ID = uuid.NewString()
	if got := exchange(ScopeReleaseQualify, "hello-norn-mysql", "qualify"); got.Code != 201 {
		t.Fatalf("qualify: %d %s", got.Code, got.Body)
	}
}
