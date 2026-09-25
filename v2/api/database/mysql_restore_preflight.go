package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

const MySQLSQLArtifactV1 = "norn.mysql-sql/v1"
const MaxMySQLStagedArtifactBytes int64 = 64 << 30

// MySQLSQLArtifact identifies a local, private SQL dump. A future durable
// recovery operation must sign and retain this record with its operation and
// source catalog revision before any restore is allowed.
type MySQLSQLArtifact struct {
	Format MySQLSQLArtifactFormat `json:"format"`
	Source TargetIdentity         `json:"source"`
	Bytes  int64                  `json:"bytes"`
	SHA256 string                 `json:"sha256"`
}

type MySQLSQLArtifactFormat string

// MySQLRestorePreparation is a read-only result for a future durable restore
// executor. It grants no permission to mutate the target.
type MySQLRestorePreparation struct {
	Source TargetIdentity `json:"source"`
	Target TargetIdentity `json:"target"`
	Bytes  int64          `json:"bytes"`
	SHA256 string         `json:"sha256"`
}

// PrepareMySQLRestore verifies an exact catalog target, a bounded owner-only
// artifact and an empty destination before any SQL write. It deliberately
// bypasses neither the public capability gate nor the need for a durable
// operation fence: callers cannot use this result as restore authorization.
func PrepareMySQLRestore(ctx context.Context, resolver *Resolver, profileID, logicalID string, expected TargetIdentity, secrets SecretSource, path string, artifact MySQLSQLArtifact) (MySQLRestorePreparation, error) {
	if resolver == nil || !validMySQLArtifactIdentity(expected) {
		return MySQLRestorePreparation{}, fmt.Errorf("MySQL restore requires an exact expected target")
	}
	resolved, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: profileID, Purpose: PurposeApplication, LogicalResourceID: logicalID, Expected: &expected})
	if err != nil {
		return MySQLRestorePreparation{}, err
	}
	if resolved.Target.Engine != EngineMySQL || resolved.Target != expected {
		return MySQLRestorePreparation{}, fmt.Errorf("MySQL restore target identity changed")
	}
	if artifact.Format != MySQLSQLArtifactV1 || !validMySQLArtifactIdentity(artifact.Source) ||
		(artifact.Source.ServiceID == expected.ServiceID && artifact.Source.ServiceGeneration == expected.ServiceGeneration && artifact.Source.Database == expected.Database) {
		return MySQLRestorePreparation{}, fmt.Errorf("MySQL restore artifact source is invalid or equals the target")
	}
	if err := VerifyMySQLSQLArtifact(path, artifact); err != nil {
		return MySQLRestorePreparation{}, err
	}
	session, err := OpenSession(ctx, resolved, secrets)
	if err != nil {
		return MySQLRestorePreparation{}, err
	}
	defer session.Close()
	if _, err := session.Probe(ctx); err != nil {
		return MySQLRestorePreparation{}, err
	}
	if session.mysqlConnector == nil {
		return MySQLRestorePreparation{}, fmt.Errorf("MySQL restore target connector is unavailable")
	}
	db := sql.OpenDB(session.mysqlConnector)
	defer db.Close()
	for _, query := range []string{
		`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()`,
		`SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE()`,
		`SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA = DATABASE()`,
	} {
		var count int64
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return MySQLRestorePreparation{}, fmt.Errorf("MySQL restore destination inspection failed")
		}
		if count != 0 {
			return MySQLRestorePreparation{}, fmt.Errorf("MySQL restore destination is not empty")
		}
	}
	return MySQLRestorePreparation{Source: artifact.Source, Target: expected, Bytes: artifact.Bytes, SHA256: artifact.SHA256}, nil
}

// VerifyMySQLSQLArtifact rejects symlinks, non-regular or publicly readable
// files, size drift and checksum mismatch. The artifact is checked before
// opening the destination connection. A later executor must verify it again
// while holding its durable target fence and use the same opened inode.
func VerifyMySQLSQLArtifact(path string, artifact MySQLSQLArtifact) error {
	if artifact.Format != MySQLSQLArtifactV1 || !validMySQLArtifactIdentity(artifact.Source) || artifact.Bytes <= 0 || artifact.Bytes > MaxMySQLStagedArtifactBytes || len(artifact.SHA256) != 64 {
		return fmt.Errorf("MySQL restore artifact metadata is invalid")
	}
	if _, err := hex.DecodeString(artifact.SHA256); err != nil || strings.ToLower(artifact.SHA256) != artifact.SHA256 {
		return fmt.Errorf("MySQL restore artifact checksum is invalid")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("MySQL restore artifact path must be absolute")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("MySQL restore artifact cannot be opened")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() != artifact.Bytes {
		return fmt.Errorf("MySQL restore artifact is not a matching private regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("MySQL restore artifact ownership is invalid")
	}
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, artifact.Bytes+1))
	if err != nil || read != artifact.Bytes {
		return fmt.Errorf("MySQL restore artifact read failed")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), artifact.SHA256) {
		return fmt.Errorf("MySQL restore artifact checksum differs")
	}
	endInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, endInfo) || endInfo.Size() != artifact.Bytes || !endInfo.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("MySQL restore artifact changed while reading")
	}
	var extra [1]byte
	if n, err := file.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		return fmt.Errorf("MySQL restore artifact changed while reading")
	}
	return nil
}

func validMySQLArtifactIdentity(target TargetIdentity) bool {
	return target.Engine == EngineMySQL && target.ServiceGeneration > 0 && target.BindingGeneration > 0 &&
		identifierPattern.MatchString(target.ServiceID) && identifierPattern.MatchString(target.BindingID) &&
		mysqlDatabasePattern.MatchString(target.Database) && mysqlUserPattern.MatchString(target.Role)
}
