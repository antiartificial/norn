// norn-postgres-fence-preflight performs a read-only provider qualification
// check. Its catalog is an explicitly pinned private snapshot, not a claim
// that the catalog is still active or that a later fence effect will succeed.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"norn/v2/api/database"
)

const maxPrivateInput = 1 << 20

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "PostgreSQL fence preflight:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("norn-postgres-fence-preflight", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	catalogPath := flags.String("catalog-file", "", "owner-only catalog snapshot")
	catalogSHA := flags.String("catalog-sha256", "", "exact SHA-256 of catalog file bytes")
	targetPath := flags.String("expected-target-file", "", "owner-only exact target identity")
	profile := flags.String("profile", "", "deployment profile ID")
	logical := flags.String("logical-resource", "", "application logical database ID")
	secretDir := flags.String("secrets-dir", "", "owner-only secret material directory")
	if err := flags.Parse(arguments); err != nil || len(flags.Args()) != 0 || *catalogPath == "" || *catalogSHA == "" ||
		*targetPath == "" || *profile == "" || *logical == "" || *secretDir == "" {
		return errors.New("complete catalog, target, profile, logical resource and secret selection is required")
	}
	catalogBytes, err := readPrivateInput(*catalogPath)
	if err != nil {
		return errors.New("catalog snapshot must be an owner-only regular file")
	}
	digest := sha256.Sum256(catalogBytes)
	if *catalogSHA != hex.EncodeToString(digest[:]) {
		return errors.New("catalog snapshot differs from the pinned SHA-256")
	}
	var catalog database.Catalog
	if err := decodeStrictJSON(catalogBytes, &catalog); err != nil {
		return errors.New("catalog snapshot is invalid")
	}
	targetBytes, err := readPrivateInput(*targetPath)
	if err != nil {
		return errors.New("expected target must be an owner-only regular file")
	}
	var expected database.TargetIdentity
	if err := decodeStrictJSON(targetBytes, &expected); err != nil {
		return errors.New("expected target is invalid")
	}
	resolver, err := database.NewResolver(catalog)
	if err != nil {
		return fmt.Errorf("catalog snapshot fails validation: %w", err)
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: *profile,
		Purpose: database.PurposeApplication, LogicalResourceID: *logical, Expected: &expected})
	if err != nil || resolved.Target != expected || resolved.Target.Engine != database.EnginePostgreSQL {
		return errors.New("selected application PostgreSQL target differs from the exact expected identity")
	}
	secrets, err := database.NewDirectorySecretSource(*secretDir)
	if err != nil {
		return errors.New("database secret directory is unavailable")
	}
	defer secrets.Close()
	preflight, err := database.PreflightPostgresRuntimeRoleFenceForCutover(ctx, resolved, expected, secrets)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		CatalogSHA256   string                  `json:"catalogSha256"`
		Target          database.TargetIdentity `json:"target"`
		FenceGeneration uint64                  `json:"fenceGeneration"`
		ServerVersion   int                     `json:"serverVersion"`
		CanLogin        bool                    `json:"canLogin"`
		Sessions        int                     `json:"sessions"`
	}{hex.EncodeToString(digest[:]), preflight.Target, preflight.FenceGeneration,
		preflight.ServerVersion, preflight.CanLogin, preflight.Sessions})
}

func readPrivateInput(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("private input path is not absolute")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxPrivateInput {
		return nil, errors.New("private input is not an owner-only bounded regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("private input is not owned by this user")
	}
	return io.ReadAll(io.LimitReader(file, maxPrivateInput+1))
}

func decodeStrictJSON(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > maxPrivateInput {
		return errors.New("JSON input is empty or oversized")
	}
	keys := json.NewDecoder(bytes.NewReader(raw))
	if err := rejectDuplicateKeys(keys); err != nil {
		return err
	}
	if _, err := keys.Token(); err != io.EOF {
		return errors.New("JSON input has trailing content")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("JSON input has trailing content")
	}
	return nil
}

func rejectDuplicateKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("JSON input has a repeated key")
			}
			seen[name] = true
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("JSON input has an invalid delimiter")
	}
	_, err = decoder.Token()
	return err
}
