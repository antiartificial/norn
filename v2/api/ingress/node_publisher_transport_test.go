package ingress

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The normal executor uses these exported TLS clients against separate
// publisher listeners. This fixture proves the wire and file boundary on two
// loopback nodes; it does not prove Traefik or public load balancer traffic.
func TestTwoNodePublisherTransportPartialRetry(t *testing.T) {
	runTwoNodePublisherTransport(t, "127.0.0.1", "::1", false)
}

// Run this opt-in test inside an isolated Linux container attached to two
// private bridge networks. It uses the same inventory parser as the executor.
func TestPrivateFleetInventoryPublisherTransport(t *testing.T) {
	addresses := strings.Split(os.Getenv("NORN_TEST_INGRESS_PRIVATE_IPS"), ",")
	if len(addresses) != 2 {
		t.Skip("set NORN_TEST_INGRESS_PRIVATE_IPS to two container-owned private IPv4 addresses")
	}
	for _, address := range addresses {
		ip := net.ParseIP(address)
		if ip == nil || ip.To4() == nil || !ip.IsPrivate() || ip.IsLoopback() {
			t.Fatal("test ingress address must be a private IPv4 address")
		}
	}
	if addresses[0] == addresses[1] {
		t.Fatal("test ingress addresses must be distinct")
	}
	runTwoNodePublisherTransport(t, addresses[0], addresses[1], true)
}

func runTwoNodePublisherTransport(t *testing.T, firstIP, secondIP string, parseInventory bool) {
	t.Helper()
	first, err := net.Listen("tcp", net.JoinHostPort(firstIP, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	port := first.Addr().(*net.TCPAddr).Port
	second, err := net.Listen("tcp", net.JoinHostPort(secondIP, strconv.Itoa(port)))
	if err != nil {
		if !parseInventory {
			t.Skipf("second loopback address is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	defer second.Close()

	now := time.Now()
	root := x509.Certificate{SerialNumber: big.NewInt(71), Subject: pkix.Name{CommonName: "publisher transport test CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caPEM, _, ca, caKey := nodeTestCertificate(t, nil, nil, &root)
	serverTemplate := x509.Certificate{SerialNumber: big.NewInt(72), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP(firstIP), net.ParseIP(secondIP)}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
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
		{ID: "ingress-01", APIURL: "https://" + net.JoinHostPort(firstIP, "18082")},
		{ID: "ingress-02", APIURL: "https://" + net.JoinHostPort(secondIP, "18082")},
	}
	if parseInventory {
		body := []byte(fmt.Sprintf(`{"cluster":"norn-test","environment":"staging/private","ingressNodes":[{"name":"ingress-01","privateIP":"%s"},{"name":"ingress-02","privateIP":"%s"}],"nodesFileSHA256":"%s","schemaVersion":"norn.fleet-ingress-inventory/v1"}`+"\n", firstIP, secondIP, strings.Repeat("a", 64)))
		digest := sha256.Sum256(body)
		parsed, err := ParseFleetIngressInventory(body, "sha256:"+hex.EncodeToString(digest[:]), "norn-test", "staging/private", 18082)
		if err != nil || len(parsed) != 2 {
			t.Fatalf("completed private inventory nodes=%+v err=%v", parsed, err)
		}
		nodes = parsed
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
