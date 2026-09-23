// Package archive is the evidence/history archive layer of Norn v3 M2 (ADR 0001).
// Bulky terminal evidence and event bodies are moved to an immutable, checksummed
// archive only after their replay/recovery window, and the source payload is
// pruned ONLY after the archived object has been verified retrievable — never
// before. StoreVerified encodes that ordering so a caller cannot prune against an
// unverified archive.
//
// Local profiles use the filesystem adapter here; a Fleet profile supplies an
// object-storage adapter satisfying the same interface. Both preserve original
// signed bytes exactly (no reformatting), so signatures still verify after a
// round trip.
package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound is returned when an archived object is absent.
var ErrNotFound = errors.New("archive: object not found")

// Reference is the durable watermark recorded in the control store after a
// verified archive. Holding one authorizes pruning the source payload.
type Reference struct {
	Key      string `json:"key"`
	Checksum string `json:"checksum"` // sha256 hex of the original bytes
	Size     int64  `json:"size"`
}

// Archive stores immutable, checksummed objects.
type Archive interface {
	// Put stores data under key and returns its reference. Storage is atomic:
	// a reader never observes a partial object.
	Put(ctx context.Context, key string, data []byte) (Reference, error)
	// Get returns the object bytes, or ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, error)
	// Verify re-reads the object and confirms its checksum and size match ref;
	// a missing or corrupted object is an error.
	Verify(ctx context.Context, ref Reference) error
	// Delete removes an object (idempotent).
	Delete(ctx context.Context, key string) error
}

func checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// StoreVerified implements the ADR 0001 archive protocol: upload immutable bytes,
// then verify the object is retrievable with a matching checksum, and only then
// return the reference. If verification fails it returns an error and no
// reference, so the caller never prunes the source against an unverified archive.
func StoreVerified(ctx context.Context, a Archive, key string, data []byte) (Reference, error) {
	ref, err := a.Put(ctx, key, data)
	if err != nil {
		return Reference{}, err
	}
	if err := a.Verify(ctx, ref); err != nil {
		return Reference{}, fmt.Errorf("archive: verification failed for %s: %w", key, err)
	}
	return ref, nil
}

// FSArchive is a filesystem-backed Archive using atomic temp-file + rename writes.
type FSArchive struct {
	root string
}

// NewFSArchive returns a filesystem archive rooted at dir, creating it if needed.
func NewFSArchive(dir string) (*FSArchive, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &FSArchive{root: dir}, nil
}

var _ Archive = (*FSArchive)(nil)

func (f *FSArchive) path(key string) string {
	// Keys may be namespaced with '/'; keep them within the root.
	return filepath.Join(f.root, filepath.FromSlash(key))
}

func (f *FSArchive) Put(ctx context.Context, key string, data []byte) (Reference, error) {
	dest := f.path(key)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return Reference{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".archive-*")
	if err != nil {
		return Reference{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return Reference{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Reference{}, err
	}
	if err := tmp.Close(); err != nil {
		return Reference{}, err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return Reference{}, err
	}
	return Reference{Key: key, Checksum: checksum(data), Size: int64(len(data))}, nil
}

func (f *FSArchive) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := os.ReadFile(f.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

func (f *FSArchive) Verify(ctx context.Context, ref Reference) error {
	data, err := f.Get(ctx, ref.Key)
	if err != nil {
		return err
	}
	if int64(len(data)) != ref.Size {
		return fmt.Errorf("archive: size mismatch for %s: have %d want %d", ref.Key, len(data), ref.Size)
	}
	if got := checksum(data); got != ref.Checksum {
		return fmt.Errorf("archive: checksum mismatch for %s", ref.Key)
	}
	return nil
}

func (f *FSArchive) Delete(ctx context.Context, key string) error {
	err := os.Remove(f.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
