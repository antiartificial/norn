package controlrecovery

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	dumpEntryName     = "control.dump"
	manifestEntryName = "manifest.json"
	envelopeEntryName = "manifest.dsse.json"
	maxManifestBytes  = 4 << 20
)

type CreateBundleOptions struct {
	Pool           *pgxpool.Pool
	DatabaseURL    string
	Schema         string
	OutputPath     string
	Recipients     []age.Recipient
	Signer         *ManifestSigner
	PGDumpPath     string
	Now            func() time.Time
	RequiredKeyIDs []string
}

type VerifiedBundle struct {
	Manifest RecoveryManifest
	archive  *zip.Reader
	dump     *zip.File
	file     *os.File
}

func CreateBundle(ctx context.Context, options CreateBundleOptions) (RecoveryManifest, error) {
	if options.Pool == nil || options.DatabaseURL == "" || options.Schema == "" || options.OutputPath == "" || len(options.Recipients) == 0 || options.Signer == nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery bundle input is incomplete")
	}
	if options.PGDumpPath == "" {
		options.PGDumpPath = "pg_dump"
	}
	if !safeSchemaName.MatchString(options.Schema) {
		return RecoveryManifest{}, fmt.Errorf("control recovery schema name is unsupported")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if _, err := os.Lstat(options.OutputPath); err == nil || !os.IsNotExist(err) {
		return RecoveryManifest{}, fmt.Errorf("control recovery destination already exists or is unavailable")
	}
	service, err := newLibpqService(options.DatabaseURL)
	if err != nil {
		return RecoveryManifest{}, err
	}
	defer service.Close()

	tx, err := options.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery snapshot start failed")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery snapshot normalization failed")
	}
	if err := classifySchema(ctx, tx, options.Schema, InspectionRegistry()); err != nil {
		return RecoveryManifest{}, err
	}
	if err := validateCatalog(ctx, tx, options.Schema); err != nil {
		return RecoveryManifest{}, err
	}
	if err := validateRelationships(ctx, tx, options.Schema); err != nil {
		return RecoveryManifest{}, err
	}
	poolFingerprint, err := databaseFingerprint(ctx, tx)
	if err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery source identity check failed")
	}
	dsnConnection, err := pgx.Connect(ctx, options.DatabaseURL)
	if err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery source command connection failed")
	}
	dsnFingerprint, fingerprintErr := databaseFingerprint(ctx, dsnConnection)
	_ = dsnConnection.Close(context.Background())
	if fingerprintErr != nil || dsnFingerprint != poolFingerprint {
		return RecoveryManifest{}, fmt.Errorf("control recovery pool and command database differ")
	}
	var snapshot string
	if err := tx.QueryRow(ctx, `SELECT pg_export_snapshot()`).Scan(&snapshot); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery snapshot export failed")
	}

	manifest, err := loadManifestInventory(ctx, tx, options, options.Now().UTC())
	if err != nil {
		return RecoveryManifest{}, err
	}
	versionOutput, err := exec.CommandContext(ctx, options.PGDumpPath, "--version").Output()
	if err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery pg_dump preflight failed")
	}
	manifest.PGDumpVersion = strings.TrimSpace(string(versionOutput))

	directory := filepath.Dir(options.OutputPath)
	temporary, err := os.CreateTemp(directory, ".norn-control-recovery-*.tmp")
	if err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery destination staging failed")
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		_ = temporary.Close()
		if !published {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery destination permission failed")
	}
	encrypted, err := age.Encrypt(temporary, options.Recipients...)
	if err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery encryption setup failed")
	}
	archive := zip.NewWriter(encrypted)
	dumpEntry, err := archive.CreateHeader(&zip.FileHeader{Name: dumpEntryName, Method: zip.Store})
	if err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery archive setup failed")
	}
	hash := sha256.New()
	counted := &countWriter{writer: io.MultiWriter(dumpEntry, hash)}
	command := exec.CommandContext(ctx, options.PGDumpPath,
		"--format=custom", "--no-owner", "--no-privileges", "--strict-names",
		"--schema="+options.Schema, "--snapshot="+snapshot, "--dbname=service=norn_recovery",
	)
	command.Env = service.environment
	command.Stdout = counted
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery pg_dump failed")
	}
	manifest.Sections = []ManifestSection{{Name: dumpEntryName, Format: "postgresql-custom", Size: counted.count, SHA256: hex.EncodeToString(hash.Sum(nil))}}
	manifestBytes, envelopeBytes, err := options.Signer.Sign(manifest)
	if err != nil {
		return RecoveryManifest{}, err
	}
	if err := writeZipEntry(archive, manifestEntryName, manifestBytes); err != nil {
		return RecoveryManifest{}, err
	}
	if err := writeZipEntry(archive, envelopeEntryName, envelopeBytes); err != nil {
		return RecoveryManifest{}, err
	}
	if err := archive.Close(); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery archive close failed")
	}
	if err := encrypted.Close(); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery encryption close failed")
	}
	if err := temporary.Sync(); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery destination sync failed")
	}
	if err := temporary.Close(); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery destination close failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery snapshot commit failed")
	}
	if err := os.Link(temporaryPath, options.OutputPath); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery atomic publish failed")
	}
	published = true
	if err := os.Remove(temporaryPath); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery staging cleanup failed")
	}
	if err := syncDirectory(directory); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery directory sync failed")
	}
	return manifest, nil
}

func VerifyBundle(path string, identities []age.Identity, trusted []ed25519.PublicKey, availableKeyIDs []string) (*VerifiedBundle, error) {
	// Non-blocking open so a FIFO cannot stall before the regular-file check.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("control recovery bundle open failed")
	}
	keepOpen := false
	defer func() {
		if !keepOpen {
			_ = file.Close()
		}
	}()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("control recovery bundle stat failed")
	}
	decrypted, plaintextSize, err := age.DecryptReaderAt(file, stat.Size(), identities...)
	if err != nil {
		return nil, fmt.Errorf("control recovery bundle decryption failed")
	}
	archive, err := zip.NewReader(decrypted, plaintextSize)
	if err != nil {
		return nil, fmt.Errorf("control recovery archive is invalid")
	}
	entries := make(map[string]*zip.File, len(archive.File))
	for _, entry := range archive.File {
		if _, duplicate := entries[entry.Name]; duplicate {
			return nil, fmt.Errorf("control recovery archive has duplicate entries")
		}
		entries[entry.Name] = entry
	}
	if len(entries) != 3 || entries[dumpEntryName] == nil || entries[manifestEntryName] == nil || entries[envelopeEntryName] == nil {
		return nil, fmt.Errorf("control recovery archive inventory is invalid")
	}
	manifestBytes, err := readBoundedZip(entries[manifestEntryName], maxManifestBytes)
	if err != nil {
		return nil, err
	}
	envelopeBytes, err := readBoundedZip(entries[envelopeEntryName], maxManifestBytes)
	if err != nil {
		return nil, err
	}
	manifest, err := verifyManifest(manifestBytes, envelopeBytes, trusted)
	if err != nil {
		return nil, err
	}
	section := manifest.Sections[0]
	if section.Size != int64(entries[dumpEntryName].UncompressedSize64) {
		return nil, fmt.Errorf("control recovery dump size differs from signed manifest")
	}
	reader, err := entries[dumpEntryName].Open()
	if err != nil {
		return nil, fmt.Errorf("control recovery dump open failed")
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, reader)
	closeErr := reader.Close()
	if copyErr != nil || closeErr != nil || written != section.Size || hex.EncodeToString(hash.Sum(nil)) != section.SHA256 {
		return nil, fmt.Errorf("control recovery dump integrity verification failed")
	}
	if missingRequiredKeys(manifest.RequiredSigningKeyIDs, availableKeyIDs) {
		return nil, fmt.Errorf("control recovery required signing key inventory is incomplete")
	}
	keepOpen = true
	return &VerifiedBundle{Manifest: manifest, archive: archive, dump: entries[dumpEntryName], file: file}, nil
}

func (b *VerifiedBundle) OpenDump() (io.ReadCloser, error) {
	if b == nil || b.dump == nil {
		return nil, fmt.Errorf("verified control recovery dump is unavailable")
	}
	return b.dump.Open()
}

func (b *VerifiedBundle) Close() error {
	if b == nil || b.file == nil {
		return nil
	}
	err := b.file.Close()
	b.file = nil
	b.dump = nil
	b.archive = nil
	return err
}

func loadManifestInventory(ctx context.Context, tx pgx.Tx, options CreateBundleOptions, now time.Time) (RecoveryManifest, error) {
	manifest := RecoveryManifest{
		Format:               recoveryBundleFormat,
		BundleID:             uuid.NewString(),
		CreatedAt:            now.Format(time.RFC3339Nano),
		Schema:               options.Schema,
		RelationshipContract: "norn.control-relationships/v1",
		RelationshipsValid:   true,
	}
	for _, migration := range inspectionCatalog {
		manifest.Catalog = append(manifest.Catalog, ManifestCatalogEntry{migration.version, migration.name, migration.checksum, migration.minimumReader, migration.minimumWriter})
	}
	for _, table := range InspectionRegistry() {
		var count int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{options.Schema, table.Name}.Sanitize()).Scan(&count); err != nil {
			return RecoveryManifest{}, fmt.Errorf("control recovery table inventory failed")
		}
		manifest.Tables = append(manifest.Tables, ManifestTableCount{Name: table.Name, Count: count})
	}
	if err := tx.QueryRow(ctx, `SELECT authority::text FROM `+pgx.Identifier{options.Schema, "control_plane_identity"}.Sanitize()+` WHERE singleton`).Scan(&manifest.Authority); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery authority inventory failed")
	}
	var databaseName, databaseOID, serverAddress string
	if err := tx.QueryRow(ctx, `SELECT current_database(),(SELECT oid::text FROM pg_database WHERE datname=current_database()),coalesce(inet_server_addr()::text,'local'),current_setting('server_version_num')`).Scan(&databaseName, &databaseOID, &serverAddress, &manifest.PostgreSQLVersion); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery database identity failed")
	}
	manifest.SourceDatabaseFingerprint = digestHex([]byte(databaseName + "\x00" + databaseOID + "\x00" + serverAddress))
	keyIDs, err := loadRequiredKeyIDs(ctx, tx, options.Schema)
	if err != nil {
		return RecoveryManifest{}, err
	}
	keyIDs = append(keyIDs, options.RequiredKeyIDs...)
	manifest.RequiredSigningKeyIDs = uniqueSorted(keyIDs)
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{options.Schema, "operation_effects"}.Sanitize()+` WHERE lifecycle IN ('reserved','launched')`).Scan(&manifest.UnresolvedEffects); err != nil {
		return RecoveryManifest{}, fmt.Errorf("control recovery unresolved effect inventory failed")
	}
	for _, recipient := range options.Recipients {
		stringer, ok := recipient.(fmt.Stringer)
		if !ok || stringer.String() == "" {
			return RecoveryManifest{}, fmt.Errorf("control recovery encryption recipient is not identifiable")
		}
		manifest.EncryptionRecipients = append(manifest.EncryptionRecipients, digestHex([]byte(stringer.String())))
	}
	manifest.EncryptionRecipients = uniqueSorted(manifest.EncryptionRecipients)
	return manifest, nil
}

func validateRelationships(ctx context.Context, tx pgx.Tx, schema string) error {
	q := func(table string) string { return pgx.Identifier{schema, table}.Sanitize() }
	query := `SELECT
		(SELECT count(*) FROM ` + q("operation_acceptance_intents") + ` i LEFT JOIN ` + q("operation_request_identities") + ` r ON r.id=i.request_identity_id LEFT JOIN ` + q("operations") + ` o ON o.id=i.operation_id LEFT JOIN ` + q("control_plane_identity") + ` c ON c.authority=r.authority AND c.singleton WHERE r.id IS NULL OR o.id IS NULL OR c.authority IS NULL OR r.operation_id<>i.operation_id OR r.fingerprint_version<>i.fingerprint_version OR r.fingerprint_digest<>i.fingerprint_digest) +
		(SELECT count(*) FROM ` + q("operation_request_identities") + ` r LEFT JOIN ` + q("operations") + ` o ON o.id=r.operation_id LEFT JOIN ` + q("control_plane_identity") + ` c ON c.authority=r.authority AND c.singleton WHERE o.id IS NULL OR c.authority IS NULL) +
		(SELECT count(*) FROM ` + q("operation_effects") + ` e LEFT JOIN ` + q("operations") + ` o ON o.id=e.operation_id LEFT JOIN ` + q("control_plane_identity") + ` c ON c.authority=e.authority AND c.singleton WHERE o.id IS NULL OR c.authority IS NULL) +
		(SELECT count(*) FROM ` + q("fleet_runner_attempts") + ` a LEFT JOIN ` + q("fleet_runner_attempts") + ` root ON root.id=a.root_attempt_id LEFT JOIN ` + q("fleet_runner_attempts") + ` retry ON retry.id=a.retry_of WHERE root.id IS NULL OR root.plan_id<>a.plan_id OR (a.retry_of<>'' AND (retry.id IS NULL OR retry.plan_id<>a.plan_id OR retry.attempt>=a.attempt))) +
		(SELECT count(*) FROM ` + q("deployment_regions") + ` r LEFT JOIN ` + q("deployments") + ` d ON d.id=r.deployment_id WHERE d.id IS NULL) +
		(SELECT count(*) FROM ` + q("deployment_steps") + ` s LEFT JOIN ` + q("deployments") + ` d ON d.id=s.deployment_id WHERE d.id IS NULL)`
	var broken int64
	if err := tx.QueryRow(ctx, query).Scan(&broken); err != nil {
		return fmt.Errorf("control recovery relationship validation failed")
	}
	if broken != 0 {
		return fmt.Errorf("control recovery relationship validation found broken references")
	}
	return nil
}

func loadRequiredKeyIDs(ctx context.Context, tx pgx.Tx, schema string) ([]string, error) {
	q := func(table string) string { return pgx.Identifier{schema, table}.Sanitize() }
	rows, err := tx.Query(ctx, `SELECT key_id FROM (
		SELECT signing_key_id AS key_id FROM `+q("operation_acceptance_intents")+`
		UNION SELECT key_id FROM `+q("mutation_audit_events")+` WHERE key_id<>''
		UNION SELECT key_id FROM `+q("mutation_audit_incidents")+` WHERE key_id<>''
		UNION SELECT payload->>'keyId' FROM `+q("operations")+` WHERE kind='release.qualification' AND payload->>'keyId'<>''
	) keys ORDER BY key_id`)
	if err != nil {
		return nil, fmt.Errorf("control recovery key inventory failed")
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("control recovery key inventory failed")
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

type countWriter struct {
	writer io.Writer
	count  int64
}

func (w *countWriter) Write(data []byte) (int, error) {
	written, err := w.writer.Write(data)
	w.count += int64(written)
	return written, err
}

func writeZipEntry(archive *zip.Writer, name string, data []byte) error {
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		return fmt.Errorf("control recovery archive entry creation failed")
	}
	if _, err := entry.Write(data); err != nil {
		return fmt.Errorf("control recovery archive entry write failed")
	}
	return nil
}

func readBoundedZip(entry *zip.File, limit int64) ([]byte, error) {
	if int64(entry.UncompressedSize64) > limit {
		return nil, fmt.Errorf("control recovery metadata exceeds limit")
	}
	reader, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("control recovery metadata open failed")
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, limit+1))
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func missingRequiredKeys(required, available []string) bool {
	known := make(map[string]struct{}, len(available))
	for _, key := range available {
		known[key] = struct{}{}
	}
	for _, key := range required {
		if _, ok := known[key]; !ok {
			return true
		}
	}
	return false
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type rowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func databaseFingerprint(ctx context.Context, queryer rowQueryer) (string, error) {
	var databaseName, databaseOID, serverAddress string
	if err := queryer.QueryRow(ctx, `SELECT current_database(),(SELECT oid::text FROM pg_database WHERE datname=current_database()),coalesce(inet_server_addr()::text,'local')`).Scan(&databaseName, &databaseOID, &serverAddress); err != nil {
		return "", err
	}
	return digestHex([]byte(databaseName + "\x00" + databaseOID + "\x00" + serverAddress)), nil
}
