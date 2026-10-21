package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type platformReleaseAuthIdentity struct{ store.IdentityStore }

func (platformReleaseAuthIdentity) AccessTokenActive(context.Context, string) (bool, error) {
	return true, nil
}
func (platformReleaseAuthIdentity) TouchAccessDevice(context.Context, string) error { return nil }

func platformReleaseTestToken(t *testing.T, secret string, scopes []string, deviceID string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims := map[string]interface{}{"sub": "norn-exec", "iss": "norn", "aud": "norn-control", "use": "access", "managed": true, "iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix(), "jti": "test-token", "did": deviceID, "scp": scopes}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(body)
	key := hmac.New(sha256.New, []byte(secret))
	_, _ = key.Write([]byte("norn.jwt-signing/v1"))
	mac := hmac.New(sha256.New, key.Sum(nil))
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestPlatformReleaseCheckpointReadAuth(t *testing.T) {
	cfg := &config.Config{APIToken: "platform-release-auth-test-secret"}
	identities := platformReleaseAuthIdentity{}
	path := "/api/v1/fleet/platform-releases/example/attempt/checkpoints"
	for _, tc := range []struct {
		name            string
		scopes          []string
		deviceID        string
		readOK, writeOK bool
	}{
		{"enrolled executor", []string{handler.ScopeFleetExecute}, "device-1", true, true},
		{"operator reader", []string{handler.ScopeAPIRead}, "", true, false},
		{"generic fleet operator", []string{handler.ScopeFleetOperate}, "", false, false},
		{"widened executor", []string{handler.ScopeFleetExecute, handler.ScopeAPIRead}, "device-1", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := platformReleaseTestToken(t, cfg.APIToken, tc.scopes, tc.deviceID)
			for _, route := range []struct {
				method     string
				middleware func(http.Handler) http.Handler
				want       bool
			}{
				{http.MethodGet, platformReleaseReadOrExecutorAuth(cfg, identities), tc.readOK},
				{http.MethodPost, platformReleaseExecutorAuth(cfg, identities), tc.writeOK},
			} {
				called := false
				wrapped := route.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) }))
				req := httptest.NewRequest(route.method, path, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				res := httptest.NewRecorder()
				wrapped.ServeHTTP(res, req)
				if called != route.want {
					t.Errorf("%s called=%v want=%v status=%d", route.method, called, route.want, res.Code)
				}
			}
		})
	}
}

func TestEtcdPlatformReleaseIntentRoutesDefaultOff(t *testing.T) {
	router := chi.NewRouter()
	registerEtcdPlatformReleaseIntentRoutes(router, &config.Config{}, nil, nil)
	for _, path := range []string{"/api/v1/fleet/platform-releases", "/api/v1/fleet/platform-releases/example", "/api/v1/fleet/platform-releases/example/attempt"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s: expected disabled route, got %d", path, response.Code)
		}
	}
}
