package pipeline

// The attested publication adapter is intentionally independent of the
// runner/supervisor package to avoid a pipeline -> supervisor import cycle.
// Its source is a one-shot verified copier (Manager.CopySnapshotArtifact).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type AttestedSnapshotArtifact struct {
	OperationID     string
	ClaimGeneration int64
	SHA256          string
	Size            int64
	Copy            func(io.Writer) error
	// Fence must prove the operation claim is still live. It is called before
	// provenance and again immediately before publication; a stale worker may
	// stage bytes but cannot create a trusted snapshot pair.
	Fence func() error
}

// PublishAttestedSnapshot stages verified node-local bytes and publishes with
// the existing sidecar-first, no-replace contract. The operation-derived label
// makes an accepted replay target one deterministic filename; it may reuse
// only a bound dump with the exact expected bytes.
func PublishAttestedSnapshot(location snapshotLocation, at time.Time, artifact AttestedSnapshotArtifact) (*dataSnapshot, error) {
	if location.bound == nil || artifact.OperationID == "" || artifact.ClaimGeneration <= 0 || artifact.Size <= 0 || len(artifact.SHA256) != 64 || artifact.Copy == nil || artifact.Fence == nil {
		return nil, fmt.Errorf("attested snapshot publication is incomplete")
	}
	if err := os.MkdirAll(location.dir, 0o750); err != nil {
		return nil, err
	}
	label := "effect-" + artifact.OperationID
	if len(label) > 64 {
		label = label[:64]
	}
	filename := fmt.Sprintf("%s_%s_%s.dump", location.database, label, at.UTC().Format("20060102T150405"))
	path := filepath.Join(location.dir, filename)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() != artifact.Size || verifyBoundDump(location, filename, info.Size()) != nil {
			return nil, fmt.Errorf("existing snapshot publication is unverified")
		}
		if digest, e := fileSHA256(path); e != nil || digest != artifact.SHA256 {
			return nil, fmt.Errorf("existing snapshot publication differs from attested artifact")
		}
		return &dataSnapshot{Filename: filename, Timestamp: at.UTC().Format("20060102T150405"), Size: info.Size()}, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	tmp, err := os.OpenFile(filepath.Join(location.dir, ".attested-snapshot-"+artifact.OperationID+".tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	hash := sha256.New()
	if err := artifact.Copy(io.MultiWriter(tmp, hash)); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(tmpPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.Size || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return nil, fmt.Errorf("attested artifact bytes do not match manifest")
	}
	if err := artifact.Fence(); err != nil {
		return nil, fmt.Errorf("snapshot claim fence before provenance: %w", err)
	}
	if err := writeSidecarExclusive(location, filename, artifact.SHA256, artifact.Size); err != nil {
		return nil, err
	}
	if err := artifact.Fence(); err != nil {
		return nil, fmt.Errorf("snapshot claim fence before publication: %w", err)
	}
	if err := os.Link(tmpPath, path); err != nil {
		if err == fs.ErrExist {
			return nil, fmt.Errorf("snapshot publication raced after sidecar")
		}
		return nil, err
	}
	return &dataSnapshot{Filename: filename, Timestamp: at.UTC().Format("20060102T150405"), Size: artifact.Size}, nil
}
