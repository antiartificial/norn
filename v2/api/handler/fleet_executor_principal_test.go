package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/store"
)

func executorPrincipal() AccessPrincipal {
	return AccessPrincipal{
		Subject: "norn-exec", TokenID: "jti", DeviceID: "dev-1",
		Scopes: []string{ScopeFleetExecute}, ExpiresAt: time.Now().Add(time.Hour),
		Source: AccessPrincipalSourceManagedToken,
	}
}

func TestExecutorPrincipalRequiresManagedTokenAndExactScope(t *testing.T) {
	if !IsFleetExecutorPrincipal(executorPrincipal()) {
		t.Fatal("valid executor principal rejected")
	}
	req := WithAccessPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), func() *AccessPrincipal { p := executorPrincipal(); return &p }())
	if _, ok := FleetExecutorPrincipalFromRequest(req); !ok {
		t.Fatal("request principal rejected")
	}
	if _, ok := FleetExecutorPrincipalFromRequest(httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Fatal("missing principal accepted")
	}
	mutations := map[string]func(*AccessPrincipal){
		"scope combined": func(p *AccessPrincipal) { p.Scopes = []string{ScopeFleetExecute, ScopeAPIRead} },
		"admin only":     func(p *AccessPrincipal) { p.Scopes = []string{ScopeAdmin} },
		"admin plus":     func(p *AccessPrincipal) { p.Scopes = []string{ScopeAdmin, ScopeFleetExecute} },
		"other scope":    func(p *AccessPrincipal) { p.Scopes = []string{ScopeFleetOperate} },
		"no scope":       func(p *AccessPrincipal) { p.Scopes = nil },
		"legacy":         func(p *AccessPrincipal) { p.Legacy = true; p.Source = AccessPrincipalSourceUnmanagedLegacy },
		"legacy source":  func(p *AccessPrincipal) { p.Source = AccessPrincipalSourceUnmanagedLegacy },
		"shared api":     func(p *AccessPrincipal) { p.Source = AccessPrincipalSourceSharedAPI },
		"cloudflare":     func(p *AccessPrincipal) { p.Source = AccessPrincipalSourceCloudflareAccess },
		"no source":      func(p *AccessPrincipal) { p.Source = "" },
		"no token id":    func(p *AccessPrincipal) { p.TokenID = "" },
		"no device id":   func(p *AccessPrincipal) { p.DeviceID = "" },
		"expired":        func(p *AccessPrincipal) { p.ExpiresAt = time.Now().Add(-time.Second) },
		"no expiry":      func(p *AccessPrincipal) { p.ExpiresAt = time.Time{} },
		"ci identity":    func(p *AccessPrincipal) { p.CI = &CIIdentity{Provider: "github-actions"} },
	}
	for name, mutate := range mutations {
		p := executorPrincipal()
		mutate(&p)
		if IsFleetExecutorPrincipal(p) {
			t.Errorf("%s accepted as executor", name)
		}
	}
}

func TestFleetExecuteOnlyForExecutorPlatform(t *testing.T) {
	for _, cfg := range []*config.Config{{}, {FleetAuthorityOnly: true}} {
		h := &Handler{cfg: cfg}
		got, err := h.deviceEnrollmentScopesForPlatform(FleetExecutorPlatform, []string{ScopeFleetExecute})
		if err != nil || !reflect.DeepEqual(got, []string{ScopeFleetExecute}) {
			t.Fatalf("executor alone: %v %v", got, err)
		}
		for _, platform := range []string{"", "ios", "web", "fleet-executor-x"} {
			if _, err := h.deviceEnrollmentScopesForPlatform(platform, []string{ScopeFleetExecute}); err == nil {
				t.Fatalf("platform %q granted fleet:execute", platform)
			}
		}
		for _, combo := range [][]string{
			{ScopeFleetExecute, ScopeAPIRead}, {ScopeAPIRead, ScopeFleetExecute}, {ScopeFleetExecute, ScopeAdmin}, {ScopeFleetExecute, ScopeFleetOperate},
		} {
			if _, err := h.deviceEnrollmentScopesForPlatform(FleetExecutorPlatform, combo); err == nil {
				t.Fatalf("combined %v accepted", combo)
			}
		}
		// Unchanged behaviour for other requests.
		if _, err := h.deviceEnrollmentScopesForPlatform("ios", []string{ScopeAPIRead}); err != nil {
			t.Fatal(err)
		}
	}
	// Not issuable as a plain managed token.
	if _, err := NewManagedAccessTokenRecord("x", []string{ScopeFleetExecute}, time.Hour, time.Now()); err == nil {
		t.Fatal("non-device managed token granted fleet:execute")
	}
	cfg := &config.Config{APIToken: "0123456789abcdef0123456789abcdef"}
	if _, _, err := issueDeviceTokenForPlatform(cfg, "ios", "d", "s", []string{ScopeFleetExecute}, "old", 0); err == nil {
		t.Fatal("issue for wrong platform accepted")
	}
}

func TestEtcdFleetExecutorEnrollmentHandlerRejectsEveryOtherPlatformAndScope(t *testing.T) {
	h := NewEtcdFleetExecutorEnrollmentHandler(&config.Config{}, nil)
	for _, tc := range []struct {
		platform string
		scopes   []string
	}{
		{platform: "ios", scopes: []string{ScopeAPIRead}},
		{platform: FleetExecutorPlatform, scopes: []string{ScopeAPIRead}},
		{platform: FleetExecutorPlatform, scopes: []string{ScopeFleetExecute, ScopeAPIRead}},
		{platform: FleetExecutorPlatform},
	} {
		if _, err := h.deviceEnrollmentScopesForPlatform(tc.platform, tc.scopes); err == nil {
			t.Fatalf("restricted etcd enrollment accepted platform=%q scopes=%v", tc.platform, tc.scopes)
		}
	}
	if got, err := h.deviceEnrollmentScopesForPlatform(FleetExecutorPlatform, []string{ScopeFleetExecute}); err != nil || !reflect.DeepEqual(got, []string{ScopeFleetExecute}) {
		t.Fatalf("exact executor enrollment rejected: scopes=%v err=%v", got, err)
	}

	// Exercise the public route handler and prove rejection happens before a
	// missing identity store or key validation can turn it into another result.
	req := httptest.NewRequest(http.MethodPost, "https://authority.example/api/v1/enrollments", strings.NewReader(`{"deviceName":"wrong","platform":"ios","requestedScopes":["api:read"]}`))
	rec := httptest.NewRecorder()
	h.StartDeviceEnrollment(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_scope") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestExecutorTokenLifetimeSevenDays(t *testing.T) {
	want := 7 * 24 * time.Hour
	for _, p := range []string{"fleet-executor", " Fleet-Executor "} {
		if got := deviceTokenLifetimeForPlatform(p); got != want {
			t.Fatalf("%q lifetime %v", p, got)
		}
	}
	if deviceTokenLifetimeForPlatform("ios") != deviceTokenTTL || deviceTokenLifetimeForPlatform("web") != browserDeviceTokenTTL {
		t.Fatal("other lifetimes changed")
	}
	cfg := &config.Config{APIToken: "0123456789abcdef0123456789abcdef", FleetAuthorityOnly: true}
	_, rec, err := issueDeviceTokenForPlatform(cfg, FleetExecutorPlatform, "d", "s", []string{ScopeFleetExecute}, "", deviceTokenLifetimeForPlatform(FleetExecutorPlatform))
	if err != nil || rec.ExpiresAt.Sub(rec.IssuedAt) != want {
		t.Fatalf("issued %v %v", rec, err)
	}
}

func TestExecutorRotateKeepsScope(t *testing.T) {
	for _, authority := range []bool{false, true} {
		cfg := &config.Config{APIToken: "0123456789abcdef0123456789abcdef", FleetAuthorityOnly: authority}
		auth := &lifecycleAuthStore{platform: FleetExecutorPlatform}
		p := executorPrincipal()
		req := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/auth/rotate", nil), &p)
		rec := httptest.NewRecorder()
		RotateManagedToken(cfg, auth, nil, rec, req)
		if rec.Code != http.StatusOK || auth.issued == nil {
			t.Fatalf("authority=%v status=%d body=%s", authority, rec.Code, rec.Body.String())
		}
		if !reflect.DeepEqual(auth.issued.Scopes, []string{ScopeFleetExecute}) || auth.issued.RotatedFrom != "jti" ||
			auth.issued.ExpiresAt.Sub(auth.issued.IssuedAt) != 7*24*time.Hour {
			t.Fatalf("rotated token = %+v", auth.issued)
		}
	}
	// A fleet:execute principal on a device of another platform cannot rotate.
	auth := &lifecycleAuthStore{platform: "ios"}
	p := executorPrincipal()
	req := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/auth/rotate", nil), &p)
	rec := httptest.NewRecorder()
	RotateManagedToken(&config.Config{APIToken: "0123456789abcdef0123456789abcdef"}, auth, nil, rec, req)
	if rec.Code == http.StatusOK || auth.issued != nil {
		t.Fatalf("wrong-platform rotation status=%d", rec.Code)
	}
}

func TestExecutorScopeNeverViaOIDC(t *testing.T) {
	p := executorPrincipal()
	p.CI = &CIIdentity{Provider: "github-actions", Repository: "o/r"}
	if IsFleetExecutorPrincipal(p) {
		t.Fatal("CI principal satisfied executor lane")
	}
	// A signed token carrying CI claims and the scope still fails the helper.
	secret := "0123456789abcdef0123456789abcdef"
	now := time.Now().UTC()
	token, err := signToken(secret, tokenClaims{
		Sub: "ci", Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(), Jti: "ci-jti", Did: "d",
		Iss: "norn", Aud: "norn-control", Use: "access", Managed: true,
		Scopes: []string{ScopeFleetExecute}, CI: p.CI,
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, ok := VerifyAccessTokenWithIdentityStore(secret, token, time.Time{}, activeIdentityStore{})
	if !ok {
		t.Fatal("test token did not verify")
	}
	if IsFleetExecutorPrincipal(*principal) {
		t.Fatal("verified CI token satisfied executor lane")
	}
}

func TestExecutorTokenRevokedAtRequestTime(t *testing.T) {
	cfg := &config.Config{APIToken: "0123456789abcdef0123456789abcdef", FleetAuthorityOnly: true}
	token, _, err := issueDeviceTokenForPlatform(cfg, FleetExecutorPlatform, "d", "s", []string{ScopeFleetExecute}, "", fleetExecutorTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	principal, ok := VerifyAccessTokenWithIdentityStore(cfg.APIToken, token, time.Time{}, activeIdentityStore{})
	if !ok || !IsFleetExecutorPrincipal(*principal) {
		t.Fatal("live executor token rejected")
	}
	if _, ok := VerifyAccessTokenWithIdentityStore(cfg.APIToken, token, time.Time{}, revokedIdentityStore{}); ok {
		t.Fatal("revoked executor token authenticated")
	}
}

type revokedIdentityStore struct{ store.IdentityStore }

func (revokedIdentityStore) AccessTokenActive(context.Context, string) (bool, error) {
	return false, nil
}

type activeIdentityStore struct{ store.IdentityStore }

func (activeIdentityStore) AccessTokenActive(context.Context, string) (bool, error) { return true, nil }
func (activeIdentityStore) TouchAccessDevice(context.Context, string) error         { return nil }
