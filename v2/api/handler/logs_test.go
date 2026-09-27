package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// An app-bound credential cannot read another app's runtime logs; its own
// app proceeds to the (here disconnected) runtime.
func TestStreamLogsEnforcesAppBinding(t *testing.T) {
	h := &Handler{}
	read := func(app string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/apps/"+app+"/logs", nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", app)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "ci", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}, App: "shop"})
		rec := httptest.NewRecorder()
		h.StreamLogs(rec, req)
		return rec.Code
	}
	if code := read("other"); code != http.StatusForbidden {
		t.Fatalf("foreign app logs = %d", code)
	}
	if code := read("shop"); code != http.StatusServiceUnavailable {
		t.Fatalf("own app logs = %d", code)
	}
}
