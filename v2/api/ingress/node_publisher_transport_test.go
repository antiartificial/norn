package ingress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// The normal executor uses these exported TLS clients against separate
// publisher listeners. This fixture proves the wire and file boundary on two
// loopback nodes; it does not prove Traefik or public load balancer traffic.
func TestTwoNodePublisherTransportPartialRetry(t *testing.T) {
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	port := first.Addr().(*net.TCPAddr).Port
	second, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
	if err != nil {
		t.Skipf("second loopback address is unavailable: %v", err)
	}
	defer second.Close()

	now := time.Now()
	root := x509.Certificate{SerialNumber: big.NewInt(71), Subject: pkix.Name{CommonName: "publisher transport test CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caPEM, _, ca, caKey := nodeTestCertificate(t, nil, nil, &root)
	serverTemplate := x509.Certificate{SerialNumber: big.NewInt(72), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverPEM, serverKey, _, _ := nodeTestCertificate(t, ca, caKey, &serverTemplate)
	const publisherURI = "spiffe://norn.test/control/ingress-publisher"
	uri, _ := url.Parse(publisherURI)
	clientTemplate := x509.Certificate{SerialNumber: big.NewInt(73), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		URIs: []*url.URL{uri}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientPEM, clientKey, _, _ := nodeTestCertificate(t, ca, caKey, &clientTemplate)
	identity, err := tls.X509KeyPair(serverPEM, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("test CA is invalid")
	}
	route := fixtureRoute(t, "publisher-transport")
	const intentID = "reserved-transport-intent"
	var allowSecond atomic.Bool
	serve := func(listener net.Listener, nodeID, directory string) {
		handler, err := NewNodePublisherHandler(directory, publisherURI, nodeID,
			func(_ context.Context, requestedIntent, requestedNode string) (*AuthorizedRoutePublication, error) {
				if requestedIntent != intentID || requestedNode != nodeID || nodeID == "ingress-02" && !allowSecond.Load() {
					return nil, nil
				}
				return &AuthorizedRoutePublication{IntentID: intentID, NodeID: nodeID, Route: route, Generation: 1}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second}
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{identity}, ClientAuth: tls.RequireAndVerifyClientCert,
			ClientCAs: roots, MinVersion: tls.VersionTLS12}
		go func() { _ = server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
		t.Cleanup(func() { _ = server.Close() })
	}
	firstRoutes, secondRoutes := t.TempDir(), t.TempDir()
	serve(first, "ingress-01", firstRoutes)
	serve(second, "ingress-02", secondRoutes)
	nodes := []IngressNode{
		{ID: "ingress-01", APIURL: "https://127.0.0.1:18082"},
		{ID: "ingress-02", APIURL: "https://[::1]:18082"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wrongURI, _ := url.Parse("spiffe://norn.test/control/unrelated")
	wrongTemplate := clientTemplate
	wrongTemplate.SerialNumber = big.NewInt(74)
	wrongTemplate.URIs = []*url.URL{wrongURI}
	wrongPEM, wrongKey, _, _ := nodeTestCertificate(t, ca, caKey, &wrongTemplate)
	if err := ProbePublisherNodesWithTLS(ctx, caPEM, wrongPEM, wrongKey, nodes, port); err == nil {
		t.Fatal("another verified client identity passed publisher preflight")
	}
	if err := ProbePublisherNodesWithTLS(ctx, caPEM, clientPEM, clientKey, nodes, port); err != nil {
		t.Fatalf("two-node private publisher preflight: %v", err)
	}
	receipts, err := PublishRouteIntentToNodesWithTLS(ctx, caPEM, clientPEM, clientKey, nodes, port, intentID, 1, route.SHA256)
	if err == nil || len(receipts) != 1 || receipts[0].NodeID != "ingress-01" {
		t.Fatalf("partial publication receipts=%+v err=%v", receipts, err)
	}
	if revision, err := ReadPublishedRouteRevision(secondRoutes, route.RouterName); err != nil || revision.Present {
		t.Fatalf("refusing node published route=%+v err=%v", revision, err)
	}
	allowSecond.Store(true)
	receipts, err = PublishRouteIntentToNodesWithTLS(ctx, caPEM, clientPEM, clientKey, nodes, port, intentID, 1, route.SHA256)
	if err != nil || len(receipts) != 2 {
		t.Fatalf("publication retry receipts=%+v err=%v", receipts, err)
	}
	for _, directory := range []string{firstRoutes, secondRoutes} {
		revision, err := ReadPublishedRouteRevision(directory, route.RouterName)
		if err != nil || !revision.Present || revision.Generation != 1 || revision.RouteSHA256 != route.SHA256 {
			t.Fatalf("published route in %s=%+v err=%v", directory, revision, err)
		}
	}
}
