package ingress

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/url"
	"testing"
	"time"
)

func TestPrivateRouteAuthorityListenerStopsWithAppLock(t *testing.T) {
	const nodeURI = "spiffe://norn.test/fleet/ingress-01"
	const controlURI = "spiffe://norn.test/control/route-authority"
	now := time.Now()
	base := x509.Certificate{SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "route listener test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caPEM, _, ca, caKey := nodeTestCertificate(t, nil, nil, &base)
	serverURI, _ := url.Parse(controlURI)
	serverTemplate := x509.Certificate{SerialNumber: big.NewInt(12), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, URIs: []*url.URL{serverURI}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverPEM, serverKey, _, _ := nodeTestCertificate(t, ca, caKey, &serverTemplate)
	clientURI, _ := url.Parse(nodeURI)
	clientTemplate := x509.Certificate{SerialNumber: big.NewInt(13), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), URIs: []*url.URL{clientURI}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientPEM, clientKey, _, _ := nodeTestCertificate(t, ca, caKey, &clientTemplate)
	route := fixtureRoute(t, "listener")
	handler, err := NewControlRouteAuthorityHandler(map[string]string{nodeURI: "ingress-01"}, func(_ context.Context, intentID, nodeID string) (*AuthorizedRoutePublication, error) {
		return &AuthorizedRoutePublication{IntentID: intentID, NodeID: nodeID, Route: route, Generation: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lockCtx, loseLock := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- ServePrivateControlRouteAuthority(context.Background(), lockCtx, listener, handler, serverPEM, serverKey, caPEM)
	}()
	resolve, err := NewRemoteRoutePublicationAuthority("https://"+listener.Addr().String(), controlURI, caPEM, clientPEM, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := resolve(context.Background(), "reserved-intent", "ingress-01")
	if err != nil || decision.Route.SHA256 != route.SHA256 {
		t.Fatalf("private route authority=%+v err=%v", decision, err)
	}
	loseLock()
	select {
	case err := <-served:
		if err == nil {
			t.Fatal("lost app lock did not stop authority with an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("route authority did not stop after app lock loss")
	}
	if _, err := resolve(context.Background(), "reserved-intent", "ingress-01"); err == nil {
		t.Fatal("stopped route authority still served a decision")
	}
	public, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer public.Close()
	if err := ServePrivateControlRouteAuthority(context.Background(), context.Background(), public, handler, serverPEM, serverKey, caPEM); err == nil {
		t.Fatal("public route authority listener was accepted")
	}
}
