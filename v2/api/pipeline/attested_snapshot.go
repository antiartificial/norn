package pipeline

// The attested publication adapter is intentionally independent of the
// runner/supervisor package to avoid a pipeline -> supervisor import cycle.
// Its source is a one-shot verified copier (Manager.CopySnapshotArtifact).

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"norn/v2/api/effect"
)

type AttestedSnapshotArtifact struct {
	OperationID     string
	ClaimGeneration int64
	SHA256          string
	Size            int64
	Copy            func(io.Writer) error
	// Prepare commits the immutable operation intent before either member of
	// the public pair exists. Receipt is called only after the pair has been
	// verified; production callers use both callbacks.
	Prepare func(string) error
	Receipt func(string) error
	// Fence holds operation ownership while it runs the sidecar and dump
	// publication callback. A stale worker may stage bytes but cannot create a
	// trusted public pair after claim turnover.
	Fence func(func() error) error
}

var linkAttestedSnapshot = os.Link

// PublishAttestedSnapshot stages verified node-local bytes and publishes with
// the existing sidecar-first, no-replace contract. The operation-derived label
// makes an accepted replay target one deterministic filename; it may reuse
// only a bound dump with the exact expected bytes.
func PublishAttestedSnapshot(location snapshotLocation, at time.Time, artifact AttestedSnapshotArtifact) (*dataSnapshot, error) {
	if location.bound == nil || artifact.OperationID == "" || artifact.ClaimGeneration <= 0 || artifact.Size <= 0 || len(artifact.SHA256) != 64 || artifact.Copy == nil {
		return nil, fmt.Errorf("attested snapshot publication is incomplete")
	}
	if err := os.MkdirAll(location.dir, 0o750); err != nil {
		return nil, err
	}
	if err := removeAttestedSnapshotStaging(location.dir, artifact.OperationID); err != nil {
		return nil, err
	}
	label := "effect-" + artifact.OperationID
	if len(label) > 64 {
		label = label[:64]
	}
	filename := fmt.Sprintf("%s_%s_%s.dump", location.database, label, at.UTC().Format("20060102T150405"))
	path := filepath.Join(location.dir, filename)
	if artifact.Prepare != nil {
		if err := artifact.Prepare(filename); err != nil {
			return nil, fmt.Errorf("prepare snapshot publication intent: %w", err)
		}
	}
	if info, err := os.Lstat(path); err == nil {
		if artifact.Prepare == nil && artifact.Fence != nil {
			if err := artifact.Fence(func() error { return nil }); err != nil {
				return nil, fmt.Errorf("snapshot claim fence before replay: %w", err)
			}
		}
		if !info.Mode().IsRegular() || info.Size() != artifact.Size || verifyBoundDump(location, filename, info.Size()) != nil {
			return nil, fmt.Errorf("existing snapshot publication is unverified")
		}
		if digest, e := fileSHA256(path); e != nil || digest != artifact.SHA256 {
			return nil, fmt.Errorf("existing snapshot publication differs from attested artifact")
		}
		if err := recordAttestedSnapshotReceipt(artifact, filename); err != nil {
			return nil, err
		}
		return &dataSnapshot{Filename: filename, Timestamp: at.UTC().Format("20060102T150405"), Size: info.Size()}, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// A crashed staging writer is untrusted but must not block a retry. The
	// immutable operation-derived publication name below is the replay identity;
	// staging names are deliberately private and unique.
	tmp, err := os.CreateTemp(location.dir, ".attested-snapshot-"+artifact.OperationID+"-*")
	if err != nil {
		return nil, err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
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
	publish := func() error {
		if err := ensureAttestedSidecar(location, filename, artifact.SHA256, artifact.Size); err != nil {
			return err
		}
		return linkAttestedSnapshot(tmpPath, path)
	}
	if artifact.Prepare == nil && artifact.Fence != nil {
		err = artifact.Fence(publish)
	} else {
		err = publish()
	}
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			if info, e := os.Lstat(path); e == nil && info.Mode().IsRegular() && info.Size() == artifact.Size && verifyBoundDump(location, filename, info.Size()) == nil {
				if digest, e := fileSHA256(path); e == nil && digest == artifact.SHA256 {
					if e := recordAttestedSnapshotReceipt(artifact, filename); e != nil {
						return nil, e
					}
					return &dataSnapshot{Filename: filename, Timestamp: at.UTC().Format("20060102T150405"), Size: info.Size()}, nil
				}
			}
			return nil, fmt.Errorf("snapshot publication raced with different bytes")
		}
		return nil, fmt.Errorf("snapshot publication: %w", err)
	}
	if err := verifyBoundDump(location, filename, artifact.Size); err != nil {
		return nil, fmt.Errorf("verify published snapshot pair: %w", err)
	}
	if err := recordAttestedSnapshotReceipt(artifact, filename); err != nil {
		return nil, err
	}
	return &dataSnapshot{Filename: filename, Timestamp: at.UTC().Format("20060102T150405"), Size: artifact.Size}, nil
}

// Once a matching public pair is visible, failure to record its receipt is
// ambiguous rather than a failed snapshot. Leave it queued for exact replay;
// integrity failures above remain ordinary fail-closed errors.
func recordAttestedSnapshotReceipt(artifact AttestedSnapshotArtifact, filename string) error {
	if artifact.Receipt == nil {
		return nil
	}
	if err := artifact.Receipt(filename); err != nil {
		return &effect.PendingError{EffectID: artifact.OperationID, Resource: "snapshot/" + artifact.OperationID, Reason: "persist snapshot publication receipt", Cause: err}
	}
	return nil
}

// removeAttestedSnapshotStaging removes only abandoned private staging files
// for this operation. A live claim is exclusive, so a retry cannot race a
// second legitimate writer for the same operation; removing these files makes
// a process crash recoverable without treating local staging bytes as proof.
func removeAttestedSnapshotStaging(dir, operationID string) error {
	prefix := ".attested-snapshot-" + operationID + "-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("attested snapshot staging entry %s is not a regular file", entry.Name())
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func ensureAttestedSidecar(location snapshotLocation, filename, digest string, size int64) error {
	if err := writeSidecarExclusive(location, filename, digest, size); !errors.Is(err, fs.ErrExist) {
		return err
	}
	sidecar, err := readSidecar(location, filename)
	if err != nil || sidecar == nil || sidecar.Target != location.bound.resolved.Target || sidecar.SHA256 != digest || sidecar.Size != size {
		return fmt.Errorf("existing snapshot sidecar differs from attested artifact")
	}
	return nil
}
