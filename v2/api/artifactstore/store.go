// Package artifactstore provides streaming, content-addressed storage for
// large database artifacts. It is deliberately independent of archive.Store:
// evidence bundles are bounded []byte values, while these artifacts can be up
// to 64 GiB and must never require whole-file buffering.
package artifactstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxArtifactBytes is the per-artifact limit shared by every adapter. It is a
// ceiling on source, stored, and materialized bytes; it is not a claim about
// available local or remote storage capacity.
const MaxArtifactBytes int64 = 64 << 30

var (
	ErrInvalidDescriptor = errors.New("artifact descriptor is invalid")
	ErrArtifactTooLarge  = errors.New("artifact exceeds the 64 GiB limit")
	ErrArtifactCorrupt   = errors.New("artifact does not match its descriptor")
	ErrArtifactNotFound  = errors.New("artifact not found")
	ErrArtifactFull      = errors.New("artifact store capacity exhausted")
)

// Descriptor is an immutable artifact identity. Key is derived exclusively
// from SHA256, so callers cannot choose a mutable or semantically overloaded
// storage name.
type Descriptor struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// KeyForSHA256 returns the sole allowed key for a SHA-256 digest.
func KeyForSHA256(sum string) string { return "sha256/" + strings.ToLower(sum) }

// Validate ensures the descriptor has the canonical, content-bound identity.
func (d Descriptor) Validate() error {
	if d.Size < 0 {
		return fmt.Errorf("%w: negative size", ErrInvalidDescriptor)
	}
	if d.Size > MaxArtifactBytes {
		return ErrArtifactTooLarge
	}
	if len(d.SHA256) != 64 || !isLowerHex(d.SHA256) {
		return fmt.Errorf("%w: SHA-256 must be 64 lowercase hex characters", ErrInvalidDescriptor)
	}
	if d.Key != KeyForSHA256(d.SHA256) {
		return fmt.Errorf("%w: key must equal %q", ErrInvalidDescriptor, KeyForSHA256(d.SHA256))
	}
	return nil
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

// Store is portable across private local filesystems and future object-store
// adapters. Publish reads source once and verifies it against expected before
// immutable publication. Open streams a single artifact; a full read through
// EOF verifies its exact size and SHA-256. Materialize and Verify always read
// through EOF and therefore provide complete verification.
//
// All methods reject descriptors above MaxArtifactBytes. A Store never accepts
// caller-selected keys and never exposes a []byte artifact API.
type Store interface {
	Publish(ctx context.Context, expected Descriptor, source io.Reader) (Descriptor, error)
	Open(ctx context.Context, expected Descriptor) (io.ReadCloser, error)
	Materialize(ctx context.Context, expected Descriptor, destination io.Writer) error
	Verify(ctx context.Context, expected Descriptor) error
}
