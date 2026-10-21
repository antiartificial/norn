package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/config"
)

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
