package ingress

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func nodeTestCertificate(t *testing.T, parent *x509.Certificate, signer *ecdsa.PrivateKey, template *x509.Certificate) ([]byte, []byte, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if parent == nil {
		parent, signer = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), cert, key
}

func TestMutualTLSNodeClientObservesPrivateReadback(t *testing.T) {
	now := time.Now()
	base := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ingress CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caPEM, _, ca, caKey := nodeTestCertificate(t, nil, nil, &base)
	serverTemplate := x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverPEM, serverKey, _, _ := nodeTestCertificate(t, ca, caKey, &serverTemplate)
	uri, _ := url.Parse("spiffe://norn.test/control/ingress")
	clientTemplate := x509.Certificate{SerialNumber: big.NewInt(3), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), URIs: []*url.URL{uri}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientPEM, clientKey, _, _ := nodeTestCertificate(t, ca, caKey, &clientTemplate)
	serverIdentity, err := tls.X509KeyPair(serverPEM, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	desired := fixtureRoute(t, "release-a")
	rawDataJSON, err := json.Marshal(observedRawData(t, desired))
	if err != nil {
		t.Fatal(err)
	}
	var generation = uint64(7)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || !hasVerifiedClientURI(r.TLS.VerifiedChains, uri.String()) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/routes/" + desired.RouterName + "/revision":
			_, _ = w.Write([]byte(`{"generation":7,"routeSHA256":"` + desired.SHA256 + `","present":true}`))
		case "/api/rawdata":
			_, _ = w.Write(rawDataJSON)
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{serverIdentity}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	nodes := []IngressNode{{ID: "ingress-a", APIURL: server.URL}}
	observations, err := ObservePublishedRenderedRouteWithTLS(context.Background(), caPEM, clientPEM, clientKey, nodes, desired, generation)
	if err != nil || len(observations) != 1 || observations[0].PublishedGeneration != generation {
		t.Fatalf("mutual TLS observation = %+v, %v", observations, err)
	}
	if _, err := ObservePublishedRenderedRouteWithTLS(context.Background(), caPEM, clientPEM, clientKey, []IngressNode{{ID: "ingress-a", APIURL: "http://127.0.0.1:18082"}}, desired, generation); err == nil {
		t.Fatal("plaintext node accepted")
	}
	otherCA, _, _, _ := nodeTestCertificate(t, nil, nil, &base)
	if _, err := ObservePublishedRenderedRouteWithTLS(context.Background(), otherCA, clientPEM, clientKey, nodes, desired, generation); err == nil {
		t.Fatal("untrusted server accepted")
	}
	if _, err := NewMutualTLSNodeClient(caPEM, nil, nil); err == nil {
		t.Fatal("missing client identity accepted")
	}
}
