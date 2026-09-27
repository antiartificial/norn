package ingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicProbePreservesHostAndRejectsWrongResponse(t *testing.T) {
	desired, err := RenderWeightedRoute(WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "http://orders.example.com",
		Backends: []WeightedBackend{{DeploymentID: "old", Weight: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "orders.example.com" || r.URL.Path != "/readyz" || r.Header.Get("Cache-Control") != "no-store" {
			http.Error(w, "wrong request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("orders-ready-v3"))
	}))
	defer server.Close()
	dialAddress := strings.TrimPrefix(server.URL, "http://")
	digest := sha256.Sum256([]byte("orders-ready-v3"))
	if err := probeRenderedRoutePublic(context.Background(), desired, "/readyz", hex.EncodeToString(digest[:]), nil, dialAddress); err != nil {
		t.Fatal(err)
	}
	wrong := sha256.Sum256([]byte("other-application"))
	if err := probeRenderedRoutePublic(context.Background(), desired, "/readyz", hex.EncodeToString(wrong[:]), nil, dialAddress); err == nil {
		t.Fatal("accepted wrong public endpoint identity")
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example.com/readyz", http.StatusFound)
	}))
	defer redirect.Close()
	if err := probeRenderedRoutePublic(context.Background(), desired, "/readyz", hex.EncodeToString(digest[:]), nil, strings.TrimPrefix(redirect.URL, "http://")); err == nil {
		t.Fatal("accepted public endpoint redirect")
	}
}

func TestReleaseTrafficRequiresTLSRoute(t *testing.T) {
	secure := fixtureRoute(t, "release-a")
	if err := RequireTLSRenderedRoute(secure); err != nil {
		t.Fatal(err)
	}
	plain, err := RenderWeightedRoute(WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "http://orders.example.com", Backends: []WeightedBackend{{DeploymentID: "release-a", Weight: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireTLSRenderedRoute(plain); err == nil {
		t.Fatal("plaintext release route accepted")
	}
}
