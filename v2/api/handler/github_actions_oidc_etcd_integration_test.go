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
)

type fleetJWKTransport struct{ body []byte }

func (t fleetJWKTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != "https://token.actions.githubusercontent.com/.well-known/jwks" {
		return nil, http.ErrUseLastResponse
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(t.body)), Request: r}, nil
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
