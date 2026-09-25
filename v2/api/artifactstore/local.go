package artifactstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const streamBufferBytes = 128 << 10

// LocalStore keeps artifacts under an owner-only root. It is private and
// node-local: it is useful for a bounded first implementation, but does not
// establish off-host durability.
type LocalStore struct {
	root     *os.Root
	capacity int64
	lock     *os.File
	mu       sync.Mutex
}

// OpenLocal opens a private, bounded local artifact root. The capacity is a
// total stored-byte bound and is separate from MaxArtifactBytes.
func OpenLocal(root string, capacity int64) (*LocalStore, error) {
	if !filepath.IsAbs(root) || capacity <= 0 {
		return nil, fmt.Errorf("artifact root must be absolute with a positive capacity")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("artifact root must be an owner-only directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("artifact root must be owned by the artifact process")
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	lock, err := confined.OpenFile(".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		_ = confined.Close()
		return nil, fmt.Errorf("artifact lock file: %w", err)
	}
	return &LocalStore{root: confined, capacity: capacity, lock: lock}, nil
}

func (s *LocalStore) Close() error {
	if s.lock != nil {
		_ = s.lock.Close()
	}
	return s.root.Close()
}

func (s *LocalStore) exclusive() (func(), error) {
	s.mu.Lock()
	if err := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("artifact lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		s.mu.Unlock()
	}, nil
}

// Publish stages a bounded stream, syncs it, and atomically links it into its
// content-bound name. The whole publish is locked across processes sharing a
// root, which makes the capacity check and no-replace commit one operation.
func (s *LocalStore) Publish(ctx context.Context, expected Descriptor, source io.Reader) (Descriptor, error) {
	if err := expected.Validate(); err != nil {
		return Descriptor{}, err
	}
	if err := ctx.Err(); err != nil {
		return Descriptor{}, err
	}
	unlock, err := s.exclusive()
	if err != nil {
		return Descriptor{}, err
	}
	defer unlock()

	if err := s.mkdirDurable(path.Dir(expected.Key)); err != nil {
		return Descriptor{}, err
	}
	if existing, err := s.identity(expected.Key); err == nil {
		if existing != expected {
			return Descriptor{}, ErrArtifactCorrupt
		}
		if _, err := copyVerified(ctx, io.Discard, source, expected); err != nil {
			return Descriptor{}, err
		}
		return expected, nil
	} else if !errors.Is(err, ErrArtifactNotFound) {
		return Descriptor{}, err
	}

	used, err := s.usage()
	if err != nil {
		return Descriptor{}, err
	}
	if used > s.capacity-expected.Size {
		return Descriptor{}, fmt.Errorf("%w (%d of %d bytes used)", ErrArtifactFull, used, s.capacity)
	}
	if err := s.mkdirDurable(".staging"); err != nil {
		return Descriptor{}, err
	}
	temporary, err := randomStagingName()
	if err != nil {
		return Descriptor{}, err
	}
	file, err := s.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return Descriptor{}, fmt.Errorf("create artifact staging file: %w", err)
	}
	defer s.root.Remove(temporary)
	actual, copyErr := copyVerified(ctx, file, source, expected)
	if copyErr == nil {
		copyErr = file.Sync()
	}
	if closeErr := file.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return Descriptor{}, copyErr
	}
	if actual != expected {
		return Descriptor{}, ErrArtifactCorrupt
	}
	if err := s.root.Chmod(temporary, 0o400); err != nil {
		return Descriptor{}, err
	}
	if err := s.root.Link(temporary, expected.Key); err != nil {
		if errors.Is(err, fs.ErrExist) {
			existing, identityErr := s.identity(expected.Key)
			if identityErr != nil || existing != expected {
				return Descriptor{}, ErrArtifactCorrupt
			}
			return expected, nil
		}
		return Descriptor{}, fmt.Errorf("publish artifact: %w", err)
	}
	if err := s.syncDirectory(path.Dir(expected.Key)); err != nil {
		return Descriptor{}, fmt.Errorf("sync artifact directory: %w", err)
	}
	return expected, nil
}

func randomStagingName() (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return path.Join(".staging", hex.EncodeToString(nonce)), nil
}

func (s *LocalStore) Open(ctx context.Context, expected Descriptor) (io.ReadCloser, error) {
	if err := expected.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, stat, err := s.open(expected.Key)
	if err != nil {
		return nil, err
	}
	if stat.Size() != expected.Size || stat.Size() > MaxArtifactBytes {
		_ = file.Close()
		return nil, ErrArtifactCorrupt
	}
	return &verifyingReadCloser{reader: contextReader{ctx: ctx, reader: file}, closer: file, expected: expected, hash: sha256.New()}, nil
}

func (s *LocalStore) Materialize(ctx context.Context, expected Descriptor, destination io.Writer) error {
	reader, err := s.Open(ctx, expected)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyBuffer(destination, reader, make([]byte, streamBufferBytes))
	closeErr := reader.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (s *LocalStore) Verify(ctx context.Context, expected Descriptor) error {
	return s.Materialize(ctx, expected, io.Discard)
}

func (s *LocalStore) open(name string) (*os.File, os.FileInfo, error) {
	file, err := s.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, ErrArtifactNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrArtifactCorrupt
	}
	return file, stat, nil
}

func (s *LocalStore) identity(name string) (Descriptor, error) {
	file, stat, err := s.open(name)
	if err != nil {
		return Descriptor{}, err
	}
	defer file.Close()
	if stat.Size() > MaxArtifactBytes {
		return Descriptor{}, ErrArtifactCorrupt
	}
	hash := sha256.New()
	read, err := io.CopyBuffer(hash, io.LimitReader(file, MaxArtifactBytes+1), make([]byte, streamBufferBytes))
	if err != nil || read != stat.Size() || read > MaxArtifactBytes {
		return Descriptor{}, ErrArtifactCorrupt
	}
	return Descriptor{Key: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: read}, nil
}

func (s *LocalStore) usage() (int64, error) {
	var total int64
	err := fs.WalkDir(s.root.FS(), ".", func(_ string, entry fs.DirEntry, err error) error {
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

func (s *LocalStore) mkdirDurable(name string) error {
	if name == "." {
		return nil
	}
	current := "."
	for _, component := range splitPath(name) {
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
				return fmt.Errorf("artifact path component %s is not a directory", next)
			}
		default:
			return err
		}
		current = next
	}
	return nil
}

func splitPath(name string) []string {
	if name == "." || name == "" {
		return nil
	}
	return strings.Split(name, "/")
}

func (s *LocalStore) syncDirectory(name string) error {
	directory, err := s.root.Open(name)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func copyVerified(ctx context.Context, destination io.Writer, source io.Reader, expected Descriptor) (Descriptor, error) {
	hash := sha256.New()
	reader := io.LimitReader(contextReader{ctx: ctx, reader: source}, expected.Size+1)
	count, err := io.CopyBuffer(io.MultiWriter(destination, hash), reader, make([]byte, streamBufferBytes))
	if err != nil {
		return Descriptor{}, err
	}
	if count > expected.Size {
		return Descriptor{}, ErrArtifactTooLarge
	}
	if count != expected.Size {
		return Descriptor{}, ErrArtifactCorrupt
	}
	// LimitReader cannot show whether it stopped at exactly Size. Read one more
	// byte from the original source to reject a longer stream without buffering.
	var extra [1]byte
	for emptyReads := 0; ; emptyReads++ {
		n, readErr := contextReader{ctx: ctx, reader: source}.Read(extra[:])
		if n != 0 {
			return Descriptor{}, ErrArtifactTooLarge
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return Descriptor{}, readErr
		}
		if emptyReads == 99 {
			return Descriptor{}, io.ErrNoProgress
		}
	}
	actual := Descriptor{Key: expected.Key, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: count}
	if actual.SHA256 != expected.SHA256 {
		return Descriptor{}, ErrArtifactCorrupt
	}
	return actual, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type verifyingReadCloser struct {
	reader   io.Reader
	closer   io.Closer
	expected Descriptor
	hash     hash.Hash
	count    int64
	err      error
}

func (r *verifyingReadCloser) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		r.count += int64(n)
		_, _ = r.hash.Write(p[:n])
		if r.count > r.expected.Size || r.count > MaxArtifactBytes {
			r.err = ErrArtifactCorrupt
			return n, r.err
		}
	}
	if errors.Is(err, io.EOF) {
		if r.count != r.expected.Size || hex.EncodeToString(r.hash.Sum(nil)) != r.expected.SHA256 {
			r.err = ErrArtifactCorrupt
			return n, r.err
		}
	}
	return n, err
}

func (r *verifyingReadCloser) Close() error { return r.closer.Close() }
