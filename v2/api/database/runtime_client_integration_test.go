package database

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/internal/pgtest"
)

// The runtime connection value is a real credential-bearing URL that an
// ordinary Node process (and pgx) can use against a server that enforces
// SCRAM-SHA-256; the file form works identically; a wrong password fails.
func TestRuntimeConnectionValueWorksForOrdinaryClients(t *testing.T) {
	node, client := pgtest.NodeClientScript(t)
	server := pgtest.Start(t)
	server.CreateDatabase(t, "shop")
	const password = `S3cr@t:/?#[]%&='" é` + "NORN_RUNTIME_CANARY"
	server.CreatePasswordRole(t, "shop_app", password, "shop")
	server.Exec(t, "shop", `CREATE TABLE marker (value text); INSERT INTO marker VALUES ('runtime-ok'); GRANT SELECT ON marker TO shop_app`)

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSecret(t, dir, "shop", password)
	secrets, err := NewDirectorySecretSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer secrets.Close()
	resolved := ResolvedBinding{
		Target:  TargetIdentity{ServiceID: "scoped-pg", ServiceGeneration: 1, BindingID: "shop-primary", BindingGeneration: 1, Engine: EnginePostgreSQL, Database: "shop", Role: "shop_app"},
		Purpose: PurposeApplication, CredentialRef: "secret:shop", TLS: DatabaseTLS{Mode: TLSDisabled},
		Endpoint: DatabaseEndpoint{Host: server.SocketDir, Port: server.Port},
	}
	session, err := OpenSession(context.Background(), resolved, secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Probe(context.Background()); err != nil {
		t.Fatalf("probe with scram credential: %v", err)
	}
	value, err := session.RuntimeConnectionURL()
	if err != nil {
		t.Fatal(err)
	}

	run := func(mode string, environment ...string) (string, error) {
		command := exec.Command(node, client, mode, "SELECT value, current_user, current_database() FROM marker")
		command.Env = append([]string{"PATH=" + os.Getenv("PATH")}, environment...)
		output, err := command.CombinedOutput()
		return strings.TrimSpace(string(output)), err
	}
	want := "runtime-ok\tshop_app\tshop"
	if output, err := run("value", "DATABASE_URL="+value); err != nil || output != want {
		t.Fatalf("node with DATABASE_URL = %q, %v", session.Redact([]byte(output)), err)
	}
	file := filepath.Join(t.TempDir(), "primary.url")
	if err := os.WriteFile(file, []byte(value), 0o400); err != nil {
		t.Fatal(err)
	}
	if output, err := run("file", "DATABASE_URL_FILE="+file); err != nil || output != want {
		t.Fatalf("node with DATABASE_URL_FILE = %q, %v", session.Redact([]byte(output)), err)
	}
	// The value is a URL, not a path, and the path variable is not a URL:
	// using one as the other fails rather than silently connecting.
	if _, err := run("value", "DATABASE_URL="+file); err == nil {
		t.Fatal("a file path was accepted as a connection URL")
	}
	// pgx (Go) accepts the same value.
	connection, err := pgx.Connect(context.Background(), value)
	if err != nil {
		t.Fatalf("pgx with runtime value: %v", err)
	}
	var role string
	err = connection.QueryRow(context.Background(), `SELECT current_user`).Scan(&role)
	connection.Close(context.Background())
	if err != nil || role != "shop_app" {
		t.Fatalf("pgx role = %q, %v", role, err)
	}
	// Authentication is really enforced: a wrong password fails with 28P01.
	wrong := strings.Replace(value, percentEncode(password), "wrong", 1)
	if output, err := run("value", "DATABASE_URL="+wrong); err == nil || !strings.Contains(output, "28P01") {
		t.Fatalf("wrong password = %q, %v", output, err)
	}
	// Redaction covers the raw and URL-encoded forms of the secret.
	if redacted := session.Redact([]byte("failed: " + value + " / " + password)); strings.Contains(redacted, "NORN_RUNTIME_CANARY") {
		t.Fatalf("redaction leaked: %s", redacted)
	}
}
