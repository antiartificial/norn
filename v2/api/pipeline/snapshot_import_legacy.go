package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var legacyImportFilename = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,240}\.dump$`)
var legacyImportTimestamp = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}$`)

// importLegacySnapshot retains the v2 flat namespace behind a claimed
// operation. A lost claim after download fails for inspection; a successor
// must not silently replace a file whose publication may have completed.
func importLegacySnapshot(ctx context.Context, objects SnapshotObjectStore, bucket, key, app, database, directory string) (string, error) {
	prefix := "snapshots/" + app + "/"
	if !strings.HasPrefix(key, prefix) {
		return "", fmt.Errorf("snapshot import key belongs to another app")
	}
	filename := strings.TrimPrefix(key, prefix)
	if filepath.Base(filename) != filename || !legacyImportFilename.MatchString(filename) || !strings.HasPrefix(filename, database+"_") {
		return "", fmt.Errorf("snapshot import key does not name a safe legacy snapshot")
	}
	stem := strings.TrimSuffix(filename, ".dump")
	separator := strings.LastIndex(stem, "_")
	if separator < 0 || !legacyImportTimestamp.MatchString(stem[separator+1:]) {
		return "", fmt.Errorf("snapshot import key has no valid timestamp")
	}
	if separator = strings.LastIndex(stem[:separator], "_"); separator < 0 || stem[:separator] != database {
		return "", fmt.Errorf("snapshot import key has no matching database")
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", err
	}
	destination := filepath.Join(directory, filename)
	if err := objects.GetObject(ctx, bucket, key, destination); err != nil {
		return "", fmt.Errorf("download legacy snapshot: %w", err)
	}
	info, err := os.Lstat(destination)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 {
		return "", fmt.Errorf("downloaded snapshot is not a private non-empty regular file")
	}
	return filename, nil
}
