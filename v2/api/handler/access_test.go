package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccessMiddlewareRecordsProxySafeMetadata(t *testing.T) {
	h := &Handler{access: NewAccessLog(10)}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/health?secret=hidden", nil)
	req.RemoteAddr = "10.0.0.2:12345"
	req.Header.Set("CF-Connecting-IP", "203.0.113.10")
	req.Header.Set("Cf-Access-Authenticated-User-Email", "operator@example.test")
	req.Header.Set("Authorization", "Bearer should-not-appear")
	rec := httptest.NewRecorder()

	h.AccessMiddleware(next).ServeHTTP(rec, req)

	events := h.access.Recent(1)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Status != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", event.Status, http.StatusAccepted)
	}
	if event.Path != "/api/health" {
		t.Fatalf("path = %q, want /api/health", event.Path)
	}
	if event.ClientIP != "203.0.113.10" || event.CFIP != "203.0.113.10" {
		t.Fatalf("client ip = %q cf ip = %q", event.ClientIP, event.CFIP)
	}
	if event.CFEmail != "operator@example.test" {
		t.Fatalf("cf email = %q", event.CFEmail)
	}
}

func TestAccessEventsRespectsLimit(t *testing.T) {
	log := NewAccessLog(3)
	log.Record(AccessEvent{Path: "/oldest"})
	log.Record(AccessEvent{Path: "/middle"})
	log.Record(AccessEvent{Path: "/newest"})
	h := &Handler{access: log}

	req := httptest.NewRequest(http.MethodGet, "/api/access/events?limit=2", nil)
	rec := httptest.NewRecorder()
	h.AccessEvents(rec, req)

	var events []AccessEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
}

func TestAccessMiddlewareBypassesExecWebSocket(t *testing.T) {
	h := &Handler{access: NewAccessLog(10)}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusSwitchingProtocols)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/apps/contextdb/exec", nil)
	rec := httptest.NewRecorder()

	h.AccessMiddleware(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler was not called")
	}
	if rec.Code != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSwitchingProtocols)
	}
	if got := len(h.access.Recent(10)); got != 0 {
		t.Fatalf("access events = %d, want 0", got)
	}
}
