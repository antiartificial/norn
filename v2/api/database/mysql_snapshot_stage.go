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
	if !validMySQLArtifactIdentity(expected) || expected != resolved.Target || resolved.Purpose != PurposeApplication {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot requires an exact application target")
	}
	if err := verifyMySQLSnapshotTool(dumpToolPath, dumpToolSHA256); err != nil {
		return "", MySQLSQLArtifact{}, err
	}
	if err := verifyMySQLPrivateDirectory(privateDirectory); err != nil {
		return "", MySQLSQLArtifact{}, err
	}
	session, err := OpenSession(ctx, resolved, secrets)
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
	var nontransactional int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE' AND ENGINE <> 'InnoDB'`).Scan(&nontransactional); err != nil {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot engine inspection failed")
	}
	if nontransactional != 0 {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot contains nontransactional tables")
	}
	if strings.ContainsAny(session.password, "\x00\r\n") {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot credential is not supported by private option files")
	}
	options := filepath.Join(session.directory, "mysql.cnf")
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(session.password)
	if err := writePrivate(options, []byte("[client]\nuser="+expected.Role+"\npassword=\""+escaped+"\"\n")); err != nil {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot private client material failed")
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
	command := exec.CommandContext(ctx, dumpToolPath, "--defaults-file="+options, "--protocol=tcp", "--host="+resolved.Endpoint.Host,
		"--port="+strconv.Itoa(resolved.Endpoint.Port), "--single-transaction", "--quick", "--no-tablespaces",
		"--set-gtid-purged=OFF", "--hex-blob", "--routines", "--events", "--triggers", expected.Database)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	command.Stdout = writer
	stderr := capture.New(4096, 4096)
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return "", MySQLSQLArtifact{}, fmt.Errorf("MySQL snapshot tool failed: %s", session.RedactCaptured(stderr))
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
	artifact := MySQLSQLArtifact{Format: MySQLSQLArtifactV1, Source: expected, Bytes: writer.written, SHA256: hex.EncodeToString(writer.hash.Sum(nil))}
	if err := VerifyMySQLSQLArtifact(path, artifact); err != nil {
		_ = os.Remove(path)
		return "", MySQLSQLArtifact{}, err
	}
	return path, artifact, nil
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
