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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/artifactstore"
	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

type sourceDatabaseStager struct{}

func (sourceDatabaseStager) Stage(ctx context.Context, source database.ResolvedBinding, expected database.TargetIdentity, secrets database.SecretSource, tool, digest, directory string) (string, database.MySQLSQLArtifact, error) {
	return database.StageMySQLSQLSnapshotWithMaintenanceCredential(ctx, source, expected, secrets, tool, digest, directory)
}

// source admits one deployed database and runs all one-attempt source effects
// under the same exact claim. Any uncertain effect leaves the source fenced.
func runSource(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("source", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databaseURLFile := flags.String("database-url-file", "", "owner-only PostgreSQL URL file")
	auditKeyFile := flags.String("audit-key-file", "", "owner-only audit key file")
	var previousKeyFiles privatePaths
	flags.Var(&previousKeyFiles, "previous-audit-key-file", "owner-only previous audit verification key file")
	authority := flags.String("authority", "", "expected control authority UUID")
	schema := flags.String("schema", "public", "control PostgreSQL schema")
	secretsDir := flags.String("secrets-dir", "", "owner-only MySQL secret directory")
	nomadURL := flags.String("nomad-url", "", "Nomad endpoint")
	selectionFile := flags.String("selection-file", "", "owner-only JSON source selection file")
	sourceDatabase := flags.String("source-database", "", "expected source database")
	actorIssuer := flags.String("actor-issuer", "", "operator actor issuer")
	actorSubject := flags.String("actor-subject", "", "operator actor subject")
	requestKey := flags.String("request-key", "", "stable idempotency key")
	stageDir := flags.String("stage-dir", "", "owner-only SQL stage directory")
	dumpTool := flags.String("dump-tool-path", "", "absolute mysqldump path")
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
		return errors.New("invalid source arguments")
	}
	if *databaseURLFile == "" || *auditKeyFile == "" || *authority == "" || *secretsDir == "" ||
		*nomadURL == "" || *selectionFile == "" || *sourceDatabase == "" || *actorIssuer == "" ||
		*actorSubject == "" || *requestKey == "" || *stageDir == "" || *dumpTool == "" ||
		*s3Endpoint == "" || *s3Bucket == "" || *s3Region == "" || *s3AccessFile == "" ||
		*s3SecretFile == "" || *s3Spool == "" || *s3Capacity <= 0 || !schemaPattern.MatchString(*schema) {
		return errors.New("incomplete private source selection")
	}
	if err := privateDirectory(*stageDir); err != nil {
		return errors.New("SQL stage directory is not owner-only")
	}
	if err := privateDirectory(*s3Spool); err != nil {
		return errors.New("S3 spool directory is not owner-only")
	}
	if !filepath.IsAbs(*dumpTool) {
		return errors.New("mysqldump path must be absolute")
	}
	selectionJSON, err := readPrivateText(*selectionFile)
	if err != nil {
		return errors.New("source selection file is not owner-only regular input")
	}
	var selection store.MySQLSourceSnapshotAdmissionRequest
	decoder := json.NewDecoder(strings.NewReader(selectionJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&selection); err != nil || decoder.Decode(new(interface{})) != io.EOF || selection.Binding.Source.Database != *sourceDatabase {
		return errors.New("source selection is invalid or names a different database")
	}
	toolBytes, err := os.ReadFile(*dumpTool)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(toolBytes)) != selection.DumpToolSHA256 {
		return errors.New("mysqldump digest does not match source selection")
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
	store.DeclareReaderContract(config, "mysql-source")
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
	secrets, err := database.NewDirectorySecretSource(*secretsDir)
	if err != nil {
		return errors.New("MySQL secret directory is unavailable")
	}
	defer secrets.Close()
	observer, err := nomad.NewClient(*nomadURL)
	if err != nil {
		return errors.New("Nomad client is unavailable")
	}
	objects, err := artifactstore.OpenS3(ctx, artifactstore.S3Config{Endpoint: *s3Endpoint, Bucket: *s3Bucket,
		Prefix: *s3Prefix, Region: *s3Region, AccessKey: access, SecretKey: secret,
		Insecure: *s3Insecure, SpoolDirectory: *s3Spool, SpoolCapacity: *s3Capacity, RetainFor: 24 * time.Hour})
	if err != nil {
		return fmt.Errorf("retained artifact store is unavailable: %w", err)
	}
	accepted, err := control.AcceptPrivateMySQLSourceSnapshot(ctx, acceptance, observer, store.MySQLSourceSnapshotAcceptanceInput{
		Selection: selection, Actor: store.OperationActor{Issuer: *actorIssuer, Subject: *actorSubject}, Key: *requestKey,
		Audit: store.AcceptanceAuditContext{Source: "private-mysql-maintenance-cli"}})
	if err != nil {
		return fmt.Errorf("signed source admission failed: %w", err)
	}
	if accepted.Operation.Status != model.OperationQueued {
		if _, err := control.LoadSignedMySQLSourceArtifactRetentionReceipt(ctx, acceptance, accepted.Operation.ID); err != nil {
			return errors.New("existing source operation requires inspection")
		}
		_, err := fmt.Fprintf(output, "source_operation_id=%s status=retained-proved\n", accepted.Operation.ID)
		return err
	}
	if _, err := acceptance.VerifyAcceptedOperation(ctx, accepted.Operation.ID); err != nil {
		return fmt.Errorf("signed source verification failed: %w", err)
	}
	encoded, err := json.Marshal(accepted.Operation.Payload)
	var request store.MySQLSourceSnapshotRequest
	if err != nil || json.Unmarshal(encoded, &request) != nil || request.Source.Database != *sourceDatabase {
		return errors.New("signed source request differs from expected database")
	}
	owner := fmt.Sprintf("mysql-source:%d:%s", os.Getpid(), accepted.Operation.ID)
	claimed, claim, err := control.ClaimPrivateMySQLOperation(ctx, accepted.Operation.ID, owner, store.MySQLSourceSnapshotOperationKind, 2*time.Minute)
	if err != nil || claimed == nil || claim.OperationID() != accepted.Operation.ID {
		return fmt.Errorf("signed source operation is not claimable: %w", err)
	}
	runner := store.MySQLSourceSnapshotRunner{Control: control, Acceptance: acceptance, Secrets: secrets, Stopper: observer}
	if err := runner.RunClaimed(ctx, claim, request); err != nil {
		return fmt.Errorf("source quiescence requires inspection: %w", err)
	}
	staged, err := runner.StageClaimed(ctx, claim, request, *dumpTool, *stageDir, sourceDatabaseStager{})
	if err != nil {
		return fmt.Errorf("source staging requires inspection: %w", err)
	}
	retained, err := control.RetainClaimedMySQLSourceArtifact(ctx, acceptance, claim, objects)
	if err != nil {
		return fmt.Errorf("source retention requires inspection: %w", err)
	}
	if retained.Receipt.StagingReceiptSHA256 != staged.SHA256 {
		return errors.New("retained source receipt differs from staged receipt")
	}
	if err := os.Remove(staged.Receipt.ArtifactPath); err != nil {
		return fmt.Errorf("source retained, but local staged SQL cleanup failed: %w", err)
	}
	_, err = fmt.Fprintf(output, "source_operation_id=%s status=retained-proved\n", accepted.Operation.ID)
	return err
}
