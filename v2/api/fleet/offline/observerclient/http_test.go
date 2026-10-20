package observerclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

const testPlanID = "11111111-1111-4111-8111-111111111111"
const testAttemptID = "22222222-2222-4222-8222-222222222222"

func testHTTPClient(t *testing.T, h http.Handler) (*HTTPClient, *httptest.Server) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	certDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fleet.test"},
		DNSNames: []string{"fleet.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fleet.test"},
		DNSNames: []string{"fleet.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(h)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{certDER}, PrivateKey: priv}}}
	s.StartTLS()
	t.Cleanup(s.Close)
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	addr := strings.TrimPrefix(s.URL, "https://")
	c, err := NewHTTPClient(HTTPConfig{ServerName: "fleet.test", Address: addr, RootCAs: pool,
		ObserverBearer: "observer-secret"})
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}

func TestHTTPClientPinsTLSAndUsesOnlyObserverScope(t *testing.T) {
	grant := offline.EffectGrant{SchemaVersion: offline.EffectGrantSchema, PlanID: testPlanID, AttemptID: testAttemptID, Phase: "consumed"}
	now := time.Now().UTC()
	grant.ConsumedAt = &now
	seen := map[string]string{}
	c, s := testHTTPClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = r.Header.Get("Authorization")
		if r.Host != "fleet.test" {
			t.Errorf("unexpected host %s", r.Host)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		switch r.URL.Path {
		case "/api/v1/fleet/offline/attempts/" + testPlanID + "/" + testAttemptID + "/terminal-observer/grant":
			_ = json.NewEncoder(w).Encode(map[string]any{"authorizesEffect": false, "grant": grant})
		case "/api/v1/fleet/offline/attempts/" + testPlanID + "/" + testAttemptID + "/terminal-observer/challenge":
			_ = json.NewEncoder(w).Encode(map[string]any{"authorizesEffect": false, "challenge": offline.TerminalChallengeV1{SchemaVersion: offline.TerminalChallengeSchemaV1, PlanID: testPlanID, AttemptID: testAttemptID}})
		default:
			http.NotFound(w, r)
		}
	}))
	if _, err := c.ReadConsumedGrant(context.Background(), testPlanID, testAttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadChallenge(context.Background(), testPlanID, testAttemptID); err != nil {
		t.Fatal(err)
	}
	if seen["/api/v1/fleet/offline/attempts/"+testPlanID+"/"+testAttemptID+"/terminal-observer/grant"] != "Bearer observer-secret" ||
		seen["/api/v1/fleet/offline/attempts/"+testPlanID+"/"+testAttemptID+"/terminal-observer/challenge"] != "Bearer observer-secret" {
		t.Fatal("observer principal was not used on both reads")
	}
	c.client.Transport.(*http.Transport).TLSClientConfig.ServerName = "wrong.test"
	if _, err := c.ReadConsumedGrant(context.Background(), testPlanID, testAttemptID); err == nil {
		t.Fatal("wrong certificate identity accepted")
	}
	_ = s
}

func TestHTTPClientRejectsRedirectProxyAndAmbiguousBodies(t *testing.T) {
	c, _ := testHTTPClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("redirect") != "" {
			http.Redirect(w, r, "https://elsewhere.test", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		_, _ = w.Write([]byte(`{"authorizesEffect":false,"grant":{},"grant":{}}`))
	}))
	if _, err := c.ReadConsumedGrant(context.Background(), testPlanID, testAttemptID); err == nil {
		t.Fatal("duplicate JSON keys accepted")
	}
	proxy := os.Getenv("HTTPS_PROXY")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	defer os.Setenv("HTTPS_PROXY", proxy)
	// The direct pinned dial remains usable regardless of an inherited proxy.
	if _, err := c.ReadConsumedGrant(context.Background(), testPlanID, testAttemptID); err == nil || strings.Contains(err.Error(), "proxyconnect") {
		t.Fatalf("proxy used or invalid response accepted: %v", err)
	}
	r, _ := testHTTPClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "https://elsewhere.test", http.StatusFound)
	}))
	if _, err := r.ReadConsumedGrant(context.Background(), testPlanID, testAttemptID); err == nil {
		t.Fatal("redirect accepted")
	}
}

// A generic ingress 404 must never be interpreted as authoritative absence.
func TestHTTPClientRequiresExactAbsenceProblem(t *testing.T) {
	for _, body := range []string{"404 page not found", `{"error":"not_found"}`,
		`{"code":"offline_terminal_not_found","status":404,"type":"https://norn.dev/problems/offline_terminal_not_found"}`} {
		t.Run(body, func(t *testing.T) {
			c, _ := testHTTPClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.Header().Set("Cache-Control", "private, no-store")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(body))
			}))
			_, err := c.ReadTerminal(context.Background(), testPlanID, testAttemptID)
			if errors.Is(err, ErrNotFound) != strings.Contains(body, "offline_terminal_not_found") {
				t.Fatalf("absence classification: %v", err)
			}
		})
	}
}
