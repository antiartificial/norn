package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// TestMySQLExactTargetDumpRestore is an opt-in, disposable rehearsal of the
// MySQL client tools and resolver identity. It is deliberately not a recovery
// adapter: no production snapshot/restore capability is enabled by this test.
func TestMySQLExactTargetDumpRestore(t *testing.T) {
	adminDSN := os.Getenv("NORN_TEST_MYSQL_DSN")
	if adminDSN == "" {
		t.Skip("NORN_TEST_MYSQL_DSN is not set")
	}
	for _, tool := range []string{"mysqldump", "mysql"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	adminConfig, err := mysql.ParseDSN(adminDSN)
	if err != nil || adminConfig.Net != "tcp" {
		t.Fatal("NORN_TEST_MYSQL_DSN must be a TCP MySQL DSN")
	}
	admin := sql.OpenDB(mustMySQLConnector(t, adminConfig))
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal("disposable MySQL server is unavailable")
	}
	host, portText, err := net.SplitHostPort(adminConfig.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		t.Fatal("invalid disposable MySQL port")
	}
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	sourceDB, targetDB := "norn_src_"+suffix, "norn_dst_"+suffix
	sourceRole, targetRole := "src_"+suffix, "dst_"+suffix
	sourcePassword, targetPassword := "source"+suffix, "target"+suffix
	// Cleanup only the unique fixture names, including when preparation fails.
	defer func() {
		for _, name := range []string{sourceDB, targetDB} {
			_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+name+"`")
		}
		for _, name := range []string{sourceRole, targetRole} {
			_, _ = admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+name+"'@'%'")
		}
	}()
	for _, item := range []struct{ db, role, password string }{{sourceDB, sourceRole, sourcePassword}, {targetDB, targetRole, targetPassword}} {
		for _, statement := range []string{
			"CREATE DATABASE `" + item.db + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
			"CREATE USER '" + item.role + "'@'%' IDENTIFIED BY '" + item.password + "'",
			"GRANT ALL PRIVILEGES ON `" + item.db + "`.* TO '" + item.role + "'@'%'",
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatal("prepare disposable MySQL recovery fixture failed")
			}
		}
	}
	if os.Getenv("NORN_TEST_MYSQL_CA_FILE") != "" {
		if _, err := admin.ExecContext(ctx, "ALTER USER '"+sourceRole+"'@'%' REQUIRE SSL"); err != nil {
			t.Fatal("require TLS for the disposable snapshot source failed")
		}
	}
	if _, err := admin.ExecContext(ctx, "CREATE TABLE `"+sourceDB+"`.marker (value VARCHAR(80) NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "INSERT INTO `"+sourceDB+"`.marker VALUES ('source-only-recovery-marker')"); err != nil {
		t.Fatal(err)
	}

	catalog := testCatalog()
	catalog.Services[4].Endpoint = DatabaseEndpoint{Host: host, Port: port}
	catalog.Services[4].EngineVersion = "8.4"
	catalog.Bindings[4].Database, catalog.Bindings[4].Role = sourceDB, sourceRole
	catalog.Bindings[4].CredentialRef = "secret:recovery/source"
	caPath := os.Getenv("NORN_TEST_MYSQL_CA_FILE")
	var caPEM []byte
	if caPath != "" {
		caPEM, err = os.ReadFile(caPath)
		if err != nil {
			t.Fatal(err)
		}
		catalog.Bindings[4].TLS = DatabaseTLS{Mode: TLSVerifyCA, CARef: "secret:recovery/ca"}
	}
	catalog.Bindings = append(catalog.Bindings, DatabaseBinding{APIVersion: APIVersion, ID: "recovery-target", ServiceID: "wp-mysql", Database: targetDB, Role: targetRole, Generation: 2, CredentialRef: "secret:recovery/target", TLS: DatabaseTLS{Mode: TLSDisabled}})
	catalog.Profiles[0].DatabaseBindings["restore-db"] = "recovery-target"
	resolver := mustResolver(t, catalog)
	secrets := literalSecrets{
		"secret:recovery/source": fmt.Sprintf(`{"password":%q}`, sourcePassword),
		"secret:recovery/target": fmt.Sprintf(`{"password":%q}`, targetPassword),
	}
	if caPath != "" {
		secrets["secret:recovery/ca"] = string(caPEM)
	}
	resolve := func(logical string, expected *TargetIdentity) ResolvedBinding {
		t.Helper()
		result, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: logical, Expected: expected})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	source, target := resolve("wordpress-db", nil), resolve("restore-db", nil)
	if source.Target == target.Target {
		t.Fatal("source and target identities are equal")
	}
	stale := target.Target
	stale.BindingGeneration--
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "restore-db", Expected: &stale})
	var typed *ResolverError
	if !errors.As(err, &typed) || typed.Code != CodeStaleTarget {
		t.Fatalf("stale restore identity was accepted: %v", err)
	}
	for _, capability := range []Capability{CapabilitySnapshot, CapabilityRestore} {
		_, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "restore-db", Expected: &target.Target, RequiredCapabilities: []Capability{capability}})
		var typed *ResolverError
		if !errors.As(err, &typed) || typed.Code != CodeUnsupportedCapability {
			t.Fatalf("unimplemented MySQL %s capability became active: %v", capability, err)
		}
	}
	// Re-resolve with exact identities immediately before the scoped tool
	// actions. Production restore still needs a durable lifecycle fence.
	source = resolve("wordpress-db", &source.Target)
	target = resolve("restore-db", &target.Target)
	sourceSession, err := OpenSession(ctx, source, secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceSession.Close()
	targetSession, err := OpenSession(ctx, target, secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer targetSession.Close()
	for _, session := range []*Session{sourceSession, targetSession} {
		if _, err := session.Probe(ctx); err != nil {
			t.Fatal(err)
		}
	}
	options := func(role, password string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "mysql.cnf")
		// Test credentials are alphanumeric; they never appear in argv or logs.
		if err := os.WriteFile(path, []byte("[client]\nuser="+role+"\npassword="+password+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	targetOptions := options(targetRole, targetPassword)
	dumpTool, err := exec.LookPath("mysqldump")
	if err != nil {
		t.Fatal(err)
	}
	toolBytes, err := os.ReadFile(dumpTool)
	if err != nil {
		t.Fatal(err)
	}
	toolChecksum := sha256.Sum256(toolBytes)
	stageDirectory := t.TempDir()
	if err := os.Chmod(stageDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := StageMySQLSQLSnapshot(ctx, source, stale, secrets, dumpTool, fmt.Sprintf("%x", toolChecksum), stageDirectory); err == nil {
		t.Fatal("snapshot staging accepted a different expected target")
	}
	if _, _, err := StageMySQLSQLSnapshot(ctx, source, source.Target, secrets, dumpTool, strings.Repeat("0", 64), stageDirectory); err == nil {
		t.Fatal("snapshot staging accepted a changed tool checksum")
	}
	dumpPath, artifact, err := StageMySQLSQLSnapshot(ctx, source, source.Target, secrets, dumpTool, fmt.Sprintf("%x", toolChecksum), stageDirectory)
	if err != nil {
		t.Fatalf("stage MySQL snapshot: %v", err)
	}
	if caPath != "" {
		// The same mysqldump client must reject an unrelated CA. This is a
		// negative control for the CLI transport, separate from the Go probe.
		unrelated := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		wrongCA := filepath.Join(t.TempDir(), "wrong-ca.pem")
		if err := os.WriteFile(wrongCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: unrelated.Certificate().Raw}), 0o600); err != nil {
			unrelated.Close()
			t.Fatal(err)
		}
		unrelated.Close()
		wrong := exec.CommandContext(ctx, dumpTool, "--defaults-file="+options(sourceRole, sourcePassword), "--protocol=tcp", "--host="+host,
			"--port="+strconv.Itoa(port), "--ssl-mode=VERIFY_CA", "--ssl-ca="+wrongCA, sourceDB)
		if err := wrong.Run(); err == nil {
			t.Fatal("mysqldump accepted an unrelated CA")
		}
	}
	dumpBytes, err := os.ReadFile(dumpPath)
	if err != nil || len(dumpBytes) == 0 || !strings.Contains(string(dumpBytes), "source-only-recovery-marker") {
		t.Fatal("source dump did not contain the expected marker")
	}
	preparation, err := PrepareMySQLRestore(ctx, resolver, "mini", "restore-db", target.Target, secrets, dumpPath, artifact)
	if err != nil || preparation.Target != target.Target || preparation.Source != source.Target {
		t.Fatalf("exact-target restore preflight = %+v, %v", preparation, err)
	}
	if _, err := PrepareMySQLRestore(ctx, resolver, "mini", "restore-db", stale, secrets, dumpPath, artifact); err == nil {
		t.Fatal("stale restore target passed preflight")
	}
	badPath := filepath.Join(t.TempDir(), "tampered.sql")
	badBytes := append([]byte(nil), dumpBytes...)
	badBytes[len(badBytes)-1] ^= 1
	if err := os.WriteFile(badPath, badBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyMySQLSQLArtifact(badPath, artifact); err == nil {
		t.Fatal("tampered SQL dump passed checksum verification")
	}
	symlink := filepath.Join(t.TempDir(), "linked.sql")
	if err := os.Symlink(dumpPath, symlink); err != nil {
		t.Fatal(err)
	}
	if err := VerifyMySQLSQLArtifact(symlink, artifact); err == nil {
		t.Fatal("symlink SQL dump passed verification")
	}
	input, err := os.Open(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	restoreCommand := exec.CommandContext(ctx, "mysql", "--defaults-file="+targetOptions, "--protocol=tcp", "--host="+host, "--port="+portText, "--database="+targetDB)
	restoreCommand.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	restoreCommand.Stdin = input
	if output, err := restoreCommand.CombinedOutput(); err != nil {
		t.Fatalf("target restore failed: %s", targetSession.Redact(output))
	}
	// Verify both ends through the admin connection. The target marker must be
	// present and the source marker must remain untouched.
	for _, db := range []string{sourceDB, targetDB} {
		var marker string
		if err := admin.QueryRowContext(ctx, "SELECT value FROM `"+db+"`.marker").Scan(&marker); err != nil || marker != "source-only-recovery-marker" {
			t.Fatalf("%s marker = %q, %v", db, marker, err)
		}
	}
	if _, err := PrepareMySQLRestore(ctx, resolver, "mini", "restore-db", target.Target, secrets, dumpPath, artifact); err == nil {
		t.Fatal("nonempty restore target passed preflight")
	}
	t.Logf("disposable MySQL exact-target dump/restore passed: sha256=%s", artifact.SHA256)
}
