package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

func TestOfflineEffectTransportRefusesUnauthorizedAndMissingObserver(t *testing.T) {
	router := chi.NewRouter()
	trust := &offlineFleetTrust{runnerSubject: "offline-runner"}
	identities := offlineTestIdentity{device: &store.AccessDevice{ID: "runner-device"}}
	principal := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "offline-runner", TokenID: "runner-token", DeviceID: "runner-device", Scopes: []string{handler.ScopeFleetOperate}}
	var inject *handler.AccessPrincipal
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if inject != nil {
				r = handler.WithAccessPrincipal(r, inject)
			}
			next.ServeHTTP(w, r)
		})
	}
	registerEtcdFleetOfflineEffectRoutes(router, auth, identities, trust, nil)
	base := "/api/v1/fleet/offline/attempts/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/effect-grants"
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, base}, {http.MethodPost, base + "/33333333-3333-4333-8333-333333333333/consume"},
		{http.MethodGet, base},
		{http.MethodGet, base + "/33333333-3333-4333-8333-333333333333"},
	} {
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s without runner = %d", tc.path, response.Code)
		}
		inject = &principal
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "offline_effect_observer_unavailable") {
			t.Fatalf("%s with incomplete observer = %d %s", tc.path, response.Code, response.Body.String())
		}
		inject = nil
	}
	other := principal
	other.Subject = "other"
	inject = &other
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, base, strings.NewReader(`{}`)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong runner subject = %d", response.Code)
	}
}

func TestOfflineEffectRuntimeCannotActivateWithoutManifest(t *testing.T) {
	cfg := &config.Config{FleetAuthorityOnly: true, Profile: "staging", Environment: "staging"}
	if offlineEffectAuthorityForRuntime(cfg, nil) != nil {
		t.Fatal("absent observer manifest activated")
	}
}
