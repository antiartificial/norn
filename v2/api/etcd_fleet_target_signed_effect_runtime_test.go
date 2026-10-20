package main

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/config"
	"norn/v2/api/handler"
)

type targetEffectIdentity struct{ signedEffectTransportIdentity }

func (targetEffectIdentity) TouchAccessDevice(context.Context, string) error { return nil }

// Sign a test-only managed device JWT so the route exercises the real etcd
// middleware as well as the exact subject, device and scope checks.
func targetEffectTestToken(t *testing.T, secret, subject, device, id, scope string, expiry time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"sub": subject, "iss": "norn", "aud": "norn-control", "use": "access", "managed": true,
		"iat": time.Now().Add(-time.Minute).Unix(), "exp": expiry.Unix(), "jti": id, "did": device, "scp": []string{scope},
	})
	if err != nil {
		t.Fatal(err)
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	key := hmac.New(sha256.New, []byte(secret))
	_, _ = key.Write([]byte("norn.jwt-signing/v1"))
	mac := hmac.New(sha256.New, key.Sum(nil))
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestTargetSignedEffectRoutesRequireReconciledTrustAndExplicitGate(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	base := "/api/v1/fleet/offline/attempts/" + effectTransportPlan + "/" + effectTransportAttempt + "/signed-effect/effect-issue/challenge"
	fixture, transport := effectTransportFixture()
	makeTrust := func() *targetTerminalTrust {
		return &targetTerminalTrust{transport: transport, generation: 2, observerKeyID: "key-2", observerPublic: make(ed25519.PublicKey, ed25519.PublicKeySize)}
	}
	call := func(router http.Handler, token string) int {
		req := httptest.NewRequest(http.MethodGet, base, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}
	runner := targetEffectTestToken(t, secret, "runner", "runner-device", "runner-token", handler.ScopeFleetOperate, time.Now().Add(time.Hour))
	verifier := targetEffectTestToken(t, secret, "verifier", "verifier-device", "verifier-token", handler.ScopeFleetVerify, time.Now().Add(time.Hour))
	for _, tc := range []struct {
		name      string
		enabled   bool
		trust     *targetTerminalTrust
		reconcile bool
		cfgChange func(*config.Config)
	}{
		{name: "disabled", trust: makeTrust(), reconcile: true},
		{name: "terminal disabled", enabled: true, trust: makeTrust(), reconcile: true, cfgChange: func(c *config.Config) { c.FleetTargetTerminalEnabled = false }},
		{name: "authority only", enabled: true, trust: makeTrust(), reconcile: true, cfgChange: func(c *config.Config) { c.FleetAuthorityOnly = true }},
		{name: "no target trust", enabled: true},
		{name: "unreconciled", enabled: true, trust: makeTrust()},
		{name: "wrong observer trust", enabled: true, trust: makeTrust(), reconcile: true},
		{name: "expired manifest", enabled: true, trust: func() *targetTerminalTrust {
			v := makeTrust()
			v.transport.expiresAt = time.Now().Add(-time.Second)
			return v
		}(), reconcile: true},
		{name: "enabled", enabled: true, trust: makeTrust(), reconcile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			if tc.trust != nil && tc.reconcile {
				pin := &terminalTrustStoreFixture{}
				if tc.name == "wrong observer trust" {
					pin.err = context.Canceled
				}
				err := tc.trust.reconcile(context.Background(), pin)
				if (err != nil) != (tc.name == "wrong observer trust") {
					t.Fatalf("reconcile error = %v", err)
				}
			}
			cfg := &config.Config{APIToken: secret, FleetTargetTerminalEnabled: true, FleetTargetSignedEffectEnabled: tc.enabled}
			if tc.cfgChange != nil {
				tc.cfgChange(cfg)
			}
			registerEtcdFleetTargetSignedEffectRoutes(router, cfg, targetEffectIdentity{}, fixture, tc.trust)
			if tc.name != "enabled" {
				if got := call(router, verifier); got != http.StatusNotFound {
					t.Fatalf("route mounted without valid gate and trust: %d", got)
				}
				return
			}
			if got := call(router, verifier); got != http.StatusOK || fixture.issued != 0 {
				t.Fatalf("pinned verifier route status=%d issue count=%d", got, fixture.issued)
			}
			request := httptest.NewRequest(http.MethodPost, base, strings.NewReader(`{}`))
			request.Header.Set("Authorization", "Bearer "+verifier)
			record := httptest.NewRecorder()
			router.ServeHTTP(record, request)
			if record.Code != http.StatusAccepted || fixture.issued != 1 {
				t.Fatalf("pinned verifier issue status=%d issue count=%d", record.Code, fixture.issued)
			}
			if got := call(router, runner); got != http.StatusForbidden {
				t.Fatalf("runner entered verifier route: %d", got)
			}
			grantPath := strings.Replace(base, "/challenge", "/runner/grants/"+effectTransportChallenge, 1)
			grantRequest := httptest.NewRequest(http.MethodGet, grantPath, nil)
			grantRequest.Header.Set("Authorization", "Bearer "+runner)
			grantRecord := httptest.NewRecorder()
			router.ServeHTTP(grantRecord, grantRequest)
			if grantRecord.Code != http.StatusOK {
				t.Fatalf("pinned runner grant read status=%d", grantRecord.Code)
			}
			grantRequest.Header.Set("Authorization", "Bearer "+verifier)
			grantRecord = httptest.NewRecorder()
			router.ServeHTTP(grantRecord, grantRequest)
			if grantRecord.Code != http.StatusForbidden {
				t.Fatalf("verifier entered runner route: %d", grantRecord.Code)
			}
			observer := targetEffectTestToken(t, secret, "observer", "observer-device", "observer-token", handler.ScopeFleetObserve, time.Now().Add(time.Hour))
			observerPath := strings.Replace(base, "/challenge", "/observer/challenge", 1)
			observe := httptest.NewRequest(http.MethodGet, observerPath, nil)
			observe.Header.Set("Authorization", "Bearer "+observer)
			observed := httptest.NewRecorder()
			router.ServeHTTP(observed, observe)
			if observed.Code != http.StatusOK {
				t.Fatalf("pinned observer read status=%d", observed.Code)
			}
			observe.Header.Set("Authorization", "Bearer "+verifier)
			observed = httptest.NewRecorder()
			router.ServeHTTP(observed, observe)
			if observed.Code != http.StatusForbidden {
				t.Fatalf("verifier entered observer route: %d", observed.Code)
			}
			for label, token := range map[string]string{
				"wrong device":  targetEffectTestToken(t, secret, "verifier", "runner-device", "bad-device", handler.ScopeFleetVerify, time.Now().Add(time.Hour)),
				"wrong subject": targetEffectTestToken(t, secret, "observer", "verifier-device", "bad-subject", handler.ScopeFleetVerify, time.Now().Add(time.Hour)),
				"expired":       targetEffectTestToken(t, secret, "verifier", "verifier-device", "expired", handler.ScopeFleetVerify, time.Now().Add(-time.Second)),
			} {
				got := call(router, token)
				if got != http.StatusForbidden && !(label == "expired" && got == http.StatusUnauthorized) {
					t.Errorf("%s identity admitted: %d", label, got)
				}
			}
		})
	}
	if fixture.issued != 1 {
		t.Fatalf("wrong identities changed ledger: %d", fixture.issued)
	}
}
