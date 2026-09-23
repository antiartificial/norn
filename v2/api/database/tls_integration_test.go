package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/internal/pgtest"
)

// verify-full against a scoped TLS server: the session's libpq tools and
// in-process pgx both negotiate TLS and verify the server against the
// catalog's CA and server name; a wrong CA, a name mismatch and plaintext
// all fail. Runtime delivery of TLS targets stays refused (not implemented).
func TestTLSVerifyFullAgainstScopedServer(t *testing.T) {
	server := pgtest.StartTLS(t)
	server.CreateDatabase(t, "shop")
	const password = "tls-scram-" + "NORN_TLS_CANARY"
	server.CreatePasswordRole(t, "shop_app", password, "shop")

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSecret(t, dir, "shop", password)
	for name, data := range map[string][]byte{"tls/ca": server.CAPEM, "tls/other-ca": pgtest.OtherCAPEM(t)} {
		if err := os.MkdirAll(filepath.Join(dir, "tls"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	secrets, err := NewDirectorySecretSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer secrets.Close()
	binding := func(host string, tls DatabaseTLS) ResolvedBinding {
		return ResolvedBinding{
			Target:  TargetIdentity{ServiceID: "tls-pg", ServiceGeneration: 1, BindingID: "shop-tls", BindingGeneration: 1, Engine: EnginePostgreSQL, Database: "shop", Role: "shop_app"},
			Purpose: PurposeApplication, CredentialRef: "secret:shop", TLS: tls, Endpoint: DatabaseEndpoint{Host: host, Port: server.Port},
		}
	}
	verifyFull := DatabaseTLS{Mode: TLSVerifyFull, ServerName: "localhost", CARef: "secret:tls/ca"}

	session, err := OpenSession(context.Background(), binding("localhost", verifyFull), secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Probe(context.Background()); err != nil {
		t.Fatalf("pgx verify-full probe: %v", err)
	}
	output, err := session.Command(context.Background(), "psql", "-X", "-At", "-d", session.ServiceArgument(), "-c", "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "t" {
		t.Fatalf("psql over verify-full = %q, %v", session.Redact(output), err)
	}
	if _, err := session.RuntimeConnectionURL(); err == nil {
		t.Fatal("TLS runtime delivery was offered although CA placement in allocations is not implemented")
	}

	// A CA that did not sign the server certificate fails verification.
	wrongCA := verifyFull
	wrongCA.CARef = "secret:tls/other-ca"
	wrong, err := OpenSession(context.Background(), binding("localhost", wrongCA), secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if _, err := wrong.Probe(context.Background()); err == nil {
		t.Fatal("verify-full accepted a server certificate from another CA")
	}
	if output, err := wrong.Command(context.Background(), "psql", "-X", "-At", "-d", wrong.ServiceArgument(), "-c", "SELECT 1").CombinedOutput(); err == nil {
		t.Fatalf("psql accepted a server certificate from another CA: %q", output)
	}
	// verify-full requires the connection host to be the declared name.
	if _, err := OpenSession(context.Background(), binding("127.0.0.1", verifyFull), secrets); err == nil {
		t.Fatal("verify-full with a host other than the server name was opened")
	}
	// Plaintext to the TLS-only listener is rejected by the server.
	plain, err := OpenSession(context.Background(), binding("127.0.0.1", DatabaseTLS{Mode: TLSDisabled}), secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if _, err := plain.Probe(context.Background()); err == nil {
		t.Fatal("plaintext connection accepted by a TLS-only listener")
	}
}
