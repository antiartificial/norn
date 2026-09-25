package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"norn/v2/api/capture"
)

const maxMySQLRestoreToolBytes int64 = 512 << 20

// MySQLRestoreTool is the independently verified mysql client selected by a
// private supervisor. It is intentionally not a catalog or public API field.
type MySQLRestoreTool struct {
	Path   string
	SHA256 string
}

// RestoreMySQLSQLArtifact runs a signed restore's already-open artifact using
// the configured executable path after checksum-verifying its opened inode.
// The original path is required because macOS clients may load libraries
// relative to it. That leaves a path-resolution race that cannot be removed
// portably with a copied binary or fexecve; a post-run inode mismatch fails
// closed and leaves the durable intent for inspection. The caller must have
// committed its durable external-effect boundary first. This primitive does
// not create that authority and must remain behind a private runner.
func RestoreMySQLSQLArtifact(ctx context.Context, resolved ResolvedBinding, secrets SecretSource, artifactPath string, artifact MySQLSQLArtifact, tool MySQLRestoreTool) error {
	if resolved.Target.Engine != EngineMySQL || !validMySQLArtifactIdentity(resolved.Target) || tool.Path == "" {
		return fmt.Errorf("MySQL restore execution contract is invalid")
	}
	restore, err := MySQLRestoreBinding(resolved)
	if err != nil {
		return err
	}
	artifactFile, err := OpenVerifiedMySQLSQLArtifact(artifactPath, artifact)
	if err != nil {
		return err
	}
	defer artifactFile.Close()
	toolFile, err := openVerifiedMySQLRestoreTool(tool)
	if err != nil {
		return err
	}
	defer toolFile.Close()
	verifiedTool, err := toolFile.Stat()
	if err != nil {
		return fmt.Errorf("MySQL restore tool stat failed")
	}
	session, err := OpenSession(ctx, restore, secrets)
	if err != nil {
		return err
	}
	defer session.Close()
	if _, err := session.Probe(ctx); err != nil {
		return err
	}
	if err := verifyMySQLRestoreAccount(ctx, session, restore.Target.Role, restore.MySQLMaintenance.RestoreAccountHost); err != nil {
		return err
	}
	if strings.ContainsAny(session.password, "\x00\r\n") {
		return fmt.Errorf("MySQL restore credential is not supported by private option files")
	}
	options := filepath.Join(session.directory, "restore.cnf")
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(session.password)
	if err := writePrivate(options, []byte("[client]\nuser="+restore.Target.Role+"\npassword=\""+escaped+"\"\n")); err != nil {
		return fmt.Errorf("MySQL restore private client material failed")
	}
	tlsArgs, err := mysqlRestoreTLSArgs(session, restore.TLS)
	if err != nil {
		return err
	}
	args := []string{"--defaults-file=" + options, "--protocol=tcp", "--host=" + resolved.Endpoint.Host,
		"--port=" + strconv.Itoa(resolved.Endpoint.Port), "--database=" + resolved.Target.Database}
	args = append(args, tlsArgs...)
	command := exec.CommandContext(ctx, tool.Path, args...)
	// A client wrapper may leave a child holding stderr open after the parent
	// is killed on claim loss. Bound pipe draining so the supervisor can record
	// the ambiguous import promptly instead of waiting for that child.
	command.WaitDelay = 200 * time.Millisecond
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	command.Stdin = artifactFile
	stderr := capture.New(4096, 4096)
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("MySQL restore tool failed: %s", session.RedactCaptured(stderr))
	}
	// macOS clients can load private libraries relative to the installed binary,
	// so fexecve or a copied binary is not portable. Keep the checked descriptor
	// open and prove the path still names that inode after execution. This does
	// not prove the exec pathname resolved to that inode; an altered path is
	// treated as an uncertain restore and the caller records needs-inspection.
	current, err := os.Stat(tool.Path)
	if err != nil || !os.SameFile(verifiedTool, current) {
		return fmt.Errorf("MySQL restore tool changed during execution")
	}
	return nil
}

// mysqlRestoreResolvedBinding selects the restore-only identity from an
// already resolved application binding. It is intentionally private: neither
// the runtime adapter nor a public route can request maintenance authority.
// MySQLRestoreBinding derives the restore-only connection from an immutable
// resolved application binding. It exposes no secret values and is used only
// by the private durable restore runner for post-import verification.
func MySQLRestoreBinding(resolved ResolvedBinding) (ResolvedBinding, error) {
	maintenance := resolved.MySQLMaintenance
	if maintenance == nil || maintenance.Generation == 0 ||
		!mysqlUserPattern.MatchString(maintenance.RestoreRole) ||
		!validMySQLAccountHost(maintenance.RestoreAccountHost) ||
		!referencePattern.MatchString(maintenance.RestoreCredentialRef) ||
		maintenance.RestoreRole == resolved.Target.Role || maintenance.RestoreCredentialRef == resolved.CredentialRef {
		return ResolvedBinding{}, fmt.Errorf("MySQL restore maintenance identity is unavailable")
	}
	restore := resolved
	restore.Target.Role = maintenance.RestoreRole
	restore.CredentialRef = maintenance.RestoreCredentialRef
	return restore, nil
}

func verifyMySQLRestoreAccount(ctx context.Context, session *Session, role, host string) error {
	if session == nil || session.mysqlConnector == nil {
		return fmt.Errorf("MySQL restore account verification is unavailable")
	}
	db := sql.OpenDB(session.mysqlConnector)
	defer db.Close()
	var current string
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&current); err != nil || current != role+"@"+host {
		return fmt.Errorf("MySQL restore authenticated as an unexpected account")
	}
	return nil
}

func mysqlRestoreTLSArgs(session *Session, binding DatabaseTLS) ([]string, error) {
	if session == nil {
		return nil, fmt.Errorf("MySQL restore session is unavailable")
	}
	if binding.Mode == TLSDisabled {
		return []string{"--ssl-mode=DISABLED"}, nil
	}
	if binding.Mode != TLSVerifyCA && binding.Mode != TLSVerifyFull {
		return nil, fmt.Errorf("MySQL restore TLS policy is unsupported")
	}
	if binding.Mode == TLSVerifyFull && binding.ServerName != session.endpoint.Host {
		return nil, fmt.Errorf("MySQL restore verified host differs from the target endpoint")
	}
	ca := session.runtimeTLS["ca"]
	if len(ca) == 0 {
		return nil, fmt.Errorf("MySQL restore verified CA is unavailable")
	}
	caPath := filepath.Join(session.directory, "restore-ca.pem")
	if err := writePrivate(caPath, ca); err != nil {
		return nil, fmt.Errorf("MySQL restore verified CA staging failed")
	}
	mode := "VERIFY_CA"
	if binding.Mode == TLSVerifyFull {
		mode = "VERIFY_IDENTITY"
	}
	args := []string{"--ssl-mode=" + mode, "--ssl-ca=" + caPath, "--tls-version=TLSv1.2,TLSv1.3"}
	cert, key := session.runtimeTLS["client_cert"], session.runtimeTLS["client_key"]
	if (len(cert) == 0) != (len(key) == 0) {
		return nil, fmt.Errorf("MySQL restore client TLS material is incomplete")
	}
	if len(cert) > 0 {
		certPath, keyPath := filepath.Join(session.directory, "restore-client.pem"), filepath.Join(session.directory, "restore-client-key.pem")
		if err := writePrivate(certPath, cert); err != nil {
			return nil, fmt.Errorf("MySQL restore client certificate staging failed")
		}
		if err := writePrivate(keyPath, key); err != nil {
			return nil, fmt.Errorf("MySQL restore client key staging failed")
		}
		args = append(args, "--ssl-cert="+certPath, "--ssl-key="+keyPath)
	}
	return args, nil
}

func openVerifiedMySQLRestoreTool(tool MySQLRestoreTool) (*os.File, error) {
	if !filepath.IsAbs(tool.Path) || len(tool.SHA256) != 64 || strings.ToLower(tool.SHA256) != tool.SHA256 {
		return nil, fmt.Errorf("MySQL restore tool path or digest is invalid")
	}
	if _, err := hex.DecodeString(tool.SHA256); err != nil {
		return nil, fmt.Errorf("MySQL restore tool digest is invalid")
	}
	source, err := os.OpenFile(tool.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("MySQL restore tool cannot be opened")
	}
	fail := func(message string) (*os.File, error) { _ = source.Close(); return nil, fmt.Errorf("%s", message) }
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() <= 0 || info.Size() > maxMySQLRestoreToolBytes {
		return fail("MySQL restore tool is not a trusted regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(source, maxMySQLRestoreToolBytes+1)); err != nil {
		return fail("MySQL restore tool verification failed")
	}
	if hex.EncodeToString(hash.Sum(nil)) != tool.SHA256 {
		return fail("MySQL restore tool checksum differs")
	}
	endInfo, err := source.Stat()
	if err != nil || !os.SameFile(info, endInfo) || endInfo.Size() != info.Size() || !endInfo.ModTime().Equal(info.ModTime()) {
		return fail("MySQL restore tool changed while verifying")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return fail("MySQL restore tool cannot be rewound")
	}
	return source, nil
}
