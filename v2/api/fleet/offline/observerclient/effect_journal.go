package observerclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"
)

// FileEffectJournal is a local, owner-only, create-once transcript store.
// The caller supplies a dedicated pre-existing 0700 directory on durable
// storage. This implementation intentionally does not create it implicitly.
type FileEffectJournal struct{ Directory string }

func effectJournalName(key string) (string, error) {
	base := key
	if len(base) > 7 && base[len(base)-7:] == ".signed" {
		base = base[:len(base)-7]
	}
	if uuid.Validate(base) != nil {
		return "", errors.New("invalid effect journal key")
	}
	return key + ".json", nil
}

func (j FileEffectJournal) Read(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := effectJournalName(key)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(j.Directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("effect journal directory is not owner-only")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("effect journal directory has a foreign owner")
	}
	root, err := os.OpenRoot(j.Directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := stat.Sys().(*syscall.Stat_t)
	if !stat.Mode().IsRegular() || stat.Mode().Perm() != 0600 || stat.Size() == 0 || stat.Size() > 36<<20 || !ok || st.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("effect journal record is unsafe")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 36<<20+1))
	if err != nil || len(raw) == 0 || len(raw) > 36<<20 {
		return nil, errors.New("effect journal read failed")
	}
	return raw, nil
}

func (j FileEffectJournal) WriteOnce(ctx context.Context, key string, raw []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name, keyErr := effectJournalName(key)
	if keyErr != nil || len(raw) == 0 || len(raw) > 36<<20 {
		return "", errors.New("invalid effect journal record")
	}
	info, err := os.Lstat(j.Directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("effect journal directory is not owner-only")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) {
		return "", errors.New("effect journal directory has a foreign owner")
	}
	root, err := os.OpenRoot(j.Directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if readErr != nil {
			return "", readErr
		}
		defer existing.Close()
		stat, statErr := existing.Stat()
		if statErr != nil {
			return "", statErr
		}
		owner, ownerOK := stat.Sys().(*syscall.Stat_t)
		if !stat.Mode().IsRegular() || stat.Mode().Perm() != 0600 || stat.Size() > 36<<20 || !ownerOK || owner.Uid != uint32(os.Geteuid()) {
			return "", errors.New("effect journal record is unsafe")
		}
		stored, readErr := io.ReadAll(io.LimitReader(existing, 36<<20+1))
		if readErr != nil || !bytes.Equal(stored, raw) {
			return "", errors.New("effect journal replay differs")
		}
		return digestEffectBytes(raw), nil
	}
	if err != nil {
		return "", err
	}
	_, writeErr := file.Write(raw)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return "", fmt.Errorf("effect journal write: %v; close: %v", writeErr, closeErr)
	}
	// Sync the directory entry as well as the bytes before returning a digest
	// that may be embedded in a signed attestation.
	dir, err := os.Open(filepath.Clean(j.Directory))
	if err != nil {
		return "", err
	}
	syncErr, closeErr := dir.Sync(), dir.Close()
	if syncErr != nil || closeErr != nil {
		return "", fmt.Errorf("effect journal directory sync: %v; close: %v", syncErr, closeErr)
	}
	return digestEffectBytes(raw), nil
}
