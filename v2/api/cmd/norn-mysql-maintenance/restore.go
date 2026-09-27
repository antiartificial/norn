package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/artifactstore"
	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// restore consumes only an already signed, explicitly selected one-attempt
// restore. It does not manufacture a target or infer an artifact from a path.
func runRestore(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databaseURLFile := flags.String("database-url-file", "", "owner-only PostgreSQL URL file")
	auditKeyFile := flags.String("audit-key-file", "", "owner-only audit key file")
	var previousKeyFiles privatePaths
	flags.Var(&previousKeyFiles, "previous-audit-key-file", "owner-only previous audit verification key file")
	authority := flags.String("authority", "", "expected control authority UUID")
	schema := flags.String("schema", "public", "control PostgreSQL schema")
	secretsDir := flags.String("secrets-dir", "", "owner-only MySQL secret directory")
	operationID := flags.String("restore-operation-id", "", "accepted restore operation UUID")
	targetDatabase := flags.String("target-database", "", "expected restored MySQL database")
	materializeDir := flags.String("materialize-dir", "", "owner-only artifact materialization directory")
	toolPath := flags.String("mysql-tool-path", "", "absolute mysql client path")
	toolDigest := flags.String("mysql-tool-sha256", "", "expected mysql client SHA256")
	s3Endpoint := flags.String("s3-endpoint", "", "S3 host:port")
	s3Bucket := flags.String("s3-bucket", "", "immutable artifact bucket")
	s3Prefix := flags.String("s3-prefix", "", "artifact prefix")
	s3Region := flags.String("s3-region", "", "S3 region")
	s3AccessFile := flags.String("s3-access-key-file", "", "owner-only S3 access key file")
	s3SecretFile := flags.String("s3-secret-key-file", "", "owner-only S3 secret key file")
	s3Spool := flags.String("s3-spool-dir", "", "owner-only S3 spool directory")
	s3Capacity := flags.Int64("s3-spool-capacity", 0, "S3 spool capacity in bytes")
	s3Insecure := flags.Bool("s3-loopback-http", false, "allow HTTP only for numeric loopback S3 endpoint")
	if err := flags.Parse(arguments); err != nil || len(flags.Args()) != 0 {
		return errors.New("invalid restore arguments")
	}
	if *databaseURLFile == "" || *auditKeyFile == "" || *authority == "" || *secretsDir == "" ||
		*operationID == "" || *targetDatabase == "" || *materializeDir == "" || *toolPath == "" ||
		*toolDigest == "" || *s3Endpoint == "" || *s3Bucket == "" || *s3Region == "" ||
		*s3AccessFile == "" || *s3SecretFile == "" || *s3Spool == "" || *s3Capacity <= 0 ||
		!schemaPattern.MatchString(*schema) {
		return errors.New("incomplete private restore selection")
	}
	if _, err := uuid.Parse(*operationID); err != nil {
		return errors.New("restore operation ID is invalid")
	}
	if err := privateDirectory(*materializeDir); err != nil {
		return errors.New("materialization directory is not owner-only")
	}
	if err := privateDirectory(*s3Spool); err != nil {
		return errors.New("S3 spool directory is not owner-only")
	}
	if !filepath.IsAbs(*toolPath) || len(*toolDigest) != 64 {
		return errors.New("mysql client path or digest is invalid")
	}
	toolBytes, err := os.ReadFile(*toolPath)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(toolBytes)) != strings.ToLower(*toolDigest) {
		return errors.New("mysql client digest mismatch")
	}
	databaseURL, err := readPrivateText(*databaseURLFile)
	if err != nil {
		return errors.New("database URL file is not owner-only regular input")
	}
	auditKey, err := readPrivateText(*auditKeyFile)
	if err != nil {
		return errors.New("audit key file is not owner-only regular input")
	}
	previous := make([]string, 0, len(previousKeyFiles))
	for _, path := range previousKeyFiles {
		key, err := readPrivateText(path)
		if err != nil {
			return errors.New("previous audit key file is not owner-only regular input")
		}
		previous = append(previous, key)
	}
	access, err := readPrivateText(*s3AccessFile)
	if err != nil {
		return errors.New("S3 access key file is not owner-only regular input")
	}
	secret, err := readPrivateText(*s3SecretFile)
	if err != nil {
		return errors.New("S3 secret key file is not owner-only regular input")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil || config.MaxConns < 4 {
		return errors.New("control PostgreSQL configuration is invalid")
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	store.DeclareReaderContract(config, "mysql-restore")
	config.ConnConfig.RuntimeParams["search_path"] = *schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("cannot open control PostgreSQL pool")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("control PostgreSQL is unavailable")
	}
	control := &store.DB{Pool: pool}
	signer, err := store.NewHMACAcceptanceSigner(auditKey, previous...)
	if err != nil {
		return errors.New("audit signing key is invalid")
	}
	acceptance, err := store.NewPGOperationStore(control, signer, store.AcceptancePolicy{ExpectedAuthority: *authority})
	if err != nil {
		return errors.New("signed acceptance policy is invalid")
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, *operationID)
	if err != nil || accepted.Operation.Kind != store.MySQLRestoreOperationKind || accepted.Operation.Status != model.OperationQueued {
		return errors.New("selected signed restore is unavailable or not queued")
	}
	encoded, err := json.Marshal(accepted.Operation.Payload)
	var request store.MySQLRestoreRequest
	if err != nil || json.Unmarshal(encoded, &request) != nil || request.Target.Database != *targetDatabase {
		return errors.New("expected target database does not match signed restore")
	}
	secrets, err := database.NewDirectorySecretSource(*secretsDir)
	if err != nil {
		return errors.New("MySQL secret directory is unavailable")
	}
	defer secrets.Close()
	objects, err := artifactstore.OpenS3(ctx, artifactstore.S3Config{Endpoint: *s3Endpoint, Bucket: *s3Bucket,
		Prefix: *s3Prefix, Region: *s3Region, AccessKey: access, SecretKey: secret,
		Insecure: *s3Insecure, SpoolDirectory: *s3Spool, SpoolCapacity: *s3Capacity, RetainFor: 24 * time.Hour})
	if err != nil {
		return fmt.Errorf("retained artifact store is unavailable: %w", err)
	}
	owner := fmt.Sprintf("mysql-restore:%d:%s", os.Getpid(), *operationID)
	claimed, claim, err := control.ClaimPrivateMySQLOperation(ctx, *operationID, owner, store.MySQLRestoreOperationKind, 2*time.Minute)
	if err != nil || claimed == nil || claim.OperationID() != *operationID {
		return fmt.Errorf("signed restore operation is not claimable: %w", err)
	}
	if _, err := control.PrepareClaimedMySQLRestoreFromRetainedSupervised(ctx, acceptance, claim, request, secrets, objects, *materializeDir, 2*time.Minute); err != nil {
		return fmt.Errorf("retained restore preparation requires inspection: %w", err)
	}
	runner := store.MySQLRestoreRunner{Control: control, Acceptance: acceptance, Secrets: secrets,
		Objects: objects, MaterializeDirectory: *materializeDir,
		Tool: database.MySQLRestoreTool{Path: *toolPath, SHA256: strings.ToLower(*toolDigest)}}
	if err := runner.RunClaimed(ctx, claim); err != nil {
		return fmt.Errorf("signed restore requires inspection: %w", err)
	}
	finished, err := control.GetOperation(ctx, *operationID)
	if err != nil || finished.Status != model.OperationSucceeded {
		return errors.New("signed restore terminal receipt is unavailable")
	}
	_, err = fmt.Fprintf(output, "restore_operation_id=%s status=succeeded\n", *operationID)
	return err
}

func privateDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("private directory path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("private directory must be owner-only")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return errors.New("private directory owner mismatch")
	}
	return nil
}
