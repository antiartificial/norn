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

func TestProbeRenderedRouteNodesPreservesHostAndRequiresEveryResponse(t *testing.T) {
	desired, err := RenderWeightedRoute(WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "http://orders.example.com",
		Backends: []WeightedBackend{{DeploymentID: "old", Weight: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	wantBody := []byte("orders-ready-v3")
	digest := sha256.Sum256(wantBody)
	expected := hex.EncodeToString(digest[:])
	newServer := func(body string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != "orders.example.com" || r.URL.Path != "/readyz" {
				http.Error(w, "wrong host or path", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(server.Close)
		return server
	}
	first := newServer(string(wantBody))
	second := newServer(string(wantBody))
	nodes := []RouteProbeNode{{ID: "ingress-a", DialAddress: strings.TrimPrefix(first.URL, "http://")}, {ID: "ingress-b", DialAddress: strings.TrimPrefix(second.URL, "http://")}}
	result, err := ProbeRenderedRouteNodes(context.Background(), nodes, desired, "/readyz", expected, nil)
	if err != nil || len(result) != 2 || result[1].NodeID != "ingress-b" {
		t.Fatalf("two-node endpoint probe=%+v err=%v", result, err)
	}
	stale := newServer("other-application")
	nodes[1].DialAddress = strings.TrimPrefix(stale.URL, "http://")
	if result, err := ProbeRenderedRouteNodes(context.Background(), nodes, desired, "/readyz", expected, nil); err == nil || len(result) != 0 {
		t.Fatalf("accepted wrong second-node response: result=%+v err=%v", result, err)
	}
	nodes[1].DialAddress = nodes[0].DialAddress
	if _, err := ProbeRenderedRouteNodes(context.Background(), nodes, desired, "/readyz", expected, nil); err == nil {
		t.Fatal("accepted duplicate node address")
	}
}

func TestProbeRenderedRouteNodesRejectsTLSPeerForWrongHost(t *testing.T) {
	desired, err := RenderWeightedRoute(WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "https://orders.example.com",
		Backends: []WeightedBackend{{DeploymentID: "old", Weight: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ready")) }))
	defer server.Close()
	digest := sha256.Sum256([]byte("ready"))
	nodes := []RouteProbeNode{{ID: "ingress-a", DialAddress: strings.TrimPrefix(server.URL, "https://")}}
	if _, err := ProbeRenderedRouteNodes(context.Background(), nodes, desired, "/readyz", hex.EncodeToString(digest[:]), nil); err == nil {
		t.Fatal("accepted a TLS certificate for the wrong public hostname")
	}
}
