package artifactstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// MaterializePrivate writes verified retained bytes to a new owner-only file.
// The caller owns the returned file and must remove it after use. An error
// removes the incomplete file; a successful return means Materialize verified
// the complete descriptor and the file contents were synced to disk.
func MaterializePrivate(ctx context.Context, objects Store, descriptor Descriptor, directory string) (string, error) {
	if objects == nil || descriptor.Validate() != nil || !filepath.IsAbs(directory) {
		return "", ErrInvalidDescriptor
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("materialization directory must be owner-only")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return "", fmt.Errorf("materialization directory must be owned by this process")
	}
	file, err := os.CreateTemp(directory, ".norn-artifact-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := objects.Materialize(ctx, descriptor, file); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	keep = true
	return path, nil
}
