package archive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
)

// LocalStore is the Mini adapter: private files under an owner-only root,
// atomic no-replace publication (exclusive hard link of a fully synced
// temporary file), directory durability, verified duplicates and a hard
// capacity bound. Every filesystem operation goes through an os.Root, so no
// path (including a symlinked parent directory) can resolve outside the
// configured root. It never overwrites or deletes objects.
type LocalStore struct {
	root     *os.Root
	maxBytes int64
	mu       sync.Mutex
	// lock is an advisory whole-archive lock file (flock) serializing the
	// capacity check and publication across LocalStore instances and
	// processes sharing the root, not only goroutines of one instance.
	lock *os.File
}

// OpenLocal opens (creating if needed) an archive root. The root must be
// absolute, owner-only and owned by this process; maxBytes bounds the total
// stored bytes.
func OpenLocal(root string, maxBytes int64) (*LocalStore, error) {
	if !filepath.IsAbs(root) || maxBytes <= 0 {
		return nil, fmt.Errorf("archive root must be absolute with a positive capacity")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("archive root must be an owner-only directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("archive root must be owned by the archive process")
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	lock, err := confined.OpenFile(".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		confined.Close()
		return nil, fmt.Errorf("archive lock file: %w", err)
	}
	return &LocalStore{root: confined, maxBytes: maxBytes, lock: lock}, nil
}

// Close releases the root.
func (s *LocalStore) Close() error {
	_ = s.lock.Close()
	return s.root.Close()
}

// exclusive holds the in-process mutex and the cross-process flock.
func (s *LocalStore) exclusive() (func(), error) {
	s.mu.Lock()
	if err := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("archive lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		s.mu.Unlock()
	}, nil
}

// mkdirDurable creates each missing directory of name and fsyncs the parent
// of every directory it creates, so a published object's whole ancestor
// chain survives a crash, not only its leaf directory entry.
func (s *LocalStore) mkdirDurable(name string) error {
	if name == "." {
		return nil
	}
	current := "."
	for _, component := range strings.Split(name, "/") {
		next := path.Join(current, component)
		err := s.root.Mkdir(next, 0o700)
		switch {
		case err == nil:
			if err := s.syncDirectory(current); err != nil {
				return err
			}
		case errors.Is(err, fs.ErrExist):
			info, statErr := s.root.Lstat(next)
			if statErr != nil || !info.IsDir() {
				return fmt.Errorf("archive path component %s is not a directory", next)
			}
		default:
			return err
		}
		current = next
	}
	return nil
}

func objectName(key string) (string, error) {
	if !ValidKey(key) {
		return "", fmt.Errorf("invalid archive key")
	}
	return key, nil
}

// Capacity reports stored bytes against the configured bound.
func (s *LocalStore) Capacity(ctx context.Context) (int64, int64, error) {
	unlock, err := s.exclusive()
	if err != nil {
		return 0, 0, err
	}
	defer unlock()
	used, err := s.usage()
	return used, s.maxBytes, err
}

// usage sums stored bytes (temporary files included, conservatively).
func (s *LocalStore) usage() (int64, error) {
	var total int64
	err := fs.WalkDir(s.root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func (s *LocalStore) PutImmutable(ctx context.Context, key string, data []byte) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	name, err := objectName(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	sum := sha256.Sum256(data)
	info := ObjectInfo{Key: key, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	unlock, err := s.exclusive()
	if err != nil {
		return ObjectInfo{}, err
	}
	defer unlock()
	directory := path.Dir(name)
	if err := s.mkdirDurable(directory); err != nil {
		return ObjectInfo{}, fmt.Errorf("prepare archive directory: %w", err)
	}
	if existing, err := s.identity(name); err == nil {
		return info, verifyDuplicate(existing, info)
	} else if !errors.Is(err, ErrObjectNotFound) {
		return ObjectInfo{}, err
	}
	used, err := s.usage()
	if err != nil {
		return ObjectInfo{}, err
	}
	if used+int64(len(data)) > s.maxBytes {
		return ObjectInfo{}, fmt.Errorf("%w (%d of %d bytes used)", ErrArchiveFull, used, s.maxBytes)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return ObjectInfo{}, err
	}
	temporary := path.Join(directory, ".put-"+hex.EncodeToString(suffix))
	file, err := s.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("create archive object: %w", err)
	}
	defer s.root.Remove(temporary)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("write archive object: %w", err)
	}
	if err := s.root.Chmod(temporary, 0o400); err != nil {
		return ObjectInfo{}, err
	}
	if err := s.root.Link(temporary, name); err != nil {
		if errors.Is(err, fs.ErrExist) {
			existing, statErr := s.identity(name)
			if statErr != nil {
				return ObjectInfo{}, statErr
			}
			return info, verifyDuplicate(existing, info)
		}
		return ObjectInfo{}, fmt.Errorf("publish archive object: %w", err)
	}
	if err := s.syncDirectory(directory); err != nil {
		return ObjectInfo{}, fmt.Errorf("sync archive directory: %w", err)
	}
	return info, nil
}

// open opens an object without following a final symlink and without
// blocking, requiring a regular file.
func (s *LocalStore) open(name string) (*os.File, os.FileInfo, error) {
	file, err := s.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		file.Close()
		return nil, nil, ErrObjectCorrupt
	}
	return file, stat, nil
}

// identity hashes an existing object through one descriptor.
func (s *LocalStore) identity(name string) (ObjectInfo, error) {
	file, stat, err := s.open(name)
	if err != nil {
		return ObjectInfo{}, err
	}
	defer file.Close()
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, stat.Size()+1))
	if err != nil || read != stat.Size() {
		return ObjectInfo{}, ErrObjectCorrupt
	}
	return ObjectInfo{Key: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: read}, nil
}

func verifyDuplicate(existing, want ObjectInfo) error {
	if existing.SHA256 != want.SHA256 || existing.Size != want.Size {
		return ErrImmutableConflict
	}
	return nil
}

func (s *LocalStore) Get(ctx context.Context, key string, maxBytes int64) ([]byte, ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, ObjectInfo{}, err
	}
	name, err := objectName(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	file, stat, err := s.open(name)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	defer file.Close()
	if stat.Size() > maxBytes {
		return nil, ObjectInfo{}, ErrObjectTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ObjectInfo{}, ErrObjectTooLarge
	}
	sum := sha256.Sum256(data)
	return data, ObjectInfo{Key: key, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}, nil
}

func (s *LocalStore) Verify(ctx context.Context, expected ObjectInfo) error {
	name, err := objectName(expected.Key)
	if err != nil {
		return err
	}
	actual, err := s.identity(name)
	if err != nil {
		return err
	}
	if actual.SHA256 != expected.SHA256 || actual.Size != expected.Size {
		return ErrObjectCorrupt
	}
	return nil
}

func (s *LocalStore) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	err := fs.WalkDir(s.root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		if strings.HasPrefix(name, prefix) && ValidKey(name) {
			keys = append(keys, name)
		}
		return nil
	})
	sort.Strings(keys)
	return keys, err
}

func (s *LocalStore) syncDirectory(name string) error {
	directory, err := s.root.Open(name)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
