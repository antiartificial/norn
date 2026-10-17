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
	tokens map[string]bool
}

func (s *fleetOIDCIdentityStore) ConsumeGitHubActionsAssertion(context.Context, string, string, time.Time) error {
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
	claims := githubActionsClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: githubActionsOIDCIssuer, Subject: "repo:acme/norn-fleet:environment:staging", Audience: jwt.ClaimStrings{"norn-fleet-staging"}, ID: uuid.NewString(), IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(4 * time.Minute))}, Repository: "acme/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202", RunID: "123", RunAttempt: "1", WorkflowRef: "acme/norn-fleet/.github/workflows/apply.yml@" + workflowSHA, WorkflowSHA: workflowSHA, Ref: "refs/heads/main", RefType: "branch", EventName: "workflow_dispatch", Environment: "staging", SHA: workflowSHA, RefProtected: "true"}
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
