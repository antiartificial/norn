// Command norn-control-recovery is the offline PostgreSQL control inspection
// and same-backend recovery tool. It never contacts the Norn API or queue.
//
//	inspect          redacted, non-restorable JSON to a new mode-0600 file
//	bundle-create    age-encrypted pg_dump plus signed manifest, published atomically
//	bundle-verify    decrypt, verify manifest signature, dump digest and key inventory
//	restore-passive  verify, pg_restore --single-transaction into an explicitly
//	                 --isolated-target, then read-only validation and signature checks
//	archive-verify   verify every evidence archive object offline (no database)
//	archive-reindex  rebuild a control schema's evidence index from the archive
//
// Database URLs, age identities and the manifest signing key are read from
// regular, non-symlink, owner-only files. --recovery-keys-file is an age file
// encrypted to the bundle identity whose plaintext is the recovery key JSON
// documented on controlrecovery.RecoveryKeyMaterial:
//
//	{"hmacKeys":["<unpadded base64 HMAC key bytes>"],"qualificationPublicKeys":["<unpadded base64 Ed25519 key or PEM>"]}
//
// restore-passive is inert: it starts no workers, schedules, sessions or
// effect executors, and copied leases/owners/runner attempts grant nothing.
// Its report is always activationReady=false and lists unresolved effects.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"filippo.io/age"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"

	"norn/v2/api/archive"
	"norn/v2/api/controlrecovery"
	"norn/v2/api/retention"
	"norn/v2/api/store"
)

const maxPrivateInputBytes = 8 << 20

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		return fmt.Errorf("usage: norn-control-recovery <inspect|bundle-create|bundle-verify|restore-passive|archive-verify|archive-reindex>")
	}
	switch arguments[0] {
	case "inspect":
		flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
		dsnFile := flags.String("database-url-file", "", "mode-0600 PostgreSQL URL file")
		schema := flags.String("schema", "public", "control schema")
		output := flags.String("output", "", "new inspection JSON path")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		dsn, err := readPrivateText(*dsnFile)
		if err != nil {
			return err
		}
		pool, err := openControlPool(ctx, dsn)
		if err != nil {
			return fmt.Errorf("control recovery database connection failed")
		}
		defer pool.Close()
		return writeNewAtomic(*output, func(writer io.Writer) error {
			return controlrecovery.ExportInspection(ctx, pool, *schema, writer)
		})
	case "bundle-create":
		flags := flag.NewFlagSet("bundle-create", flag.ContinueOnError)
		dsnFile := flags.String("database-url-file", "", "mode-0600 PostgreSQL URL file")
		schema := flags.String("schema", "public", "control schema")
		output := flags.String("output", "", "new encrypted bundle path")
		recipientsFile := flags.String("recipients-file", "", "age recipients file")
		signingKeyFile := flags.String("manifest-signing-key-file", "", "mode-0600 Ed25519 private key file")
		pgDump := flags.String("pg-dump", "pg_dump", "pg_dump binary")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		dsn, err := readPrivateText(*dsnFile)
		if err != nil {
			return err
		}
		recipientBytes, err := readBoundedRegularFile(*recipientsFile, maxPrivateInputBytes)
		if err != nil {
			return fmt.Errorf("read age recipients failed")
		}
		recipients, err := age.ParseRecipients(strings.NewReader(string(recipientBytes)))
		if err != nil || len(recipients) == 0 {
			return fmt.Errorf("age recipients are invalid")
		}
		signingKey, err := readPrivateBytes(*signingKeyFile)
		if err != nil {
			return err
		}
		signer, err := controlrecovery.NewManifestSigner(signingKey)
		if err != nil {
			return err
		}
		pool, err := openControlPool(ctx, dsn)
		if err != nil {
			return fmt.Errorf("control recovery database connection failed")
		}
		defer pool.Close()
		manifest, err := controlrecovery.CreateBundle(ctx, controlrecovery.CreateBundleOptions{Pool: pool, DatabaseURL: dsn, Schema: *schema, OutputPath: *output, Recipients: recipients, Signer: signer, PGDumpPath: *pgDump})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(manifest)
	case "bundle-verify", "restore-passive":
		flags := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
		bundlePath := flags.String("bundle", "", "encrypted bundle path")
		identityFile := flags.String("identity-file", "", "mode-0600 age identity file")
		publicKeyFile := flags.String("manifest-public-key-file", "", "trusted Ed25519 public key")
		keyIDsFile := flags.String("available-key-ids-file", "", "newline-separated independently recovered signing key IDs")
		recoveryKeysFile := flags.String("recovery-keys-file", "", "age-encrypted recovery verification keys")
		targetDSNFile := flags.String("target-database-url-file", "", "mode-0600 isolated target PostgreSQL URL")
		pgRestore := flags.String("pg-restore", "pg_restore", "pg_restore binary")
		isolated := flags.Bool("isolated-target", false, "confirm target is isolated and mutation-disabled")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		identityBytes, err := readPrivateBytes(*identityFile)
		if err != nil {
			return err
		}
		identities, err := age.ParseIdentities(strings.NewReader(string(identityBytes)))
		if err != nil || len(identities) == 0 {
			return fmt.Errorf("age identities are invalid")
		}
		publicBytes, err := readBoundedRegularFile(*publicKeyFile, maxPrivateInputBytes)
		if err != nil {
			return fmt.Errorf("read manifest public key failed")
		}
		public, err := controlrecovery.ParseManifestPublicKey(publicBytes)
		if err != nil {
			return err
		}
		keyIDs, err := readLines(*keyIDsFile)
		if err != nil {
			return err
		}
		var evidence *controlrecovery.RecoveryKeyMaterial
		if *recoveryKeysFile != "" {
			encryptedKeys, err := readBoundedRegularFile(*recoveryKeysFile, maxPrivateInputBytes)
			if err != nil {
				return fmt.Errorf("read encrypted recovery keys failed")
			}
			decryptedKeys, err := age.Decrypt(bytes.NewReader(encryptedKeys), identities...)
			if err != nil {
				return fmt.Errorf("decrypt recovery keys failed")
			}
			plainKeys, err := io.ReadAll(io.LimitReader(decryptedKeys, maxPrivateInputBytes+1))
			if err != nil || len(plainKeys) > maxPrivateInputBytes {
				return fmt.Errorf("recovery keys are unreadable or too large")
			}
			evidence, err = controlrecovery.ParseRecoveryKeyMaterial(plainKeys)
			if err != nil {
				return err
			}
			keyIDs = append(keyIDs, evidence.KeyIDs()...)
		}
		if arguments[0] == "bundle-verify" {
			bundle, err := controlrecovery.VerifyBundle(*bundlePath, identities, []ed25519.PublicKey{public}, keyIDs)
			if err != nil {
				return err
			}
			defer bundle.Close()
			return json.NewEncoder(os.Stdout).Encode(bundle.Manifest)
		}
		dsn, err := readPrivateText(*targetDSNFile)
		if err != nil {
			return err
		}
		pool, err := openControlPool(ctx, dsn)
		if err != nil {
			return fmt.Errorf("control recovery target connection failed")
		}
		defer pool.Close()
		if evidence == nil {
			return fmt.Errorf("restore-passive requires --recovery-keys-file")
		}
		report, err := controlrecovery.RestorePassive(ctx, controlrecovery.RestoreOptions{BundlePath: *bundlePath, Identities: identities, TrustedKeys: []ed25519.PublicKey{public}, AvailableKeyIDs: keyIDs, TargetPool: pool, TargetDatabaseURL: dsn, IsolatedTarget: *isolated, PGRestorePath: *pgRestore, EvidenceVerifier: evidence})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	case "archive-verify", "archive-reindex":
		return runArchiveCommand(ctx, arguments[0], arguments[1:])
	default:
		return fmt.Errorf("unknown control recovery command")
	}
}

// runArchiveCommand recovers the evidence archive without the API:
//
//	archive-verify   read and verify every evidence object (bounded reads,
//	                 bundle integrity, subject-derived keys, acceptance
//	                 content binding; signatures with --recovery-keys-file).
//	                 Needs no control database. Exits non-zero on any
//	                 rejected object after printing the report.
//	archive-reindex  rebuild the evidence index of a control schema from the
//	                 archive alone (restore without historical PostgreSQL);
//	                 existing index rows are never overwritten.
func runArchiveCommand(ctx context.Context, command string, arguments []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	settings := archive.Settings{}
	flags.StringVar(&settings.Backend, "archive-backend", "", "local or object")
	flags.StringVar(&settings.Dir, "archive-dir", "", "local archive root (absolute, owner-only)")
	flags.Int64Var(&settings.MaxBytes, "archive-max-bytes", 10<<30, "local archive capacity")
	flags.StringVar(&settings.Endpoint, "archive-endpoint", "", "object archive endpoint host[:port]")
	flags.StringVar(&settings.Bucket, "archive-bucket", "", "object archive bucket")
	flags.StringVar(&settings.Prefix, "archive-prefix", "", "object archive key prefix")
	flags.StringVar(&settings.Region, "archive-region", "", "object archive region")
	flags.StringVar(&settings.AccessKeyFile, "archive-access-key-file", "", "owner-only archive access key file")
	flags.StringVar(&settings.SecretKeyFile, "archive-secret-key-file", "", "owner-only archive secret key file")
	flags.StringVar(&settings.CAFile, "archive-ca-file", "", "owner-only CA bundle for the object archive")
	identityFile := flags.String("identity-file", "", "mode-0600 age identity file (with --recovery-keys-file)")
	recoveryKeysFile := flags.String("recovery-keys-file", "", "age-encrypted recovery verification keys (verifies acceptance signatures)")
	dsnFile := flags.String("database-url-file", "", "mode-0600 PostgreSQL URL file (archive-reindex)")
	schema := flags.String("schema", "public", "control schema (archive-reindex)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	var signer store.AcceptanceSigner
	if *recoveryKeysFile != "" {
		material, err := loadRecoveryKeys(*identityFile, *recoveryKeysFile)
		if err != nil {
			return err
		}
		if signer, err = material.AcceptanceSigner(); err != nil {
			return err
		}
	}
	objects, closer, err := archive.OpenStore(ctx, settings)
	if err != nil {
		return fmt.Errorf("open evidence archive: %w", err)
	}
	defer closer.Close()
	if command == "archive-verify" {
		report, err := retention.VerifyArchive(ctx, objects, signer)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			return err
		}
		if len(report.Rejected) > 0 {
			return fmt.Errorf("%d evidence archive objects failed verification", len(report.Rejected))
		}
		return nil
	}
	dsn, err := readPrivateText(*dsnFile)
	if err != nil {
		return err
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("control recovery database URL is invalid")
	}
	store.DeclareReaderContract(config, "control-recovery")
	config.ConnConfig.RuntimeParams["search_path"] = *schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("control recovery database connection failed")
	}
	defer pool.Close()
	recovery, err := retention.RestoreIndex(ctx, &store.DB{Pool: pool}, objects, signer)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(recovery); err != nil {
		return err
	}
	if len(recovery.Rejected) > 0 {
		return fmt.Errorf("%d evidence archive objects were rejected and not indexed", len(recovery.Rejected))
	}
	return nil
}

// loadRecoveryKeys decrypts the age-encrypted recovery key document.
func loadRecoveryKeys(identityFile, recoveryKeysFile string) (*controlrecovery.RecoveryKeyMaterial, error) {
	identityBytes, err := readPrivateBytes(identityFile)
	if err != nil {
		return nil, err
	}
	identities, err := age.ParseIdentities(strings.NewReader(string(identityBytes)))
	if err != nil || len(identities) == 0 {
		return nil, fmt.Errorf("age identities are invalid")
	}
	encryptedKeys, err := readBoundedRegularFile(recoveryKeysFile, maxPrivateInputBytes)
	if err != nil {
		return nil, fmt.Errorf("read encrypted recovery keys failed")
	}
	decryptedKeys, err := age.Decrypt(bytes.NewReader(encryptedKeys), identities...)
	if err != nil {
		return nil, fmt.Errorf("decrypt recovery keys failed")
	}
	plainKeys, err := io.ReadAll(io.LimitReader(decryptedKeys, maxPrivateInputBytes+1))
	if err != nil || len(plainKeys) > maxPrivateInputBytes {
		return nil, fmt.Errorf("recovery keys are unreadable or too large")
	}
	return controlrecovery.ParseRecoveryKeyMaterial(plainKeys)
}

// openControlPool opens a control-store pool that declares this binary's
// archive-aware reader contract (see store.DeclareReaderContract).
func openControlPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	store.DeclareReaderContract(config, "control-recovery")
	return pgxpool.NewWithConfig(ctx, config)
}

func readPrivateText(path string) (string, error) {
	data, err := readPrivateBytes(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("private input file is empty")
	}
	return value, nil
}

func readPrivateBytes(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("private input file must be a regular mode-0600 file")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("private input file must be a regular mode-0600 file")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	stat, owned := infoSysStat(info)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o400 == 0 || info.Mode().Perm()&0o100 != 0 || !owned || stat.Uid != uint32(unix.Geteuid()) {
		return nil, fmt.Errorf("private input file must be a regular mode-0600 file owned by the current user")
	}
	if info.Size() > maxPrivateInputBytes {
		return nil, fmt.Errorf("private input file is too large")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPrivateInputBytes+1))
	if err != nil || len(data) > maxPrivateInputBytes {
		return nil, fmt.Errorf("read private input failed")
	}
	return data, nil
}

func infoSysStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

func readLines(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	data, err := readBoundedRegularFile(path, maxPrivateInputBytes)
	if err != nil {
		return nil, fmt.Errorf("read available key IDs failed")
	}
	var result []string
	for _, value := range strings.Split(string(data), "\n") {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result, nil
}

// readBoundedRegularFile reads public inputs (recipients, public keys, key
// IDs, encrypted key documents) through one non-blocking descriptor so a FIFO
// or device cannot stall the tool before the regular-file and size checks.
func readBoundedRegularFile(path string, limit int64) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("input path is required")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("input is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("input is unreadable or too large")
	}
	return data, nil
}

func writeNewAtomic(path string, write func(io.Writer) error) error {
	if path == "" {
		return fmt.Errorf("inspection destination is required")
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".norn-control-inspection-*.tmp")
	if err != nil {
		return fmt.Errorf("inspection destination staging failed")
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("inspection destination permission failed")
	}
	if err := write(temporary); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("inspection destination sync failed")
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("inspection destination close failed")
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return fmt.Errorf("inspection destination must be a new file")
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("inspection staging cleanup failed")
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("inspection destination directory open failed")
	}
	syncErr := directoryHandle.Sync()
	closeErr := directoryHandle.Close()
	if syncErr != nil || closeErr != nil {
		return fmt.Errorf("inspection destination directory sync failed")
	}
	return nil
}
