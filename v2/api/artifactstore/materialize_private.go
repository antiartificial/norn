package artifactstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type boundedMaterializationWriter struct {
	file  io.Writer
	hash  hash.Hash
	limit int64
	size  int64
}

func (w *boundedMaterializationWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.size {
		return 0, ErrArtifactCorrupt
	}
	n, err := w.file.Write(p)
	if n > 0 {
		w.size += int64(n)
		_, _ = w.hash.Write(p[:n])
	}
	return n, err
}

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
	lock, err := os.OpenFile(filepath.Join(directory, ".norn-materialize.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// Reject an artifact that cannot fit before opening a private output file.
	// The directory lock serializes Norn materializations here, but unrelated
	// filesystem writes can still exhaust the volume while bytes are streaming.
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(directory, &filesystem); err != nil {
		return "", err
	}
	if filesystem.Bsize <= 0 || filesystem.Bavail < (uint64(descriptor.Size)+uint64(filesystem.Bsize)-1)/uint64(filesystem.Bsize) {
		return "", ErrArtifactFull
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
	verified := &boundedMaterializationWriter{file: file, hash: sha256.New(), limit: descriptor.Size}
	if err := objects.Materialize(ctx, descriptor, verified); err != nil {
		_ = file.Close()
		return "", err
	}
	if verified.size != descriptor.Size || hex.EncodeToString(verified.hash.Sum(nil)) != descriptor.SHA256 {
		_ = file.Close()
		return "", ErrArtifactCorrupt
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
