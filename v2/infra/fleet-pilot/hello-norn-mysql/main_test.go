package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeConn struct {
	remote net.Addr
	closed bool
}

func (c *fakeConn) Read([]byte) (int, error)         { return 0, nil }
func (c *fakeConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *fakeConn) Close() error                     { c.closed = true; return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr             { return c.remote }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

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
	for path, want := range map[string]int{"/healthz": 200, "/readyz": 503, "/version": 200, "/metrics": 200} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatalf("%s: got %d want %d", path, w.Code, want)
		}
	}
}

func TestRecordRejectsInvalidIDBeforeDatabase(t *testing.T) {
	s := &service{writeToken: []byte("0123456789abcdef0123456789abcdef")}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/records/bad%20id", nil)
	req.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
	s.routes().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestRecordWriteRequiresBearerBeforeDatabaseAccess(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	s := &service{writeToken: []byte(token)} // A nil DB would panic if an unauthorized request reached it.
	for name, authorization := range map[string]string{
		"missing": "",
		"wrong":   "Bearer 0123456789abcdef0123456789abcdeg",
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("PUT", "/records/valid-record", nil)
			if authorization != "" {
				req.Header.Set("Authorization", authorization)
			}
			s.routes().ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("status=%d cache=%q challenge=%q", w.Code, w.Header().Get("Cache-Control"), w.Header().Get("WWW-Authenticate"))
			}
		})
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/records/bad%20id", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	s.routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("valid bearer was not admitted to request validation: %d", w.Code)
	}
	read := httptest.NewRecorder()
	s.routes().ServeHTTP(read, httptest.NewRequest("GET", "/version", nil))
	if read.Code != http.StatusOK {
		t.Fatalf("public version status=%d", read.Code)
	}
}

func TestPilotWriteTokenRequiresStrongHeaderSafeValue(t *testing.T) {
	for _, value := range []string{"", "short", "0123456789abcdef0123456789abcde\n", "0123456789abcdef0123456789abcde\x7f"} {
		if _, err := pilotWriteToken(value); err == nil {
			t.Fatalf("accepted invalid write token %q", value)
		}
	}
	if _, err := pilotWriteToken("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("rejected valid write token: %v", err)
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

func TestMySQLDialerPinsReviewedPrivatePeer(t *testing.T) {
	var destination string
	conn := &fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("10.20.30.40"), Port: 25060}}
	dialer, err := mysqlPinnedDialer("database.example:25060", "10.20.30.40", func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp4" {
			t.Fatalf("network = %q", network)
		}
		destination = address
		return conn, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := dialer(context.Background(), "database.example:25060")
	if err != nil || got != conn || destination != "10.20.30.40:25060" {
		t.Fatalf("got conn=%v destination=%q err=%v", got, destination, err)
	}
	if _, err := dialer(context.Background(), "changed.example:25060"); err == nil {
		t.Fatal("accepted changed DSN address")
	}
}

func TestMySQLDialerRejectsUnreviewedPeerAndInvalidPin(t *testing.T) {
	for _, pin := range []string{"", "database.example", "127.0.0.1", "8.8.8.8", "10.020.30.40"} {
		if _, err := mysqlPinnedDialer("database.example:25060", pin, nil); err == nil {
			t.Fatalf("accepted pin %q", pin)
		}
	}
	conn := &fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("10.20.30.41"), Port: 25060}}
	dialer, err := mysqlPinnedDialer("database.example:25060", "10.20.30.40", func(context.Context, string, string) (net.Conn, error) { return conn, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dialer(context.Background(), "database.example:25060"); err == nil || !conn.closed {
		t.Fatal("accepted unreviewed peer or failed to close it")
	}
}

func TestMySQLDialerRequiresDNSNameAndCallableDialer(t *testing.T) {
	for _, address := range []string{
		"10.20.30.40:25060", "10.20.30.040:25060", "database..example:25060", "database_example:25060",
	} {
		if _, err := mysqlPinnedDialer(address, "10.20.30.40", func(context.Context, string, string) (net.Conn, error) { return nil, nil }); err == nil {
			t.Fatalf("accepted non-DNS address %q", address)
		}
	}
	if _, err := mysqlPinnedDialer("database.example:25060", "10.20.30.40", nil); err == nil {
		t.Fatal("accepted nil dialer")
	}
}

func TestMySQLDialerRejectsNonTCPPeerWithoutPanic(t *testing.T) {
	conn := &fakeConn{remote: fakeAddr("not-tcp")}
	dialer, err := mysqlPinnedDialer("database.example:25060", "10.20.30.40", func(context.Context, string, string) (net.Conn, error) { return conn, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dialer(context.Background(), "database.example:25060"); err == nil || !conn.closed {
		t.Fatal("accepted non-TCP peer or failed to close it")
	}
}

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }

func TestMySQLTLSUsesOnlyRequiredProviderCAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider-ca.pem")
	if err := os.WriteFile(path, []byte(testPEM(t)), 0o400); err != nil {
		t.Fatal(err)
	}
	config, err := mysqlTLSConfig("database.example:25060", path)
	if err != nil {
		t.Fatalf("valid provider CA: %v", err)
	}
	if config.RootCAs == nil || config.ServerName != "database.example" || config.InsecureSkipVerify {
		t.Fatalf("unexpected TLS config: %+v", config)
	}
	for _, input := range []struct{ address, path string }{{"database.example:25060", ""}, {"database.example:25060", filepath.Join(t.TempDir(), "missing")}, {"bad-address", path}, {"10.20.30.40:25060", path}} {
		if _, err := mysqlTLSConfig(input.address, input.path); err == nil {
			t.Fatalf("accepted invalid TLS input: %+v", input)
		}
	}
}
