package handler

import (
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
