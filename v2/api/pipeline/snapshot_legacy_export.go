package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

type legacyExportManifest struct {
	Schema     string    `json:"schema"`
	App        string    `json:"app"`
	Database   string    `json:"database"`
	Filename   string    `json:"filename"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	ExportedAt time.Time `json:"exportedAt"`
}

// exportLegacySnapshotClaimed retains the flat v1 local namespace while
// publishing a verified copy under a unique, create-only operation key.
func exportLegacySnapshotClaimed(ctx context.Context, objects snapshotCreateOnlyObjectStore, bucket, app, database, filename, directory, operationID string, operationStartedAt time.Time, journal *snapshotExportJournal) (string, error) {
	if _, err := uuid.Parse(operationID); err != nil {
		return "", fmt.Errorf("invalid snapshot export operation ID: %w", err)
	}
	if operationStartedAt.IsZero() {
		return "", fmt.Errorf("claimed snapshot export operation start time is unavailable")
	}
	if _, _, err := legacySnapshotName("snapshots/"+app+"/"+filename, app, database); err != nil {
		return "", err
	}
	private, err := os.MkdirTemp("", "norn-legacy-export-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(private)
	in, err := os.OpenFile(filepath.Join(directory, filename), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		_ = in.Close()
		return "", fmt.Errorf("legacy snapshot is not a non-empty regular file: %v", err)
	}
	copyPath := filepath.Join(private, "dump")
	out, err := os.OpenFile(copyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = in.Close()
		return "", err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(out, hash), in)
	if syncErr := out.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if closeErr := in.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil || size != info.Size() {
		return "", fmt.Errorf("copy pinned legacy snapshot: %v; copied %d of %d bytes", copyErr, size, info.Size())
	}
	manifest := legacyExportManifest{Schema: "norn.snapshot-legacy-export/v1", App: app, Database: database, Filename: filename,
		SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size, ExportedAt: operationStartedAt.UTC()}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	manifestPath := filepath.Join(private, "manifest.json")
	if err := writePrivateFile(manifestPath, encoded); err != nil {
		return "", err
	}
	key := strings.Join([]string{"snapshots", app, "operations", operationID, filename}, "/")
	if err := publishClaimedSnapshot(ctx, objects, bucket, key, private, copyPath, manifestPath, encoded, manifest.SHA256, manifest.Size, journal); err != nil {
		return "", err
	}
	return key, nil
}
