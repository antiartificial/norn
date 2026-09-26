package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var legacyImportFilename = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,240}\.dump$`)
var legacyImportTimestamp = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}$`)
var legacyExportDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// importLegacySnapshot retains the v2 flat namespace behind a claimed
// operation. A lost claim after download fails for inspection; a successor
// must not silently replace a file whose publication may have completed.
func importLegacySnapshot(ctx context.Context, objects SnapshotObjectStore, bucket, key, app, database, directory string) (string, error) {
	filename, claimed, err := legacySnapshotName(key, app, database)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", err
	}
	destination := filepath.Join(directory, filename)
	if claimed {
		private, err := os.MkdirTemp(directory, ".norn-legacy-import-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(private)
		manifestPath := filepath.Join(private, "manifest.json")
		if err := objects.GetObject(ctx, bucket, key+snapshotManifestSuffix, manifestPath); err != nil {
			return "", fmt.Errorf("download legacy export manifest: %w", err)
		}
		info, err := os.Lstat(manifestPath)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 4096 {
			return "", fmt.Errorf("legacy export manifest has invalid size or type: %v", err)
		}
		encoded, err := os.ReadFile(manifestPath)
		if err != nil {
			return "", err
		}
		var manifest legacyExportManifest
		if err := json.Unmarshal(encoded, &manifest); err != nil {
			return "", err
		}
		if manifest.Schema != "norn.snapshot-legacy-export/v1" || manifest.App != app || manifest.Database != database || manifest.Filename != filename || manifest.Size <= 0 || !legacyExportDigest.MatchString(manifest.SHA256) {
			return "", fmt.Errorf("legacy export manifest does not match this app and database")
		}
		staged := filepath.Join(private, "dump")
		if err := objects.GetObject(ctx, bucket, key, staged); err != nil {
			return "", fmt.Errorf("download legacy snapshot: %w", err)
		}
		if err := verifyRegularFileDigest(staged, manifest.SHA256, manifest.Size); err != nil {
			return "", fmt.Errorf("legacy snapshot differs from export manifest: %w", err)
		}
		if err := os.Link(staged, destination); err != nil {
			return "", fmt.Errorf("publish verified legacy snapshot: %w", err)
		}
		return filename, nil
	}
	if err := objects.GetObject(ctx, bucket, key, destination); err != nil {
		return "", fmt.Errorf("download legacy snapshot: %w", err)
	}
	info, err := os.Lstat(destination)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 {
		return "", fmt.Errorf("downloaded snapshot is not a private non-empty regular file")
	}
	return filename, nil
}

func legacySnapshotName(key, app, database string) (string, bool, error) {
	prefix := "snapshots/" + app + "/"
	if !strings.HasPrefix(key, prefix) {
		return "", false, fmt.Errorf("snapshot key belongs to another app")
	}
	filename := strings.TrimPrefix(key, prefix)
	claimed := false
	if strings.HasPrefix(filename, "operations/") {
		parts := strings.Split(filename, "/")
		if len(parts) != 3 {
			return "", false, fmt.Errorf("snapshot operation key has invalid shape")
		}
		if _, err := uuid.Parse(parts[1]); err != nil {
			return "", false, fmt.Errorf("snapshot operation key has invalid identity")
		}
		filename, claimed = parts[2], true
	}
	if filepath.Base(filename) != filename || !legacyImportFilename.MatchString(filename) || !strings.HasPrefix(filename, database+"_") {
		return "", false, fmt.Errorf("snapshot key does not name a safe legacy snapshot")
	}
	stem := strings.TrimSuffix(filename, ".dump")
	separator := strings.LastIndex(stem, "_")
	if separator < 0 || !legacyImportTimestamp.MatchString(stem[separator+1:]) {
		return "", false, fmt.Errorf("snapshot key has no valid timestamp")
	}
	if separator = strings.LastIndex(stem[:separator], "_"); separator < 0 || stem[:separator] != database {
		return "", false, fmt.Errorf("snapshot key has no matching database")
	}
	return filename, claimed, nil
}

// LegacySnapshotKeyName validates both historical flat keys and claimed
// operation export keys before an import is accepted by the HTTP producer.
func LegacySnapshotKeyName(key, app, database string) (string, error) {
	name, _, err := legacySnapshotName(key, app, database)
	return name, err
}
