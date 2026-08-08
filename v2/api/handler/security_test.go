package handler

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"norn/v2/api/store"
)

func TestControlJSONIsBoundedAndStrict(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		ok   bool
	}{
		{name: "valid", body: `{"name":"device"}`, ok: true},
		{name: "unknown field", body: `{"name":"device","unexpected":true}`},
		{name: "trailing value", body: `{"name":"device"}{"name":"other"}`},
		{name: "oversized", body: `{"name":"` + strings.Repeat("x", maxControlJSONBody) + `"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/test", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()
			var target struct {
				Name string `json:"name"`
			}
			err := decodeControlJSON(rec, req, &target)
			if (err == nil) != tt.ok {
				t.Fatalf("error = %v, want success %v", err, tt.ok)
			}
		})
	}
}

func TestSensitiveResponsesDisableCaching(t *testing.T) {
	rec := httptest.NewRecorder()
	preventSensitiveResponseCaching(rec)
	writeJSON(rec, map[string]string{"token": "secret"})
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %#v", rec.Header())
	}
}

func TestAdminControlBoundaryRequiresPrincipal(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enrollments/approve", nil)
	rec := httptest.NewRecorder()
	if _, ok := requireControlScope(rec, req, ScopeAdmin); ok || rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing principal status = %d", rec.Code)
	}

	req = WithAccessPrincipal(req, &AccessPrincipal{Scopes: []string{ScopeAPIWrite}})
	rec = httptest.NewRecorder()
	if _, ok := requireControlScope(rec, req, ScopeAdmin); ok || rec.Code != http.StatusForbidden {
		t.Fatalf("unscoped principal status = %d", rec.Code)
	}

	req = WithAccessPrincipal(req, &AccessPrincipal{Scopes: []string{ScopeAdmin}})
	rec = httptest.NewRecorder()
	if _, ok := requireControlScope(rec, req, ScopeAdmin); !ok {
		t.Fatalf("admin principal rejected: %s", rec.Body.String())
	}
}

func TestVersionedMaintenanceRequiresScopedPrincipal(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/smoke", nil)
	rec := httptest.NewRecorder()
	h.QueuePlatformSmoke(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous maintenance status = %d", rec.Code)
	}

	req = WithAccessPrincipal(req, &AccessPrincipal{Scopes: []string{ScopeAPIRead}})
	rec = httptest.NewRecorder()
	h.QueuePlatformSmoke(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read-only maintenance status = %d", rec.Code)
	}
}

func TestEnrollmentSourceOnlyTrustsForwardingFromLoopback(t *testing.T) {
	remote := httptest.NewRequest(http.MethodPost, "/api/v1/enrollments", nil)
	remote.RemoteAddr = "100.64.0.8:1234"
	remote.Header.Set("CF-Connecting-IP", "203.0.113.4")
	remoteHash := enrollmentRequestSourceHash("test-secret", remote)
	remote.Header.Del("CF-Connecting-IP")
	if remoteHash != enrollmentRequestSourceHash("test-secret", remote) {
		t.Fatal("remote caller must not control its rate-limit identity with forwarding headers")
	}

	proxied := httptest.NewRequest(http.MethodPost, "/api/v1/enrollments", nil)
	proxied.RemoteAddr = "127.0.0.1:1234"
	proxied.Header.Set("CF-Connecting-IP", "203.0.113.4")
	if enrollmentRequestSourceHash("test-secret", proxied) == enrollmentRequestSourceHash("test-secret", remote) {
		t.Fatal("trusted loopback proxy should attribute the forwarded client")
	}
}

func TestPairingRequiresTrustedTLSOutsideLoopback(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		tls        bool
		headers    map[string]string
		want       bool
	}{
		{name: "direct tls", remoteAddr: "100.64.0.8:1234", tls: true, want: true},
		{name: "direct loopback development", remoteAddr: "127.0.0.1:1234", want: true},
		{name: "trusted https proxy", remoteAddr: "127.0.0.1:1234", headers: map[string]string{"CF-Connecting-IP": "203.0.113.4", "X-Forwarded-Proto": "https"}, want: true},
		{name: "trusted Cloudflare visitor", remoteAddr: "127.0.0.1:1234", headers: map[string]string{"CF-Connecting-IP": "203.0.113.4", "CF-Visitor": `{"scheme":"https"}`}, want: true},
		{name: "forwarded cleartext", remoteAddr: "127.0.0.1:1234", headers: map[string]string{"CF-Connecting-IP": "203.0.113.4"}},
		{name: "remote forwarding spoof", remoteAddr: "100.64.0.8:1234", headers: map[string]string{"X-Forwarded-For": "127.0.0.1", "X-Forwarded-Proto": "https"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/enrollments", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.tls {
				req.TLS = &tls.ConnectionState{}
			}
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}
			if got := pairingTransportAllowed(req); got != tt.want {
				t.Fatalf("allowed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExecAuditDoesNotSerializeArgv(t *testing.T) {
	session := store.ExecSession{ID: "session-1", Command: []string{"tool", "--token", "secret-value"}, CommandDigest: strings.Repeat("a", 64)}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("secret-value")) || bytes.Contains(encoded, []byte("--token")) {
		t.Fatalf("audit JSON leaked argv: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(session.CommandDigest)) {
		t.Fatalf("audit JSON omitted digest: %s", encoded)
	}
}
