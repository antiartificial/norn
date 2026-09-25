package database

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type mysqlTLSSecrets map[string][]byte

func (s mysqlTLSSecrets) Resolve(_ context.Context, reference string) ([]byte, error) {
	value, found := s[reference]
	if !found {
		return nil, errors.New("missing secret")
	}
	return append([]byte(nil), value...), nil
}

func TestMySQLVerifiedTLSChecksChainAndHostname(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	certificate := server.Certificate()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	secrets := mysqlTLSSecrets{"secret:mysql/ca": ca}
	address := server.Listener.Addr().String()
	connect := func(config *tls.Config) error {
		connection, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", address, config)
		if err == nil {
			_ = connection.Close()
		}
		return err
	}
	verifyCA, err := mysqlVerifiedTLS(context.Background(), DatabaseTLS{Mode: TLSVerifyCA, CARef: "secret:mysql/ca"}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := connect(verifyCA); err != nil {
		t.Fatalf("verify-ca rejected the pinned certificate: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(99), Subject: pkix.Name{CommonName: "unrelated test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}, &x509.Certificate{
		SerialNumber: big.NewInt(99), Subject: pkix.Name{CommonName: "unrelated test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	wrongCA := mysqlTLSSecrets{"secret:mysql/ca": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: unrelatedDER})}
	wrong, err := mysqlVerifiedTLS(context.Background(), DatabaseTLS{Mode: TLSVerifyCA, CARef: "secret:mysql/ca"}, wrongCA)
	if err != nil || connect(wrong) == nil {
		t.Fatalf("verify-ca accepted an unrelated CA: %v", err)
	}
	serverName := ""
	if len(certificate.IPAddresses) != 0 {
		serverName = certificate.IPAddresses[0].String()
	} else if len(certificate.DNSNames) != 0 {
		serverName = certificate.DNSNames[0]
	}
	if serverName == "" {
		t.Fatal("test certificate has no DNS or IP name")
	}
	full, err := mysqlVerifiedTLS(context.Background(), DatabaseTLS{Mode: TLSVerifyFull, ServerName: serverName, CARef: "secret:mysql/ca"}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := connect(full); err != nil {
		t.Fatalf("verify-full rejected its exact name: %v", err)
	}
	wrongName, err := mysqlVerifiedTLS(context.Background(), DatabaseTLS{Mode: TLSVerifyFull, ServerName: "wrong.example", CARef: "secret:mysql/ca"}, secrets)
	if err != nil || connect(wrongName) == nil {
		t.Fatalf("verify-full accepted a wrong name: %v", err)
	}
}

func TestMySQLTLSHealthMayResolveWhileRuntimeFailsClosed(t *testing.T) {
	catalog := testCatalog()
	catalog.Services[4].TLS.MinimumMode = TLSVerifyCA
	catalog.Services[4].Recovery.Capabilities = append(catalog.Services[4].Recovery.Capabilities, CapabilityHealth)
	catalog.Bindings[4].TLS = DatabaseTLS{Mode: TLSVerifyCA, CARef: "secret:mysql/ca"}
	resolver := mustResolver(t, catalog)
	request := ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "wordpress-db"}
	request.RequiredCapabilities = []Capability{CapabilityHealth}
	if _, err := resolver.Resolve(request); err != nil {
		t.Fatalf("verified MySQL health target was refused: %v", err)
	}
	request.RequiredCapabilities = []Capability{CapabilityRuntime}
	if _, err := resolver.Resolve(request); err == nil {
		t.Fatal("unqualified MySQL TLS runtime delivery was accepted")
	}
}

func TestMySQLRuntimeTLSMaterialCopiesAndClears(t *testing.T) {
	session := &Session{target: TargetIdentity{Engine: EngineMySQL}, bindingID: "wordpress", directory: t.TempDir(), runtimeTLS: map[string][]byte{"ca": []byte("ca"), "client_key": []byte("key")}}
	material, err := session.RuntimeTLSMaterial()
	if err != nil || string(material["ca"]) != "ca" || string(material["client_key"]) != "key" {
		t.Fatalf("runtime TLS material = %q, %v", material, err)
	}
	material["ca"][0] = 'X'
	if string(session.runtimeTLS["ca"]) != "ca" {
		t.Fatal("runtime TLS material aliases session-private bytes")
	}
	if err := session.Close(); err != nil || session.runtimeTLS != nil {
		t.Fatalf("close runtime TLS material: %v, %v", err, session.runtimeTLS)
	}
}
