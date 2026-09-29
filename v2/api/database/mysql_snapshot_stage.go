package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"norn/v2/api/capture"
)

type mysqlBoundedDumpWriter struct {
	written int64
	max     int64
	file    *os.File
	hash    hash.Hash
}

func (w *mysqlBoundedDumpWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.max-w.written {
		return 0, fmt.Errorf("MySQL snapshot exceeds the staged artifact limit")
	}
	n, err := w.file.Write(data)
	if n > 0 {
		_, _ = w.hash.Write(data[:n])
		w.written += int64(n)
	}
	return n, err
}

// StageMySQLSQLSnapshot creates a private, bounded SQL artifact from an exact
// application target. This is a local staging primitive, not durable backup
// publication. The caller must fence the catalog revision and quiesce DDL and
// writes before invocation; no public snapshot capability is enabled here.
func StageMySQLSQLSnapshot(ctx context.Context, resolved ResolvedBinding, expected TargetIdentity, secrets SecretSource, dumpToolPath, dumpToolSHA256, privateDirectory string) (string, MySQLSQLArtifact, error) {
	return stageMySQLSQLSnapshot(ctx, resolved, resolved, expected, "", secrets, dumpToolPath, dumpToolSHA256, privateDirectory)
}

// StageMySQLSQLSnapshotWithMaintenanceCredential stages a source artifact
// through the catalog-derived snapshot account. The source TargetIdentity is
// retained in the artifact; the snapshot account is connection material only.
// A future signed source-snapshot runner must use this path after it has
// re-resolved and bound the source catalog generation. This function creates
// no snapshot operation or publication authority.
func StageMySQLSQLSnapshotWithMaintenanceCredential(ctx context.Context, source ResolvedBinding, expected TargetIdentity, secrets SecretSource, dumpToolPath, dumpToolSHA256, privateDirectory string) (string, MySQLSQLArtifact, error) {
	snapshot, err := MySQLSnapshotBinding(source)
	if err != nil {
		return "", MySQLSQLArtifact{}, err
	}
	return StageMySQLSQLSnapshotWithResolvedCredential(ctx, source, snapshot, expected, secrets, dumpToolPath, dumpToolSHA256, privateDirectory)
}

// StageMySQLSQLSnapshotWithResolvedCredential accepts only the snapshot
// identity derived from source.MySQLMaintenance. Keeping this explicit gives a
// signed future runner a narrow credential handoff without allowing callers to
// replace the source identity recorded in the artifact.
func StageMySQLSQLSnapshotWithResolvedCredential(ctx context.Context, source, snapshot ResolvedBinding, expected TargetIdentity, secrets SecretSource, dumpToolPath, dumpToolSHA256, privateDirectory string) (string, MySQLSQLArtifact, error) {
	maintenance := source.MySQLMaintenance
	if maintenance == nil || snapshot.Target.Role != maintenance.SnapshotRole || snapshot.CredentialRef != maintenance.SnapshotCredentialRef || snapshot.Endpoint != source.Endpoint || snapshot.TLS != source.TLS || snapshot.Purpose != source.Purpose {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot credential is not the catalog-derived maintenance identity")
	}
	return stageMySQLSQLSnapshot(ctx, source, snapshot, expected, maintenance.SnapshotAccountHost, secrets, dumpToolPath, dumpToolSHA256, privateDirectory)
}

func stageMySQLSQLSnapshot(ctx context.Context, source, credential ResolvedBinding, expected TargetIdentity, expectedAccountHost string, secrets SecretSource, dumpToolPath, dumpToolSHA256, privateDirectory string) (string, MySQLSQLArtifact, error) {
	if !validMySQLArtifactIdentity(expected) || expected != source.Target || source.Purpose != PurposeApplication || credential.Target.Engine != EngineMySQL || credential.Target.ServiceID != expected.ServiceID || credential.Target.ServiceGeneration != expected.ServiceGeneration || credential.Target.BindingID != expected.BindingID || credential.Target.BindingGeneration != expected.BindingGeneration || credential.Target.Database != expected.Database {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot requires an exact application target")
	}
	if err := verifyMySQLSnapshotTool(dumpToolPath, dumpToolSHA256); err != nil {
		return "", MySQLSQLArtifact{}, err
	}
	if err := verifyMySQLPrivateDirectory(privateDirectory); err != nil {
		return "", MySQLSQLArtifact{}, err
	}
	session, err := OpenSession(ctx, credential, secrets)
	if err != nil {
		return "", MySQLSQLArtifact{}, err
	}
	defer session.Close()
	if _, err := session.Probe(ctx); err != nil {
		return "", MySQLSQLArtifact{}, err
	}
	if session.mysqlConnector == nil {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot connector is unavailable")
	}
	db := sql.OpenDB(session.mysqlConnector)
	defer db.Close()
	if expectedAccountHost != "" {
		if err := verifyMySQLSnapshotAccount(ctx, db, credential.Target.Role, expectedAccountHost); err != nil {
			return "", MySQLSQLArtifact{}, err
		}
	}
	var nontransactional int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE' AND ENGINE <> 'InnoDB'`).Scan(&nontransactional); err != nil {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot engine inspection failed")
	}
	if nontransactional != 0 {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot contains nontransactional tables")
	}
	before, err := inspectMySQLRestoreExpectationDB(ctx, db, source.Target.Database)
	if err != nil {
		return "", MySQLSQLArtifact{}, err
	}
	if strings.ContainsAny(session.password, "\x00\r\n") {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot credential is not supported by private option files")
	}
	options := filepath.Join(session.directory, "mysql.cnf")
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(session.password)
	if err := writePrivate(options, []byte("[client]\nuser="+credential.Target.Role+"\npassword=\""+escaped+"\"\n")); err != nil {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot private client material failed")
	}
	tlsArgs, err := mysqlSnapshotTLSArgs(session, credential.TLS)
	if err != nil {
		return "", MySQLSQLArtifact{}, err
	}
	file, err := os.CreateTemp(privateDirectory, "mysql-snapshot-*.sql")
	if err != nil {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot artifact staging failed")
	}
	path := file.Name()
	defer func() {
		if file != nil {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	writer := &mysqlBoundedDumpWriter{max: MaxMySQLStagedArtifactBytes, file: file, hash: sha256.New()}
	args := []string{"--defaults-file=" + options, "--protocol=tcp", "--host=" + source.Endpoint.Host,
		"--port=" + strconv.Itoa(source.Endpoint.Port)}
	args = append(args, tlsArgs...)
	args = append(args, "--single-transaction", "--quick", "--no-tablespaces",
		"--set-gtid-purged=OFF", "--hex-blob", "--routines", "--events", "--triggers", expected.Database)
	command := exec.CommandContext(ctx, dumpToolPath, args...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	command.Stdout = writer
	stderr := capture.New(4096, 4096)
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot tool failed: %s", session.RedactCaptured(stderr))
	}
	after, err := inspectMySQLRestoreExpectationDB(ctx, db, source.Target.Database)
	if err != nil {
		return "", MySQLSQLArtifact{}, err
	}
	if before != after {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot source changed while staging")
	}
	if writer.written == 0 || writer.written > MaxMySQLStagedArtifactBytes {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot artifact is empty or oversized")
	}
	if err := file.Sync(); err != nil {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot artifact sync failed")
	}
	if err := file.Close(); err != nil {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot artifact close failed")
	}
	file = nil
	artifact := MySQLSQLArtifact{Format: MySQLSQLArtifactV2, Source: expected, Bytes: writer.written, SHA256: hex.EncodeToString(writer.hash.Sum(nil)), Expectation: after}
	if err := VerifyMySQLSQLArtifact(path, artifact); err != nil {
		_ = os.Remove(path)
		return "", MySQLSQLArtifact{}, err
	}
	return path, artifact, nil
}

func verifyMySQLSnapshotAccount(ctx context.Context, db *sql.DB, role, host string) error {
	var current string
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&current); err != nil || current != role+"@"+host {
		return fmt.Errorf("MySQL snapshot authenticated as an unexpected account")
	}
	return nil
}

// mysqlSnapshotTLSArgs binds the subprocess to the same verified transport
// policy used by the source probe. The PEM files live in Session's owner-only
// directory and are removed when that session closes.
func mysqlSnapshotTLSArgs(session *Session, binding DatabaseTLS) ([]string, error) {
	if session == nil {
		return nil, fmt.Errorf("MySQL snapshot session is unavailable")
	}
	if binding.Mode == TLSDisabled {
		return []string{"--ssl-mode=DISABLED"}, nil
	}
	if binding.Mode != TLSVerifyCA && binding.Mode != TLSVerifyFull {
		return nil, fmt.Errorf("MySQL snapshot TLS policy is unsupported")
	}
	if binding.Mode == TLSVerifyFull && binding.ServerName != session.endpoint.Host {
		return nil, fmt.Errorf("MySQL snapshot verified host differs from the target endpoint")
	}
	ca := session.runtimeTLS["ca"]
	if len(ca) == 0 {
		return nil, fmt.Errorf("MySQL snapshot verified CA is unavailable")
	}
	caPath := filepath.Join(session.directory, "snapshot-ca.pem")
	if err := writePrivate(caPath, ca); err != nil {
		return nil, fmt.Errorf("MySQL snapshot verified CA staging failed")
	}
	mode := "VERIFY_CA"
	if binding.Mode == TLSVerifyFull {
		mode = "VERIFY_IDENTITY"
	}
	args := []string{"--ssl-mode=" + mode, "--ssl-ca=" + caPath, "--tls-version=TLSv1.2,TLSv1.3"}
	cert, key := session.runtimeTLS["client_cert"], session.runtimeTLS["client_key"]
	if (len(cert) == 0) != (len(key) == 0) {
		return nil, fmt.Errorf("MySQL snapshot client TLS material is incomplete")
	}
	if len(cert) > 0 {
		certPath := filepath.Join(session.directory, "snapshot-client.pem")
		keyPath := filepath.Join(session.directory, "snapshot-client-key.pem")
		if err := writePrivate(certPath, cert); err != nil {
			return nil, fmt.Errorf("MySQL snapshot client certificate staging failed")
		}
		if err := writePrivate(keyPath, key); err != nil {
			return nil, fmt.Errorf("MySQL snapshot client key staging failed")
		}
		args = append(args, "--ssl-cert="+certPath, "--ssl-key="+keyPath)
	}
	return args, nil
}

func verifyMySQLSnapshotTool(path, digest string) error {
	if !filepath.IsAbs(path) || len(digest) != 64 {
		return fmt.Errorf("MySQL snapshot tool path or digest is invalid")
	}
	if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
		return fmt.Errorf("MySQL snapshot tool digest is invalid")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("MySQL snapshot tool cannot be opened")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("MySQL snapshot tool is not a trusted regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil || hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("MySQL snapshot tool checksum differs")
	}
	return nil
}

func verifyMySQLPrivateDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("MySQL snapshot staging directory must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("MySQL snapshot staging directory must be owner-only")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("MySQL snapshot staging directory owner is invalid")
	}
	return nil
}
