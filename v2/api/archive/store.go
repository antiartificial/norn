// Package archive is Norn's durable evidence archive (ADR 0001): immutable,
// checksummed objects holding completed evidence that is no longer kept hot
// in the control store.
package archive

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// ObjectInfo is an object's exact identity.
type ObjectInfo struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Reader is the capability needed for offline verification and index recovery.
// Opening a Reader must not publish a probe object or require write permission.
type Reader interface {
	// Get returns an object's bytes, refusing objects larger than maxBytes.
	Get(ctx context.Context, key string, maxBytes int64) ([]byte, ObjectInfo, error)
	// Verify proves the stored object has exactly the recorded identity.
	Verify(ctx context.Context, expected ObjectInfo) error
	// List returns keys under a prefix, sorted.
	List(ctx context.Context, prefix string) ([]string, error)
}

// Store adds immutable publication to Reader. Writer opening qualifies the
// backend's conditional-create guarantee before any evidence is pruned.
type Store interface {
	Reader
	// PutImmutable publishes data under key without ever replacing it. An
	// existing object with identical content is a successful duplicate; any
	// other existing content is ErrImmutableConflict.
	PutImmutable(ctx context.Context, key string, data []byte) (ObjectInfo, error)
}

// CapacityReporter is implemented by stores with a known capacity bound
// (the local adapter). Object stores report exhaustion only as write errors.
type CapacityReporter interface {
	Capacity(ctx context.Context) (used, limit int64, err error)
}

var (
	ErrImmutableConflict = errors.New("archive object exists with different content")
	ErrArchiveFull       = errors.New("archive capacity exhausted")
	ErrObjectNotFound    = errors.New("archive object not found")
	ErrObjectCorrupt     = errors.New("archive object does not match its recorded identity")
	ErrObjectTooLarge    = errors.New("archive object exceeds the read bound")
)

var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*$`)

// ValidKey reports whether key is a clean relative object key.
func ValidKey(key string) bool {
	return len(key) <= 512 && keyPattern.MatchString(key) && !strings.Contains(key, "..")
}
