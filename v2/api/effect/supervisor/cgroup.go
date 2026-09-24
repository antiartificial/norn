package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"norn/v2/api/effect"
)

func (b *cgroupBackend) QuerySnapshot(_ context.Context, execution BackendExecution, descriptor SnapshotDescriptor) (SnapshotManifest, error) {
	populated, err := cgroupPopulated(b.cgroupPath(execution.RuntimeInstanceID))
	if err != nil {
		return SnapshotManifest{}, fmt.Errorf("snapshot containment is unavailable: %w", err)
	}
	if populated {
		return SnapshotManifest{}, fmt.Errorf("snapshot containment is not proven")
	}
	return ReadSnapshotManifest(execution.StateDirectory, runnerStatusKey(b.key, execution.RuntimeInstanceID), execution.RuntimeInstanceID, true)
}

func (b *cgroupBackend) CopySnapshotArtifact(ctx context.Context, execution BackendExecution, descriptor SnapshotDescriptor, destination io.Writer) (SnapshotManifest, error) {
	manifest, err := b.QuerySnapshot(ctx, execution, descriptor)
	if err != nil {
		return SnapshotManifest{}, err
	}
	if err := VerifySnapshotManifestForDescriptor(manifest, descriptor, runnerStatusKey(b.key, execution.RuntimeInstanceID), execution.RuntimeInstanceID); err != nil {
		return SnapshotManifest{}, err
	}
	if _, err := CopySnapshotArtifact(execution.StateDirectory, runnerStatusKey(b.key, execution.RuntimeInstanceID), execution.RuntimeInstanceID, descriptor, true, destination); err != nil {
		return SnapshotManifest{}, err
	}
	return manifest, nil
}

const maxCgroupEventsBytes = 4 << 10

// cgroupBackend contains one execution per cgroup-v2 directory under root.
// Observation, revocation and result retrieval only read and write cgroupfs
// control files, so they are exercised against a fake filesystem on any
// platform; only Start (cgroup_linux.go) needs Linux clone-into-cgroup.
type cgroupBackend struct {
	root         string
	runnerBinary string
	key          []byte
	pollInterval time.Duration
}

func newCgroupBackend(root, runnerBinary string, signingKey []byte) *cgroupBackend {
	return &cgroupBackend{root: root, runnerBinary: runnerBinary, key: append([]byte(nil), signingKey...), pollInterval: 20 * time.Millisecond}
}

func (b *cgroupBackend) Observe(_ context.Context, execution BackendExecution) (BackendState, error) {
	return b.observe(execution)
}

func (b *cgroupBackend) Revoke(ctx context.Context, execution BackendExecution) (BackendState, error) {
	cgroupPath := b.cgroupPath(execution.RuntimeInstanceID)
	if _, err := os.Lstat(cgroupPath); errors.Is(err, os.ErrNotExist) {
		return BackendState{Phase: effect.SupervisorUnknown, EvidenceReference: b.reference(execution)}, nil
	} else if err != nil {
		return BackendState{}, err
	}
	if err := os.WriteFile(filepath.Join(cgroupPath, "cgroup.kill"), []byte("1\n"), 0o200); err != nil {
		return BackendState{}, fmt.Errorf("kill effect cgroup: %w", err)
	}
	ticker := time.NewTicker(b.pollInterval)
	defer ticker.Stop()
	for {
		populated, err := cgroupPopulated(cgroupPath)
		if err != nil {
			return BackendState{}, err
		}
		if !populated {
			return BackendState{Phase: effect.SupervisorStopped, ContainmentProven: true, EvidenceReference: b.reference(execution)}, nil
		}
		select {
		case <-ctx.Done():
			return BackendState{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// RetrieveResult returns output only when it matches the authenticated
// terminal status, so a replaced or truncated result can never be served.
func (b *cgroupBackend) RetrieveResult(_ context.Context, execution BackendExecution, reference string) ([]byte, error) {
	if reference != "result/"+execution.SupervisorExecutionID {
		return nil, fmt.Errorf("effect result reference mismatch")
	}
	status, err := readRunnerStatus(execution.StateDirectory, runnerStatusKey(b.key, execution.RuntimeInstanceID), execution.RuntimeInstanceID)
	if err != nil {
		return nil, err
	}
	return readVerifiedResult(execution.StateDirectory, status)
}

func (b *cgroupBackend) observe(execution BackendExecution) (BackendState, error) {
	unknown := BackendState{Phase: effect.SupervisorUnknown, EvidenceReference: b.reference(execution)}
	populated, err := cgroupPopulated(b.cgroupPath(execution.RuntimeInstanceID))
	if errors.Is(err, os.ErrNotExist) {
		// A missing cgroup proves nothing about the process; never NotFound.
		return unknown, nil
	}
	if err != nil {
		return BackendState{}, err
	}
	status, err := readRunnerStatus(execution.StateDirectory, runnerStatusKey(b.key, execution.RuntimeInstanceID), execution.RuntimeInstanceID)
	if errors.Is(err, os.ErrNotExist) {
		if populated {
			return BackendState{Phase: effect.SupervisorRunning, EvidenceReference: b.reference(execution)}, nil
		}
		return unknown, nil
	}
	if err != nil {
		return BackendState{}, err
	}
	if populated {
		// Terminal status with live descendants is still running.
		return BackendState{Phase: effect.SupervisorRunning, EvidenceReference: b.reference(execution)}, nil
	}
	if !status.terminal() {
		// The runner exited without publishing a terminal outcome.
		return unknown, nil
	}
	output, err := readVerifiedResult(execution.StateDirectory, status)
	if err != nil {
		return BackendState{}, err
	}
	return BackendState{
		Phase: status.Phase, ExitCode: status.ExitCode, Output: output, ContainmentProven: true,
		TimedOut: status.TimedOut, OutputDiscarded: status.OutputDiscarded, EvidenceReference: b.reference(execution),
	}, nil
}

func (b *cgroupBackend) cgroupPath(runtimeID string) string {
	digest := sha256.Sum256([]byte(runtimeID))
	return filepath.Join(b.root, hex.EncodeToString(digest[:]))
}

func (b *cgroupBackend) reference(execution BackendExecution) string {
	digest := sha256.Sum256([]byte(execution.RuntimeInstanceID))
	return "cgroup-v2/" + hex.EncodeToString(digest[:])
}

// cgroupPopulated parses cgroup.events strictly: every line is "key value",
// and exactly one populated line with value 0 or 1 must be present.
func cgroupPopulated(path string) (bool, error) {
	data, err := readBoundedRegular(filepath.Join(path, "cgroup.events"), maxCgroupEventsBytes)
	if err != nil {
		return false, err
	}
	found, populated := false, false
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return false, fmt.Errorf("cgroup.events line is malformed")
		}
		if fields[0] != "populated" {
			continue
		}
		if found {
			return false, fmt.Errorf("cgroup.events has duplicate populated fields")
		}
		found = true
		switch fields[1] {
		case "0":
		case "1":
			populated = true
		default:
			return false, fmt.Errorf("cgroup.events has invalid populated value")
		}
	}
	if !found {
		return false, fmt.Errorf("cgroup.events has no populated field")
	}
	return populated, nil
}
