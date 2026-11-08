package secrets

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

const maxEncryptedSnapshotBytes = 1 << 20

var appNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// EncryptedSnapshot is an exact copy of the SOPS ciphertext file. It contains
// no decrypted values; callers must keep Ciphertext private and should seal it
// before storing it outside the app directory.
type EncryptedSnapshot struct {
	Digest     string
	Ciphertext []byte
}

// SnapshotEncrypted returns the exact encrypted input selected at this read.
// It rejects mutable or weakly protected filesystem entries so a caller can
// bind the returned bytes to a durable private operation record.
func (m *Manager) SnapshotEncrypted(appID string) (EncryptedSnapshot, error) {
	if m == nil || m.appsDir == "" || !appNamePattern.MatchString(appID) {
		return EncryptedSnapshot{}, fmt.Errorf("encrypted secret snapshot target is invalid")
	}
	appsInfo, err := os.Lstat(m.appsDir)
	if err != nil || !appsInfo.IsDir() || appsInfo.Mode().Perm()&0o022 != 0 {
		return EncryptedSnapshot{}, fmt.Errorf("encrypted secret snapshot root must be a non-writable directory")
	}
	appsStat, ok := appsInfo.Sys().(*syscall.Stat_t)
	if !ok || appsStat.Uid != uint32(os.Geteuid()) {
		return EncryptedSnapshot{}, fmt.Errorf("encrypted secret snapshot root must be owned by this user")
	}
	appDir := filepath.Join(m.appsDir, appID)
	appInfo, err := os.Lstat(appDir)
	if err != nil || !appInfo.IsDir() || appInfo.Mode().Perm()&0o022 != 0 {
		return EncryptedSnapshot{}, fmt.Errorf("encrypted secret snapshot app directory is unavailable")
	}
	appStat, ok := appInfo.Sys().(*syscall.Stat_t)
	if !ok || appStat.Uid != uint32(os.Geteuid()) {
		return EncryptedSnapshot{}, fmt.Errorf("encrypted secret snapshot app directory must be owned by this user")
	}
	path := m.SecretFile(appID)
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return EncryptedSnapshot{}, fmt.Errorf("encrypted secret snapshot is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > maxEncryptedSnapshotBytes {
		return EncryptedSnapshot{}, fmt.Errorf("encrypted secret snapshot must be a bounded owner-only regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return EncryptedSnapshot{}, fmt.Errorf("encrypted secret snapshot must be owned by this user")
	}
	ciphertext, err := io.ReadAll(io.LimitReader(file, maxEncryptedSnapshotBytes+1))
	if err != nil || len(ciphertext) == 0 || len(ciphertext) > maxEncryptedSnapshotBytes {
		return EncryptedSnapshot{}, fmt.Errorf("encrypted secret snapshot could not be read")
	}
	digest := sha256.Sum256(ciphertext)
	return EncryptedSnapshot{Digest: "sha256:" + hex.EncodeToString(digest[:]), Ciphertext: ciphertext}, nil
}
