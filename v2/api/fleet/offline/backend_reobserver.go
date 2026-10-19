package offline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// ErrLatestBackendMismatch means the latest state could not be proven to be
// the exact version and bytes held by the dispatch binding.
var ErrLatestBackendMismatch = errors.New("latest Fleet backend differs from bound state")

const maxEffectStateBytes = 64 << 20

// LatestBackendReader is deliberately read-only. Latest and Version must be
// implemented against the object store, not the runner's cached receipt.
type LatestBackendReader interface {
	Latest(context.Context, string, string) (versionID string, size int64, err error)
	Version(context.Context, string, string, string, int64) ([]byte, error)
}

// VerifyLatestBackend repeats the latest-object lookup around a version-pinned
// byte read. A concurrent overwrite, delete marker, oversized or malformed
// state, or stale serial/lineage all deny an effect grant.
func VerifyLatestBackend(ctx context.Context, reader LatestBackendReader, bound Backend) error {
	if reader == nil || bound.Bucket == "" || bound.Key == "" || bound.VersionID == "" ||
		bound.Lineage == "" || bound.Serial == 0 || !hex64.MatchString(bound.RawSHA256) {
		return ErrLatestBackendMismatch
	}
	version, size, err := reader.Latest(ctx, bound.Bucket, bound.Key)
	if err != nil || version != bound.VersionID || size <= 0 || size > maxEffectStateBytes {
		return ErrLatestBackendMismatch
	}
	raw, err := reader.Version(ctx, bound.Bucket, bound.Key, version, size)
	if err != nil || int64(len(raw)) != size {
		return ErrLatestBackendMismatch
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != bound.RawSHA256 {
		return ErrLatestBackendMismatch
	}
	var state struct {
		Lineage string `json:"lineage"`
		Serial  uint64 `json:"serial"`
	}
	if !json.Valid(raw) || rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(raw))) != nil ||
		json.Unmarshal(raw, &state) != nil || state.Lineage != bound.Lineage || state.Serial != bound.Serial {
		return ErrLatestBackendMismatch
	}
	latest, latestSize, err := reader.Latest(ctx, bound.Bucket, bound.Key)
	if err != nil || latest != version || latestSize != size {
		return ErrLatestBackendMismatch
	}
	return nil
}
