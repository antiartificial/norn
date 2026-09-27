package ingress

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func verifiedReadbackRequest(t *testing.T, path, identity string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	uri, err := url.Parse(identity)
	if err != nil {
		t.Fatal(err)
	}
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{URIs: []*url.URL{uri}}}}}
	return request
}

func TestNodeReadbackRequiresPinnedIdentityAndBoundsTraefik(t *testing.T) {
	const identity = "spiffe://norn.test/control/ingress"
	routes := t.TempDir()
	traefik := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/rawdata" {
			t.Errorf("unexpected Traefik path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"routers":{},"services":{}}`))
	}))
	defer traefik.Close()
	handler, err := NewNodeReadbackHandler(routes, traefik.URL, identity)
	if err != nil {
		t.Fatal(err)
	}
	request := verifiedReadbackRequest(t, "/api/rawdata", identity)
	request.TLS = nil
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unverified request status = %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, verifiedReadbackRequest(t, "/api/rawdata", "spiffe://norn.test/other"))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong client identity status = %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, verifiedReadbackRequest(t, "/api/rawdata", identity))
	if response.Code != http.StatusOK || response.Body.String() != `{"routers":{},"services":{}}` {
		t.Fatalf("effective readback = %d %q", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, verifiedReadbackRequest(t, "/api/rawdata?extra=1", identity))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("query-bearing readback status = %d", response.Code)
	}
	request = verifiedReadbackRequest(t, "/api/rawdata", identity)
	request.Method = http.MethodPost
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("mutating readback method status = %d", response.Code)
	}
}

func TestNodeReadbackReturnsExactLocalRevision(t *testing.T) {
	const identity = "spiffe://norn.test/control/ingress"
	routes := t.TempDir()
	traefik := httptest.NewServer(http.NotFoundHandler())
	defer traefik.Close()
	handler, err := NewNodeReadbackHandler(routes, traefik.URL, identity)
	if err != nil {
		t.Fatal(err)
	}
	desired := fixtureRoute(t, "readback")
	if err := PublishRenderedRoute(routes, desired, PublishedRouteRevision{}, 1); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, verifiedReadbackRequest(t, "/v1/routes/"+desired.RouterName+"/revision", identity))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), desired.SHA256) || !strings.Contains(response.Body.String(), `"generation":1`) {
		t.Fatalf("local route revision = %d %q", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, verifiedReadbackRequest(t, "/v1/routes/../revision", identity))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unscoped revision status = %d", response.Code)
	}
}

func TestNodeReadbackRejectsNonLoopbackAndOversizedRawData(t *testing.T) {
	if _, err := NewNodeReadbackHandler(t.TempDir(), "http://example.com:18081", "spiffe://norn.test/control"); err == nil {
		t.Fatal("non-loopback Traefik origin was accepted")
	}
	traefik := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":"` + strings.Repeat("x", 1<<20) + `"}`))
	}))
	defer traefik.Close()
	handler, err := NewNodeReadbackHandler(t.TempDir(), traefik.URL, "spiffe://norn.test/control")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, verifiedReadbackRequest(t, "/api/rawdata", "spiffe://norn.test/control"))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("oversized Traefik response status = %d", response.Code)
	}
}

func TestNodeReadbackRefusesTraefikRedirect(t *testing.T) {
	const identity = "spiffe://norn.test/control"
	traefik := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/api/rawdata", http.StatusFound)
	}))
	defer traefik.Close()
	handler, err := NewNodeReadbackHandler(t.TempDir(), traefik.URL, identity)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, verifiedReadbackRequest(t, "/api/rawdata", identity))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("redirecting Traefik response status = %d", response.Code)
	}
}
