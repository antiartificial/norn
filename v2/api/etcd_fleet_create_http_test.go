package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
)

func TestEtcdFleetCreateRoutesRequireBothDefaultOffGates(t *testing.T) {
	paths := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/fleet/create/fresh/intents"},
		{http.MethodGet, "/api/v1/fleet/create/fresh/intents/id/dispatch-binding"},
		{http.MethodGet, "/api/v1/fleet/create/fresh/intents/id/attempts/attempt"},
		{http.MethodGet, "/api/v1/fleet/create/fresh/intents/id/attempts/attempt/admission"},
		{http.MethodPost, "/api/v1/fleet/create/fresh/intents/id/attempts/attempt/effect-grant"},
		{http.MethodGet, "/api/v1/fleet/create/fresh/intents/id/attempts/attempt/effect-grant"},
		{http.MethodPost, "/api/v1/fleet/create/fresh/intents/id/attempts/attempt/effect-grant/consume"},
		{http.MethodGet, "/api/v1/fleet/create/fresh/intents/id/attempts/attempt/effect-grant/status"},
		{http.MethodPost, "/api/v1/fleet/create/fresh/intents/id/attempts/attempt/terminal-receipt"},
		{http.MethodGet, "/api/v1/fleet/create/fresh/intents/id/terminal-receipt"},
		{http.MethodPost, "/api/v1/fleet/create/fresh/intents/id/target-registration"},
	}
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		lane bool
		want int
	}{
		{name: "nil config", cfg: nil, lane: true, want: http.StatusNotFound},
		{name: "feature flag off", cfg: &config.Config{APIToken: strings.Repeat("k", 32)}, lane: true, want: http.StatusNotFound},
		{name: "executor lane off", cfg: &config.Config{APIToken: strings.Repeat("k", 32), FleetCreateEnabled: true}, lane: false, want: http.StatusNotFound},
		{name: "both gates on", cfg: &config.Config{APIToken: strings.Repeat("k", 32), FleetCreateEnabled: true}, lane: true, want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, route := range paths {
				router := chi.NewRouter()
				registerEtcdFleetCreateRoutes(router, tc.cfg, nil, nil, tc.lane)
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
				if recorder.Code != tc.want {
					t.Errorf("method=%s path=%s status=%d body=%s want=%d", route.method, route.path, recorder.Code, recorder.Body.String(), tc.want)
				}
			}
		})
	}
}
