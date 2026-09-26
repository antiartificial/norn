package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/artifactstore"
	"norn/v2/api/database"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

// runInspectSource reads signed control evidence and optionally reobserves
// external state. It never claims an operation or mutates a provider.
func runInspectSource(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("inspect-source", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databaseURLFile := flags.String("database-url-file", "", "owner-only PostgreSQL URL file")
	auditKeyFile := flags.String("audit-key-file", "", "owner-only audit key file")
	var previousKeyFiles privatePaths
	flags.Var(&previousKeyFiles, "previous-audit-key-file", "owner-only previous audit verification key file")
	authority := flags.String("authority", "", "expected control authority UUID")
	schema := flags.String("schema", "public", "control PostgreSQL schema")
	operationID := flags.String("source-operation-id", "", "exact signed source operation ID")
	observeExternal := flags.Bool("observe-external", false, "read exact Nomad, MySQL and retained-object state")
	secretsDir := flags.String("secrets-dir", "", "owner-only MySQL secret directory")
	nomadURL := flags.String("nomad-url", "", "Nomad endpoint")
	s3Endpoint := flags.String("s3-endpoint", "", "S3 host:port")
	s3Bucket := flags.String("s3-bucket", "", "immutable artifact bucket")
	s3Prefix := flags.String("s3-prefix", "", "artifact prefix")
	s3Region := flags.String("s3-region", "", "S3 region")
	s3AccessFile := flags.String("s3-access-key-file", "", "owner-only S3 access key file")
	s3SecretFile := flags.String("s3-secret-key-file", "", "owner-only S3 secret key file")
	s3Insecure := flags.Bool("s3-loopback-http", false, "allow HTTP only for numeric loopback S3 endpoint")
	observationTimeout := flags.Duration("observation-timeout", 10*time.Minute, "bound external read-only checks")
	if err := flags.Parse(arguments); err != nil || len(flags.Args()) != 0 {
		return errors.New("invalid source inspection arguments")
	}
	if *databaseURLFile == "" || *auditKeyFile == "" || *authority == "" || *operationID == "" || !schemaPattern.MatchString(*schema) {
		return errors.New("incomplete private source inspection selection")
	}
	if *observeExternal && (*secretsDir == "" || *nomadURL == "" || *s3Endpoint == "" || *s3Bucket == "" ||
		*s3Region == "" || *s3AccessFile == "" || *s3SecretFile == "" || *observationTimeout <= 0) {
		return errors.New("incomplete external source inspection selection")
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
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return errors.New("control PostgreSQL configuration is invalid")
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	store.DeclareReaderContract(config, "mysql-source-inspection")
	config.ConnConfig.RuntimeParams["search_path"] = *schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("cannot open control PostgreSQL pool")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("control PostgreSQL is unavailable")
	}
	db := &store.DB{Pool: pool}
	signer, err := store.NewHMACAcceptanceSigner(auditKey, previous...)
	if err != nil {
		return errors.New("audit signing key is invalid")
	}
	acceptance, err := store.NewPGOperationStore(db, signer, store.AcceptancePolicy{ExpectedAuthority: *authority})
	if err != nil {
		return errors.New("signed acceptance policy is invalid")
	}
	if !*observeExternal {
		inspection, err := db.InspectPrivateMySQLSourceSnapshot(ctx, acceptance, *operationID)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(inspection)
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
	access, err := readPrivateText(*s3AccessFile)
	if err != nil {
		return errors.New("S3 access key file is not owner-only regular input")
	}
	secret, err := readPrivateText(*s3SecretFile)
	if err != nil {
		return errors.New("S3 secret key file is not owner-only regular input")
	}
	observeCtx, cancel := context.WithTimeout(ctx, *observationTimeout)
	defer cancel()
	objects, err := artifactstore.OpenS3ReadOnlyVerifier(observeCtx, artifactstore.S3Config{
		Endpoint: *s3Endpoint, Bucket: *s3Bucket, Prefix: *s3Prefix, Region: *s3Region,
		AccessKey: access, SecretKey: secret, Insecure: *s3Insecure,
	})
	if err != nil {
		return errors.New("read-only retained artifact store is unavailable")
	}
	inspection, err := db.InspectPrivateMySQLSourceSnapshotLive(observeCtx, acceptance, *operationID, observer, secrets, objects)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(inspection)
}
