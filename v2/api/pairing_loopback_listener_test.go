package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPairingLoopbackListenerOnlyForwardsPairingRoutes(t *testing.T) {
	forwarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	server := pairingOnlyHandler(forwarded)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/api/v1/fleet/platform-operator/enrollments", http.StatusAccepted},
		{http.MethodGet, "/api/v1/fleet/platform-operator/enrollments", http.StatusAccepted},
		{http.MethodPost, "/api/v1/fleet/platform-operator/enrollments/approve", http.StatusAccepted},
		{http.MethodPost, "/api/v1/fleet/platform-operator/enrollments/abc/exchange", http.StatusAccepted},
		{http.MethodPost, "/api/v1/auth/revoke", http.StatusNotFound},
		{http.MethodGet, "/api/v1/operations/abc", http.StatusNotFound},
		{http.MethodDelete, "/api/v1/fleet/platform-operator/enrollments", http.StatusNotFound},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		if resp.Code != tc.want {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, resp.Code, tc.want)
		}
	}
}
