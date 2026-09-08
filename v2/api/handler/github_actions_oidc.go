package handler

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"norn/v2/api/store"
)

const githubActionsOIDCIssuer = "https://token.actions.githubusercontent.com"

const githubJWKCacheDefaultTTL = 5 * time.Minute
const githubJWKCacheMaxTTL = 15 * time.Minute
const githubJWKRefreshCooldown = 30 * time.Second

type githubJWKCache struct {
	mu                sync.Mutex
	keys              map[string]interface{}
	expires           time.Time
	lastForcedRefresh time.Time
	loading           bool
	done              chan struct{}
	now               func() time.Time
	fetch             func(*http.Request, string) (map[string]interface{}, time.Duration, error)
}

func newGitHubJWKCache() *githubJWKCache {
	return &githubJWKCache{now: time.Now, fetch: fetchGitHubJWKs}
}

func (h *Handler) githubJWKCache() *githubJWKCache {
	if h.githubJWKS != nil {
		return h.githubJWKS
	}
	// Focused embedded callers may construct Handler directly. A private cache
	// still prevents a request storm without sharing trust state across handlers.
	h.githubJWKS = newGitHubJWKCache()
	return h.githubJWKS
}

func (c *githubJWKCache) get(r *http.Request, rawURL string, force bool) (map[string]interface{}, error) {
	for {
		c.mu.Lock()
		now := c.now()
		if !force && len(c.keys) > 0 && c.expires.After(now) {
			keys := c.keys
			c.mu.Unlock()
			return keys, nil
		}
		if force && len(c.keys) > 0 && c.expires.After(now) && c.lastForcedRefresh.Add(githubJWKRefreshCooldown).After(now) {
			keys := c.keys
			c.mu.Unlock()
			return keys, nil
		}
		if c.loading {
			done := c.done
			c.mu.Unlock()
			select {
			case <-done:
				force = false
				continue
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		if force {
			c.lastForcedRefresh = now
		}
		c.loading, c.done = true, make(chan struct{})
		done := c.done
		c.mu.Unlock()

		keys, ttl, err := c.fetch(r, rawURL)
		c.mu.Lock()
		if err == nil {
			if ttl <= 0 {
				ttl = githubJWKCacheDefaultTTL
			} else if ttl > githubJWKCacheMaxTTL {
				ttl = githubJWKCacheMaxTTL
			}
			c.keys = keys
			c.expires = c.now().Add(ttl)
		}
		c.loading = false
		close(done)
		c.mu.Unlock()
		return keys, err
	}
}

type githubActionsExchangeRequest struct {
	Scope       string `json:"scope"`
	App         string `json:"app,omitempty"`
	Environment string `json:"environment"`
	Intent      string `json:"intent,omitempty"`
}

type githubActionsClaims struct {
	jwt.RegisteredClaims
	Repository           string `json:"repository"`
	RepositoryID         string `json:"repository_id"`
	RepositoryOwnerID    string `json:"repository_owner_id"`
	RepositoryVisibility string `json:"repository_visibility"`
	RunID                string `json:"run_id"`
	RunAttempt           string `json:"run_attempt"`
	WorkflowRef          string `json:"workflow_ref"`
	WorkflowSHA          string `json:"workflow_sha"`
	JobWorkflowRef       string `json:"job_workflow_ref"`
	JobWorkflowSHA       string `json:"job_workflow_sha"`
	Ref                  string `json:"ref"`
	RefType              string `json:"ref_type"`
	EventName            string `json:"event_name"`
	Environment          string `json:"environment"`
	SHA                  string `json:"sha"`
	// GitHub emits this OIDC claim as the literal string "true" or "false".
	RefProtected string `json:"ref_protected"`
}

type githubJWKSet struct {
	Keys []githubJWK `json:"keys"`
}
type githubJWK struct {
	KID string `json:"kid"`
	KTY string `json:"kty"`
	ALG string `json:"alg"`
	USE string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	CRV string `json:"crv"`
	X   string `json:"x"`
}

// ExchangeGitHubActionsOIDC turns a verified, one-job GitHub assertion into a
// Norn token. It intentionally accepts the assertion only in Authorization so
// proxy/access logs do not capture it as a JSON field.
func (h *Handler) ExchangeGitHubActionsOIDC(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.cfg == nil || h.cfg.APIToken == "" || h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "github_actions_oidc_unavailable", "GitHub Actions OIDC exchange is not configured")
		return
	}
	if h.cfg.EnvironmentID() == "development" && !h.cfg.AllowDevelopmentGitHubActionsOIDC {
		WriteControlProblem(w, r, http.StatusNotFound, "github_actions_oidc_disabled", "GitHub Actions OIDC exchange is disabled in development")
		return
	}
	var request githubActionsExchangeRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_github_actions_exchange", err.Error())
		return
	}
	request.Scope, request.App, request.Environment, request.Intent = strings.TrimSpace(request.Scope), strings.TrimSpace(request.App), strings.TrimSpace(request.Environment), strings.TrimSpace(request.Intent)
	if !githubActionsExchangeScopeAllowed(request.Scope) || request.Environment != h.cfg.EnvironmentID() || (request.Scope != ScopeFleetOperate && request.App == "") {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_github_actions_exchange", "scope, app, and environment are not valid for this exchange")
		return
	}
	assertion := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if assertion == "" || assertion == r.Header.Get("Authorization") {
		WriteControlProblem(w, r, http.StatusUnauthorized, "github_actions_assertion_required", "a GitHub Actions OIDC bearer assertion is required")
		return
	}
	claims, err := h.validateGitHubActionsAssertion(r, assertion)
	if err != nil {
		WriteControlProblem(w, r, http.StatusUnauthorized, "github_actions_assertion_invalid", err.Error())
		return
	}
	ci, err := h.authorizeGitHubActionsClaims(claims, request)
	if err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "github_actions_identity_denied", err.Error())
		return
	}
	// Reserve the verified GitHub issuer+jti before stateless token signing.
	// If signing/recording later fails the assertion is intentionally burned;
	// callers can obtain a fresh short-lived GitHub assertion.
	if err := h.db.ConsumeGitHubActionsAssertion(r.Context(), claims.Issuer, claims.ID, claims.ExpiresAt.Time); err != nil {
		if errors.Is(err, store.ErrGitHubActionsAssertionConsumed) {
			WriteControlProblem(w, r, http.StatusConflict, "github_actions_assertion_replayed", "GitHub Actions OIDC assertion was already consumed")
			return
		}
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "github_actions_exchange_unavailable", "durable GitHub Actions replay protection is unavailable")
		return
	}
	// Workflows refresh on a fixed cadence. Keep the issued lifetime fixed too
	// so an otherwise valid configuration cannot silently expire mid-step.
	ttl := 15 * time.Minute
	now := time.Now().UTC()
	claimsOut := tokenClaims{Sub: "github-actions:" + ci.Repository + ":" + ci.RunID, Iss: "norn", Aud: "norn-control", Use: "access", Managed: true, Iat: now.Unix(), Exp: now.Add(ttl).Unix(), Jti: "norn_" + uuid.NewString(), Scopes: []string{request.Scope}, App: request.App, Environment: request.Environment, CI: &ci}
	token, err := signToken(h.cfg.APIToken, claimsOut)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "github_actions_exchange_failed", "failed to sign Norn token")
		return
	}
	if err := h.db.RecordAccessToken(r.Context(), &store.AccessToken{JTI: claimsOut.Jti, Subject: claimsOut.Sub, Scopes: claimsOut.Scopes, IssuedAt: now, ExpiresAt: now.Add(ttl)}); err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "github_actions_exchange_failed", "failed to record Norn token")
		return
	}
	preventSensitiveResponseCaching(w)
	response := map[string]interface{}{"token": token, "tokenId": claimsOut.Jti, "scopes": claimsOut.Scopes, "app": request.App, "environment": request.Environment, "expiresAt": now.Add(ttl).Format(time.RFC3339), "subject": claimsOut.Sub}
	if request.Scope != ScopeFleetOperate {
		response["attestationMode"] = releaseAttestationMode(ci.RepositoryVisibility, h.cfg.ReleaseAttestationTrustMode)
	}
	writeJSONStatus(w, http.StatusCreated, response)
}

func githubActionsExchangeScopeAllowed(scope string) bool {
	switch scope {
	case ScopeReleaseAttest, ScopeReleaseStage, ScopeReleaseQualify, ScopeReleasePromote, ScopeReleaseRollback, ScopeFleetOperate, ScopeFleetExternalAdmission:
		return true
	}
	return false
}

func (h *Handler) validateGitHubActionsAssertion(r *http.Request, raw string) (*githubActionsClaims, error) {
	if strings.TrimSpace(h.cfg.GitHubActionsOIDCAudience) == "" || strings.TrimSpace(h.cfg.GitHubActionsOIDCJWKSURL) == "" {
		return nil, fmt.Errorf("GitHub Actions issuer configuration is incomplete")
	}
	parser := jwt.NewParser()
	unsigned, _, headerErr := parser.ParseUnverified(raw, jwt.MapClaims{})
	if headerErr != nil || unsigned == nil {
		return nil, fmt.Errorf("GitHub OIDC token header is invalid")
	}
	kid, _ := unsigned.Header["kid"].(string)
	if kid == "" {
		return nil, fmt.Errorf("GitHub OIDC token has no key identifier")
	}
	cache := h.githubJWKCache()
	keys, err := cache.get(r, h.cfg.GitHubActionsOIDCJWKSURL, false)
	if err != nil {
		return nil, fmt.Errorf("load GitHub Actions JWKS: %w", err)
	}
	if keys[kid] == nil {
		keys, err = cache.get(r, h.cfg.GitHubActionsOIDCJWKSURL, true)
		if err != nil {
			return nil, fmt.Errorf("refresh GitHub Actions JWKS for unknown key: %w", err)
		}
	}
	claims := &githubActionsClaims{}
	parsed, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (interface{}, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("unsupported GitHub OIDC algorithm")
		}
		kid, _ := token.Header["kid"].(string)
		key := keys[kid]
		if key == nil {
			return nil, fmt.Errorf("GitHub OIDC key is unknown")
		}
		return key, nil
	}, jwt.WithIssuer(githubActionsOIDCIssuer), jwt.WithAudience(h.cfg.GitHubActionsOIDCAudience), jwt.WithLeeway(30*time.Second), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}))
	if err != nil || parsed == nil || !parsed.Valid {
		if err == nil {
			err = fmt.Errorf("GitHub OIDC token is invalid")
		}
		return nil, err
	}
	if claims.Issuer != githubActionsOIDCIssuer || claims.Subject == "" || claims.ID == "" || claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil || claims.IssuedAt.Time.After(time.Now().Add(30*time.Second)) || claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time) > 10*time.Minute || time.Since(claims.IssuedAt.Time) > 10*time.Minute || claims.Repository == "" || !validGitHubNumericID(claims.RepositoryID) || !validGitHubNumericID(claims.RepositoryOwnerID) || !validGitHubNumericID(claims.RunID) || !validGitHubNumericID(claims.RunAttempt) || claims.WorkflowRef == "" || !fullSourceSHAPattern.MatchString(claims.WorkflowSHA) || !fullSourceSHAPattern.MatchString(claims.SHA) || claims.RefProtected != "true" || claims.Ref == "" || claims.RefType == "" || claims.EventName == "" || claims.Environment == "" {
		return nil, fmt.Errorf("GitHub OIDC token omits required identity claims")
	}
	return claims, nil
}

func validGitHubNumericID(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return value != "0"
}

func fetchGitHubJWKs(r *http.Request, rawURL string) (map[string]interface{}, time.Duration, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "token.actions.githubusercontent.com" || parsed.Path != "/.well-known/jwks" {
		return nil, 0, fmt.Errorf("GitHub Actions JWKS URL must be the fixed HTTPS GitHub issuer JWKS")
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(next *http.Request, _ []*http.Request) error {
		parsed, err := url.Parse(next.URL.String())
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "token.actions.githubusercontent.com" || parsed.Path != "/.well-known/jwks" {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("JWKS HTTP %d", response.StatusCode)
	}
	var set githubJWKSet
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&set); err != nil {
		return nil, 0, err
	}
	keys := map[string]interface{}{}
	for _, item := range set.Keys {
		if (item.ALG != "" && item.ALG != "RS256") || (item.USE != "" && item.USE != "sig") {
			continue
		}
		if item.KID == "" {
			continue
		}
		key, err := githubJWKPublicKey(item)
		if err == nil {
			keys[item.KID] = key
		}
	}
	if len(keys) == 0 {
		return nil, 0, fmt.Errorf("JWKS has no supported public keys")
	}
	return keys, githubJWKCacheTTL(response.Header.Get("Cache-Control")), nil
}

func githubJWKCacheTTL(cacheControl string) time.Duration {
	for _, directive := range strings.Split(cacheControl, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(directive), "=")
		if !ok || strings.ToLower(key) != "max-age" {
			continue
		}
		seconds, err := strconv.Atoi(strings.Trim(strings.TrimSpace(value), `"`))
		if err == nil && seconds > 0 {
			ttl := time.Duration(seconds) * time.Second
			if ttl > githubJWKCacheMaxTTL {
				return githubJWKCacheMaxTTL
			}
			return ttl
		}
	}
	return githubJWKCacheDefaultTTL
}

func githubJWKPublicKey(item githubJWK) (interface{}, error) {
	decode := func(value string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(value) }
	switch item.KTY {
	case "RSA":
		n, err := decode(item.N)
		if err != nil {
			return nil, err
		}
		e, err := decode(item.E)
		if err != nil {
			return nil, err
		}
		exponent := 0
		for _, b := range e {
			exponent = exponent<<8 + int(b)
		}
		if exponent < 3 {
			return nil, fmt.Errorf("invalid RSA exponent")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}, nil
	default:
		return nil, fmt.Errorf("unsupported JWK key type")
	}
}

func (h *Handler) authorizeGitHubActionsClaims(c *githubActionsClaims, request githubActionsExchangeRequest) (CIIdentity, error) {
	if c.Environment != request.Environment || !matchesAny(c.Ref, h.cfg.GitHubActionsAllowedRefs) || !matchesAny(c.EventName, h.cfg.GitHubActionsAllowedEvents) {
		return CIIdentity{}, fmt.Errorf("GitHub Actions ref, event, or environment is not allowlisted")
	}
	if request.Scope == ScopeFleetOperate || request.Scope == ScopeFleetExternalAdmission {
		if !matchesRepository(c, []string{h.cfg.GitHubActionsFleetAllowedRepository}) || !matchesAny(c.Environment, h.cfg.GitHubActionsFleetAllowedEnvironments) || !matchesAny(request.Intent, h.cfg.GitHubActionsFleetAllowedIntents) || !matchesPinnedDirectWorkflow(c.WorkflowRef, c.WorkflowSHA, h.cfg.GitHubActionsFleetAllowedWorkflowRefs) {
			return CIIdentity{}, fmt.Errorf("GitHub Actions fleet identity is not allowlisted")
		}
	} else {
		if !releaseExchangeLaneAllowed(request.Scope, request.Intent, c, h.cfg.GitHubActionsDefaultBranch) {
			return CIIdentity{}, fmt.Errorf("GitHub Actions release scope is not allowed for this lane")
		}
		private := c.RepositoryVisibility == "private" || c.RepositoryVisibility == "internal"
		trustMode := h.cfg.ReleaseAttestationTrustMode
		if trustMode == "" {
			trustMode = "github-public"
		}
		privateMode := trustMode == "github-private" || trustMode == "norn-signed-private"
		if (c.RepositoryVisibility != "public" && !private) || (private && !privateMode) || (c.RepositoryVisibility == "public" && trustMode != "github-public") || !matchesReleaseBinding(request.App, c, h.cfg.GitHubActionsReleaseBindings) || !matchesAny(c.Environment, h.cfg.GitHubActionsAllowedEnvironments) || !matchesAny(c.JobWorkflowRef, h.cfg.GitHubActionsAllowedWorkflowRefs) || !fullSourceSHAPattern.MatchString(c.JobWorkflowSHA) {
			return CIIdentity{}, fmt.Errorf("GitHub Actions release identity is not allowlisted")
		}
		if !workflowRefBindsSHA(c.JobWorkflowRef, c.JobWorkflowSHA) {
			return CIIdentity{}, fmt.Errorf("GitHub Actions reusable workflow is not SHA pinned")
		}
	}
	return CIIdentity{Provider: "github-actions", Repository: c.Repository, RepositoryID: c.RepositoryID, RepositoryOwnerID: c.RepositoryOwnerID, RepositoryVisibility: c.RepositoryVisibility, RunID: c.RunID, RunAttempt: c.RunAttempt, WorkflowRef: c.WorkflowRef, WorkflowSHA: c.WorkflowSHA, JobWorkflowRef: c.JobWorkflowRef, JobWorkflowSHA: c.JobWorkflowSHA, Ref: c.Ref, RefType: c.RefType, EventName: c.EventName, Environment: c.Environment, SHA: c.SHA, RefProtected: c.RefProtected == "true", Intent: request.Intent}, nil
}

// releaseExchangeLaneAllowed is deliberately narrower than configurable
// allowlists. Configuration can reduce trusted identities further but may not
// make a production promotion token valid on a staging merge lane.
func releaseExchangeLaneAllowed(scope, intent string, c *githubActionsClaims, defaultBranch string) bool {
	if c == nil || c.RefProtected != "true" {
		return false
	}
	protectedBranchRef := "refs/heads/" + strings.TrimSpace(defaultBranch)
	switch scope {
	case ScopeReleaseAttest:
		return intent == "attest" && c.Environment == "staging" && c.Ref == protectedBranchRef && c.EventName == "push"
	case ScopeReleaseStage:
		return intent == "stage" && c.Environment == "staging" && c.Ref == protectedBranchRef && c.EventName == "push"
	case ScopeReleaseQualify:
		return c.Environment == "staging" && c.Ref == protectedBranchRef && ((intent == "qualify" && c.EventName == "push") || (intent == "requalify" && c.EventName == "workflow_dispatch"))
	case ScopeReleasePromote:
		return intent == "promote" && c.Environment == "production" && c.EventName == "push" && c.RefType == "tag" && matchesAny(c.Ref, []string{"refs/tags/v*"})
	case ScopeReleaseRollback:
		return intent == "rollback" && c.Environment == "production" && c.Ref == protectedBranchRef && c.EventName == "workflow_dispatch"
	default:
		return false
	}
}

func matchesRepository(c *githubActionsClaims, allowed []string) bool {
	for _, entry := range allowed {
		parts := strings.Split(entry, "@")
		if len(parts) == 3 && parts[0] == c.Repository && parts[1] == c.RepositoryID && parts[2] == c.RepositoryOwnerID {
			return true
		}
	}
	return false
}

func matchesReleaseBinding(app string, c *githubActionsClaims, bindings []string) bool {
	if c == nil || app == "" {
		return false
	}
	for _, binding := range bindings {
		boundApp, repositoryTuple, ok := strings.Cut(binding, "=")
		if !ok || boundApp != app {
			continue
		}
		parts := strings.Split(repositoryTuple, "@")
		if len(parts) == 3 && parts[0] == c.Repository && parts[1] == c.RepositoryID && parts[2] == c.RepositoryOwnerID {
			return true
		}
	}
	return false
}
func matchesAny(value string, allowed []string) bool {
	for _, pattern := range allowed {
		matched, _ := path.Match(pattern, value)
		if matched {
			return true
		}
	}
	return false
}
func workflowRefBindsSHA(ref, sha string) bool { return strings.HasSuffix(ref, "@"+sha) }

// matchesPinnedDirectWorkflow handles GitHub's distinct direct-workflow
// claims: workflow_ref normally ends in the branch/tag that initiated the run,
// while workflow_sha carries the immutable commit containing that workflow.
// Operators still configure path@<full SHA>; authorization matches the path
// and SHA independently instead of expecting a synthetic claim GitHub never
// emits.
func matchesPinnedDirectWorkflow(claimRef, claimSHA string, allowed []string) bool {
	if !fullSourceSHAPattern.MatchString(claimSHA) {
		return false
	}
	claimSeparator := strings.LastIndexByte(claimRef, '@')
	if claimSeparator <= 0 || claimSeparator == len(claimRef)-1 {
		return false
	}
	claimPath := claimRef[:claimSeparator]
	for _, configured := range allowed {
		configured = strings.TrimSpace(configured)
		separator := strings.LastIndexByte(configured, '@')
		if separator > 0 && configured[:separator] == claimPath && configured[separator+1:] == claimSHA {
			return true
		}
	}
	return false
}
