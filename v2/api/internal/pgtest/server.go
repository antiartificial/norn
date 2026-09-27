// Package pgtest starts disposable PostgreSQL servers for tests. A server
// is owned by one test: a fresh data directory under /tmp, Unix socket only
// (no TCP listener), trust authentication for the current OS user, and it is
// stopped and deleted at cleanup. It never touches any other server.
package pgtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type Server struct {
	SocketDir string
	Port      int
	User      string
	dataDir   string
	// CAPEM is set by StartTLS: the CA that signed the server certificate
	// (DNS name localhost, IP 127.0.0.1).
	CAPEM []byte
}

// Start skips the test when initdb or pg_ctl are not installed.
func Start(t testing.TB) *Server {
	t.Helper()
	return start(t, false)
}

// StartTLS additionally listens on 127.0.0.1 only, with TLS required for
// every TCP connection (pg_hba rejects non-TLS TCP). The Unix socket stays
// available to the bootstrap superuser for fixture setup.
func StartTLS(t testing.TB) *Server {
	t.Helper()
	return start(t, true)
}

func start(t testing.TB, withTLS bool) *Server {
	t.Helper()
	initdb, err := exec.LookPath("initdb")
	if err != nil {
		t.Skip("initdb is not installed; a scoped second PostgreSQL server is unavailable")
	}
	pgCtl, err := exec.LookPath("pg_ctl")
	if err != nil {
		t.Skip("pg_ctl is not installed; a scoped second PostgreSQL server is unavailable")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	// A short root keeps the Unix socket path under the platform limit.
	root, err := os.MkdirTemp("/tmp", "norn-pg-")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{SocketDir: root, User: current.Username, dataDir: filepath.Join(root, "data")}
	t.Cleanup(func() {
		_ = command(pgCtl, "-D", server.dataDir, "-m", "immediate", "-w", "stop").Run()
		_ = os.RemoveAll(root)
	})
	if output, err := command(initdb, "-D", server.dataDir, "-U", server.User, "-A", "trust", "-E", "UTF8").CombinedOutput(); err != nil {
		t.Fatalf("initdb: %v\n%s", err, output)
	}
	listen := "''"
	if withTLS {
		listen = "'127.0.0.1'"
		server.CAPEM = writeServerCertificate(t, server.dataDir)
		hba := filepath.Join(server.dataDir, "pg_hba.conf")
		existing, err := os.ReadFile(hba)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hba, append([]byte("hostnossl all all 127.0.0.1/32 reject\n"), existing...), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var lastOutput []byte
	for attempt := 0; attempt < 5; attempt++ {
		server.Port = 56000 + rand.IntN(3000)
		options := fmt.Sprintf("-c listen_addresses=%s -k %s -p %d", listen, root, server.Port)
		if withTLS {
			options += " -c ssl=on -c ssl_cert_file=server.crt -c ssl_key_file=server.key"
		}
		output, err := command(pgCtl, "-D", server.dataDir, "-o", options, "-l", filepath.Join(root, "server.log"), "-w", "-t", "30", "start").CombinedOutput()
		if err == nil {
			return server
		}
		lastOutput = output
	}
	log, _ := os.ReadFile(filepath.Join(root, "server.log"))
	t.Fatalf("pg_ctl start failed:\n%s\n%s", lastOutput, log)
	return nil
}

// URL is a password-free libpq URL for a database on this server.
func (s *Server) URL(database string) string {
	query := url.Values{"host": {s.SocketDir}, "port": {strconv.Itoa(s.Port)}}
	return "postgresql://" + url.PathEscape(s.User) + "@/" + url.PathEscape(database) + "?" + query.Encode()
}

// CreateDatabase creates a database with the given name on this server.
func (s *Server) CreateDatabase(t testing.TB, name string) {
	t.Helper()
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Skip("psql is not installed")
	}
	identifier := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	if output, err := command(psql, "-X", "-v", "ON_ERROR_STOP=1", "-h", s.SocketDir, "-p", strconv.Itoa(s.Port), "-U", s.User, "-d", "postgres", "-c", "CREATE DATABASE "+identifier).CombinedOutput(); err != nil {
		t.Fatalf("create database: %v\n%s", err, output)
	}
}

// Exec runs SQL in-process on a database of this server as the bootstrap
// superuser (trust). Call it before a test installs a hostile environment.
func (s *Server) Exec(t testing.TB, database, sql string) {
	t.Helper()
	connection, err := pgx.Connect(context.Background(), s.URL(database))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	if _, err := connection.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

// CreatePasswordRole creates a login role that must authenticate with
// SCRAM-SHA-256 over the socket, owning the given database. The password is
// sent in-process, never on a command line.
func (s *Server) CreatePasswordRole(t testing.TB, role, password, ownedDatabase string) {
	t.Helper()
	literal := "'" + strings.ReplaceAll(password, "'", "''") + "'"
	s.Exec(t, "postgres", "SET password_encryption = 'scram-sha-256'; CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD "+literal)
	s.Exec(t, "postgres", "ALTER DATABASE "+pgx.Identifier{ownedDatabase}.Sanitize()+" OWNER TO "+pgx.Identifier{role}.Sanitize())
	hba := filepath.Join(s.dataDir, "pg_hba.conf")
	existing, err := os.ReadFile(hba)
	if err != nil {
		t.Fatal(err)
	}
	rule := "local all " + role + " scram-sha-256\nhostssl all " + role + " 127.0.0.1/32 scram-sha-256\n"
	if err := os.WriteFile(hba, append([]byte(rule), existing...), 0o600); err != nil {
		t.Fatal(err)
	}
	pgCtl, err := exec.LookPath("pg_ctl")
	if err != nil {
		t.Fatal(err)
	}
	if output, err := command(pgCtl, "-D", s.dataDir, "reload").CombinedOutput(); err != nil {
		t.Fatalf("pg_ctl reload: %v\n%s", err, output)
	}
	// Reload is asynchronous; wait until the rule is in effect.
	for attempt := 0; attempt < 50; attempt++ {
		var active bool
		connection, err := pgx.Connect(context.Background(), s.URL("postgres"))
		if err == nil {
			err = connection.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_hba_file_rules WHERE $1 = ANY(user_name) AND auth_method = 'scram-sha-256')`, role).Scan(&active)
			connection.Close(context.Background())
		}
		if err == nil && active {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("pg_hba reload did not take effect")
}

// writeServerCertificate creates a throwaway CA and a server certificate for
// localhost/127.0.0.1 in the data directory, returning the CA PEM.
func writeServerCertificate(t testing.TB, dataDir string) []byte {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "norn-pgtest-ca"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(cryptorand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverDER, err := x509.CreateCertificate(cryptorand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "server.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "server.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

// OtherCAPEM is a CA that signed nothing on any server, for negative tests.
func OtherCAPEM(t testing.TB) []byte {
	t.Helper()
	return writeServerCertificate(t, t.TempDir())
}

// NodeClientScript is the dependency-free Node PostgreSQL client used to
// prove delivered connection values work for an ordinary Node process. It
// skips the test when node is not installed.
func NodeClientScript(t testing.TB) (node, script string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	_, source, _, _ := runtime.Caller(0)
	return node, filepath.Join(filepath.Dir(source), "nodeclient", "client.mjs")
}

// command runs a PostgreSQL tool with a closed environment so no ambient
// PG* routing can redirect it to another server.
func command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C", "LC_ALL=C"}
	return cmd
}
