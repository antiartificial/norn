package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"testing"
	"time"
)

func testPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestReadinessFailureDoesNotStopLiveness(t *testing.T) {
	t.Setenv("PILOT_FAIL_READINESS", "true")
	s := &service{}
	for path, want := range map[string]int{"/health/live": 200, "/health/ready": 503, "/version": 200, "/metrics": 200} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatalf("%s: got %d want %d", path, w.Code, want)
		}
	}
}

func TestRecordRejectsInvalidIDBeforeDatabase(t *testing.T) {
	s := &service{}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest("PUT", "/records/bad%20id", nil))
	if w.Code != 400 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestDatabaseRequiresExplicitNetworkAndDatabase(t *testing.T) {
	for _, dsn := range []string{"", "user:password@unix(/tmp/mysql.sock)/pilot", "user:password@tcp(db.example:25060)/"} {
		t.Setenv("MYSQL_DSN", dsn)
		if db, err := openDatabase(); err == nil {
			db.Close()
			t.Fatal("accepted invalid database configuration")
		}
	}
}

func TestInlineMySQLCAIsAppendedAndMalformedCAIsRejected(t *testing.T) {
	roots := x509.NewCertPool()
	if err := appendMySQLCAs(roots, "", testPEM(t)); err != nil {
		t.Fatalf("valid inline CA: %v", err)
	}
	if err := appendMySQLCAs(x509.NewCertPool(), "", "not a certificate"); err == nil {
		t.Fatal("accepted malformed inline CA")
	}
}
