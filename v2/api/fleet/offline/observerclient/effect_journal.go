package observerclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// FileEffectJournal is a local, owner-only, create-once transcript store.
// The caller supplies a dedicated pre-existing 0700 directory on durable
// storage. This implementation intentionally does not create it implicitly.
type FileEffectJournal struct {
	Directory     string
	beforePublish func() error // test fault; never configured by production callers
}

func openEffectJournalRoot(path string) (*os.Root, *os.File, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, err
	}
	dir, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	info, err := dir.Stat()
	if err != nil {
		dir.Close()
		root.Close()
		return nil, nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || owner.Uid != uint32(os.Geteuid()) {
		dir.Close()
		root.Close()
		return nil, nil, errors.New("effect journal directory is not owner-only")
	}
	return root, dir, nil
}

func lockEffectJournal(ctx context.Context, root *os.Root) (*os.File, error) {
	lock, err := root.OpenFile(".effect-journal.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := lock.Stat()
	if err != nil {
		lock.Close()
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || owner.Uid != uint32(os.Geteuid()) {
		lock.Close()
		return nil, errors.New("effect journal lock is unsafe")
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lock, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			lock.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			lock.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			lock.Close()
			return nil, errors.New("effect journal lock wait timed out")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// All cooperative writers hold the same OS lock. Only then can a new writer
// remove a temp left by a crashed writer without racing an active fsync.
func cleanupEffectJournalTemps(root *os.Root, dir *os.File) error {
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".effect-") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, ".effect-"), ".tmp")
		if uuid.Validate(id) != nil {
			continue
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		removed = true
	}
	if removed {
		return dir.Sync()
	}
	return nil
}

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
	root, dir, err := openEffectJournalRoot(j.Directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	defer dir.Close()
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
	root, dir, err := openEffectJournalRoot(j.Directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	defer dir.Close()
	lock, err := lockEffectJournal(ctx, root)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := cleanupEffectJournalTemps(root, dir); err != nil {
		return "", err
	}
	if _, err := root.Lstat(name); err == nil {
		return effectJournalExactReplay(root, dir, name, raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	temporary := ".effect-" + uuid.NewString() + ".tmp"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", err
	}
	defer root.Remove(temporary)
	_, writeErr := file.Write(raw)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return "", fmt.Errorf("effect journal write: %v; close: %v", writeErr, closeErr)
	}
	if j.beforePublish != nil {
		if err := j.beforePublish(); err != nil {
			return "", err
		}
	}
	// A hard link is an atomic, no-replace publication within this one
	// directory. The final name is never visible with incomplete bytes.
	if err := root.Link(temporary, name); errors.Is(err, os.ErrExist) {
		return effectJournalExactReplay(root, dir, name, raw)
	} else if err != nil {
		return "", err
	}
	if err := dir.Sync(); err != nil {
		return "", err
	}
	if err := root.Remove(temporary); err != nil {
		return "", err
	}
	if err := dir.Sync(); err != nil {
		return "", err
	}
	return digestEffectBytes(raw), nil
}

func effectJournalExactReplay(root *os.Root, dir *os.File, name string, raw []byte) (string, error) {
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
	if err := existing.Sync(); err != nil {
		return "", err
	}
	if err := dir.Sync(); err != nil {
		return "", err
	}
	return digestEffectBytes(raw), nil
}
