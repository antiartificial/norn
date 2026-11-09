package ingress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestControlRouteAuthorityRequiresMutualTLSNodeAndPinnedServer(t *testing.T) {
	const nodeURI = "spiffe://norn.test/fleet/ingress-01"
	const controlURI = "spiffe://norn.test/control/route-authority"
	now := time.Now()
	base := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "route authority test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caPEM, _, ca, caKey := nodeTestCertificate(t, nil, nil, &base)
	serverURI, _ := url.Parse(controlURI)
	serverTemplate := x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, URIs: []*url.URL{serverURI}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverPEM, serverKey, _, _ := nodeTestCertificate(t, ca, caKey, &serverTemplate)
	nodeURL, _ := url.Parse(nodeURI)
	clientTemplate := x509.Certificate{SerialNumber: big.NewInt(3), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), URIs: []*url.URL{nodeURL}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientPEM, clientKey, _, _ := nodeTestCertificate(t, ca, caKey, &clientTemplate)
	serverIdentity, err := tls.X509KeyPair(serverPEM, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	route := fixtureRoute(t, "authority")
	calls := 0
	handler, err := NewControlRouteAuthorityHandler(map[string]string{nodeURI: "ingress-01"}, func(_ context.Context, intentID, nodeID string) (*AuthorizedRoutePublication, error) {
		calls++
		return &AuthorizedRoutePublication{IntentID: intentID, NodeID: nodeID, Route: route, Generation: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{serverIdentity}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	resolve, err := NewRemoteRoutePublicationAuthority(server.URL, controlURI, caPEM, clientPEM, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := resolve(context.Background(), "reserved-intent", "ingress-01")
	if err != nil || decision.IntentID != "reserved-intent" || decision.Route.SHA256 != route.SHA256 || calls != 1 {
		t.Fatalf("mutual TLS authority decision=%+v err=%v calls=%d", decision, err, calls)
	}
	routes := t.TempDir()
	const publisherClientURI = "spiffe://norn.test/control/ingress-publisher"
	publisher, err := NewNodePublisherHandler(routes, publisherClientURI, "ingress-01", resolve)
	if err != nil {
		t.Fatal(err)
	}
	request := verifiedReadbackRequest(t, "/v1/routes/publish", publisherClientURI)
	request.Method = http.MethodPost
	request.Header.Set("Content-Type", "application/json")
	request.Body = io.NopCloser(strings.NewReader(`{"intentId":"reserved-intent"}`))
	response := httptest.NewRecorder()
	publisher.ServeHTTP(response, request)
	if response.Code != http.StatusOK || calls != 2 {
		t.Fatalf("remote-authorized publication status=%d body=%q calls=%d", response.Code, response.Body.String(), calls)
	}
	revision, err := ReadPublishedRouteRevision(routes, route.RouterName)
	if err != nil || !revision.Present || revision.Generation != 1 || revision.RouteSHA256 != route.SHA256 {
		t.Fatalf("remote-authorized route=%+v err=%v", revision, err)
	}
	wrongServer, err := NewRemoteRoutePublicationAuthority(server.URL, "spiffe://norn.test/control/other", caPEM, clientPEM, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongServer(context.Background(), "reserved-intent", "ingress-01"); err == nil {
		t.Fatal("unmatched control server URI was accepted")
	}
	wrongNodeURI, _ := url.Parse("spiffe://norn.test/fleet/other")
	otherTemplate := clientTemplate
	otherTemplate.SerialNumber = big.NewInt(4)
	otherTemplate.URIs = []*url.URL{wrongNodeURI}
	otherPEM, otherKey, _, _ := nodeTestCertificate(t, ca, caKey, &otherTemplate)
	wrongNode, err := NewRemoteRoutePublicationAuthority(server.URL, controlURI, caPEM, otherPEM, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongNode(context.Background(), "reserved-intent", "ingress-01"); err == nil {
		t.Fatal("unlisted ingress client URI was accepted")
	}
	if _, err := NewRemoteRoutePublicationAuthority("http://127.0.0.1:1234", controlURI, caPEM, clientPEM, clientKey); err == nil {
		t.Fatal("plaintext control authority origin was accepted")
	}
}

func TestRemoteRouteAuthorityFollowsActiveControlWithoutTrustFallback(t *testing.T) {
	const nodeURI = "spiffe://norn.test/fleet/ingress-01"
	const controlURI = "spiffe://norn.test/control/route-authority"
	now := time.Now()
	base := x509.Certificate{SerialNumber: big.NewInt(101), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caPEM, _, ca, caKey := nodeTestCertificate(t, nil, nil, &base)
	serverURI, _ := url.Parse(controlURI)
	serverTemplate := x509.Certificate{SerialNumber: big.NewInt(102), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, URIs: []*url.URL{serverURI}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverPEM, serverKey, _, _ := nodeTestCertificate(t, ca, caKey, &serverTemplate)
	nodeURL, _ := url.Parse(nodeURI)
	clientTemplate := x509.Certificate{SerialNumber: big.NewInt(103), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), URIs: []*url.URL{nodeURL}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientPEM, clientKey, _, _ := nodeTestCertificate(t, ca, caKey, &clientTemplate)
	serverIdentity, err := tls.X509KeyPair(serverPEM, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	var calls [3]atomic.Int32
	servers := make([]*httptest.Server, 3)
	origins := make([]string, 3)
	for i := range servers {
		i := i
		handler, err := NewControlRouteAuthorityHandler(map[string]string{nodeURI: "ingress-01"}, func(_ context.Context, intentID, nodeID string) (*AuthorizedRoutePublication, error) {
			calls[i].Add(1)
			return &AuthorizedRoutePublication{IntentID: intentID, NodeID: nodeID, Generation: 2}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewUnstartedServer(handler)
		server.TLS = &tls.Config{Certificates: []tls.Certificate{serverIdentity}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}
		server.StartTLS()
		defer server.Close()
		servers[i], origins[i] = server, server.URL
	}
	resolve, err := NewRemoteRoutePublicationAuthorities(origins, controlURI, caPEM, clientPEM, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	servers[0].Close() // The first pinned control is inactive.
	for _, selected := range []int{2, 3} {
		if selected == 3 {
			servers[1].Close() // The active control moved to the third origin.
		}
		decision, err := resolve(context.Background(), "pinned-intent", "ingress-01")
		if err != nil || decision == nil || decision.IntentID != "pinned-intent" || decision.NodeID != "ingress-01" || decision.Generation != 2 {
			t.Fatalf("active control %d decision=%+v err=%v", selected, decision, err)
		}
	}
	if calls[0].Load() != 0 || calls[1].Load() != 1 || calls[2].Load() != 1 {
		t.Fatalf("unexpected control attempts: %d, %d, %d", calls[0].Load(), calls[1].Load(), calls[2].Load())
	}
	if _, err := NewRemoteRoutePublicationAuthorities(origins[:2], controlURI, caPEM, clientPEM, clientKey); err == nil {
		t.Fatal("two control origins were accepted")
	}
	if _, err := NewRemoteRoutePublicationAuthorities([]string{origins[0], origins[0], origins[2]}, controlURI, caPEM, clientPEM, clientKey); err == nil {
		t.Fatal("repeated control origin was accepted")
	}
	conflict := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) }))
	conflict.TLS = &tls.Config{Certificates: []tls.Certificate{serverIdentity}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}
	conflict.StartTLS()
	defer conflict.Close()
	strict, err := NewRemoteRoutePublicationAuthorities([]string{conflict.URL, origins[1], origins[2]}, controlURI, caPEM, clientPEM, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	before := calls[2].Load()
	if _, err := strict(context.Background(), "pinned-intent", "ingress-01"); err == nil || calls[2].Load() != before {
		t.Fatal("authority conflict fell through to another control")
	}
	denied := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	denied.TLS = &tls.Config{Certificates: []tls.Certificate{serverIdentity}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}
	denied.StartTLS()
	defer denied.Close()
	strict, err = NewRemoteRoutePublicationAuthorities([]string{denied.URL, origins[1], origins[2]}, controlURI, caPEM, clientPEM, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict(context.Background(), "pinned-intent", "ingress-01"); err == nil || calls[2].Load() != before {
		t.Fatal("unauthorized response fell through to another control")
	}
	malformed := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"intentId":"wrong","nodeId":"ingress-01","generation":2}`))
	}))
	malformed.TLS = &tls.Config{Certificates: []tls.Certificate{serverIdentity}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}
	malformed.StartTLS()
	defer malformed.Close()
	strict, err = NewRemoteRoutePublicationAuthorities([]string{malformed.URL, origins[1], origins[2]}, controlURI, caPEM, clientPEM, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict(context.Background(), "pinned-intent", "ingress-01"); err == nil || calls[2].Load() != before {
		t.Fatal("mismatched authority response fell through to another control")
	}
}
