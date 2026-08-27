package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"norn/v2/api/config"
)

func TestAccessTokenScopes(t *testing.T) {
	h := &Handler{cfg: &config.Config{APIToken: "test-secret"}}
	now := time.Now().UTC()
	token, err := signToken(h.cfg.APIToken, tokenClaims{
		Sub:    "mac-client",
		Iat:    now.Unix(),
		Exp:    now.Add(time.Hour).Unix(),
		Jti:    "test-token",
		Scopes: []string{ScopeAPIRead, ScopeEventsRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, ok := h.VerifyAccessToken(token)
	if !ok {
		t.Fatal("expected token to verify")
	}
	if !principal.Allows(ScopeEventsRead) {
		t.Fatal("expected events scope")
	}
	if principal.Allows(ScopeAppsExec) {
		t.Fatal("token unexpectedly allows exec")
	}
}

func TestFleetOperateIsARegisteredLeastPrivilegeScope(t *testing.T) {
	scopes, err := normalizeAccessTokenScopes([]string{ScopeAPIRead, ScopeFleetOperate})
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 || scopes[1] != ScopeFleetOperate {
		t.Fatalf("scopes = %v", scopes)
	}
	principal := AccessPrincipal{Scopes: []string{ScopeFleetOperate}}
	if !principal.Allows(ScopeFleetOperate) || principal.Allows(ScopeAPIWrite) {
		t.Fatal("fleet scope must not imply general API writes")
	}
}

func TestLegacyAccessTokenRetainsCompatibility(t *testing.T) {
	h := &Handler{cfg: &config.Config{APIToken: "test-secret"}}
	now := time.Now().UTC()
	token, err := signToken(h.cfg.APIToken, tokenClaims{Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(), Jti: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	principal, ok := h.VerifyAccessToken(token)
	if !ok || !principal.Legacy || !principal.Allows(ScopeAppsExec) {
		t.Fatal("legacy access token should retain its former full access until expiry")
	}
}

func TestStepUpTokenCannotAuthenticateAsAccessToken(t *testing.T) {
	h := &Handler{cfg: &config.Config{APIToken: "test-secret"}}
	now := time.Now().UTC()
	token, err := signToken(h.cfg.APIToken, tokenClaims{
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(), Jti: "stepup-test", Did: "device-1",
		Aud: "norn-exec-step-up", Purpose: "exec", Resource: "demo", ChallengeID: "challenge-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.VerifyAccessToken(token); ok {
		t.Fatal("step-up capability must not authenticate as a general access token")
	}
}

func TestManagedTokenFailsClosedWithoutRegistry(t *testing.T) {
	h := &Handler{cfg: &config.Config{APIToken: "test-secret"}}
	now := time.Now().UTC()
	token, err := signToken(h.cfg.APIToken, tokenClaims{
		Sub: "device", Iss: "norn", Aud: "norn-control", Use: "access", Managed: true,
		Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(), Jti: "managed-token", Did: "device-1",
		Scopes: []string{ScopeAPIRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.VerifyAccessToken(token); ok {
		t.Fatal("managed token must fail closed when its registry is unavailable")
	}
}

func TestLegacyRawSignedTokenCompatibility(t *testing.T) {
	h := &Handler{cfg: &config.Config{APIToken: "test-secret", LegacyTokenSigningUntil: time.Now().Add(time.Hour)}}
	now := time.Now().UTC()
	token := rawSignedToken(t, h.cfg.APIToken, `{"alg":"HS256","typ":"JWT"}`, map[string]interface{}{
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "jti": "legacy-raw",
	})
	if principal, ok := h.VerifyAccessToken(token); !ok || !principal.Legacy {
		t.Fatal("previous raw-key tokens should remain valid only as legacy tokens until expiry")
	}
}

func TestTokenHeaderAndSizeAreValidated(t *testing.T) {
	now := time.Now().UTC()
	token := rawSignedTokenWithKey(t, tokenSigningKey("test-secret"), `{"alg":"none","typ":"JWT"}`, map[string]interface{}{
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "jti": "bad-header",
	})
	if _, err := verifyToken("test-secret", token, time.Time{}); err == nil {
		t.Fatal("non-HS256 token header must be rejected even with a valid MAC")
	}
	if _, err := verifyToken("test-secret", strings.Repeat("x", 8193), time.Time{}); err == nil {
		t.Fatal("oversized token must be rejected")
	}
}

func TestLegacyRawSigningExpiresAtConfiguredDeadline(t *testing.T) {
	now := time.Now().UTC()
	token := rawSignedToken(t, "test-secret", `{"alg":"HS256","typ":"JWT"}`, map[string]interface{}{
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(), "jti": "legacy-cutoff",
	})
	if _, err := verifyToken("test-secret", token, now.Add(time.Minute)); err != nil {
		t.Fatalf("legacy signature before cutoff was rejected: %v", err)
	}
	if _, err := verifyToken("test-secret", token, now.Add(-time.Minute)); err == nil {
		t.Fatal("legacy signature remained valid after cutoff")
	}
}

func TestSigningKeyIsDomainSeparated(t *testing.T) {
	now := time.Now().UTC()
	token, err := signToken("test-secret", tokenClaims{Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(), Jti: "domain-test"})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	mac := hmac.New(sha256.New, []byte("test-secret"))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if hmac.Equal([]byte(parts[2]), []byte(base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))) {
		t.Fatal("new JWTs must not be signed directly with the bearer root secret")
	}
}

func rawSignedToken(t *testing.T, secret, header string, claims map[string]interface{}) string {
	t.Helper()
	return rawSignedTokenWithKey(t, []byte(secret), header, claims)
}

func rawSignedTokenWithKey(t *testing.T, key []byte, header string, claims map[string]interface{}) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestNormalizeAccessTokenScopes(t *testing.T) {
	scopes, err := normalizeAccessTokenScopes(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 || scopes[0] != ScopeAPIRead || scopes[1] != ScopeEventsRead {
		t.Fatalf("default scopes = %#v", scopes)
	}
	if _, err := normalizeAccessTokenScopes([]string{"shell:anything"}); err == nil {
		t.Fatal("expected unsupported scope to fail")
	}
}
