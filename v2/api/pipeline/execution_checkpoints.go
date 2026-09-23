package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"norn/v2/api/effect"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// Execution checkpoints make an accepted operation's source and build outputs
// immutable across claims. They are enabled with supervised build.test, where
// a deferred test effect is resumed by a later claim: that claim must act on
// the same source and reuse the same build instead of repeating Docker
// build/push or authorizing a test run against different content.

type sourceCheckpoint struct {
	SourceKind    string   `json:"sourceKind"`
	CommitSHA     string   `json:"commitSha"`
	SourceRef     string   `json:"sourceRef"`
	SourceDirty   bool     `json:"sourceDirty"`
	SourceChanges []string `json:"sourceChanges"`
	TreeDigest    string   `json:"treeDigest"`
}

type buildCheckpoint struct {
	ImageTag       string `json:"imageTag"`
	SourceIdentity string `json:"sourceIdentity"`
}

// SourceIdentityChangedError means a later claim resolved different source
// content than the operation's recorded checkpoint. The operation is not
// re-executed on the new content.
type SourceIdentityChangedError struct{ Field string }

func (e *SourceIdentityChangedError) Error() string {
	return fmt.Sprintf("source %s differs from this operation's recorded checkpoint; the accepted operation will not run on different source (start a new operation)", e.Field)
}

func (p *Pipeline) executionCheckpointsEnabled(st *state) bool {
	return p.BuildTestEffects != nil && p.DB != nil && st.claim.OperationID() != "" && st.claim.Generation() > 0
}

func checkpointPending(st *state, stage string, err error) error {
	return &effect.PendingError{Resource: "operation/" + st.claim.OperationID() + "/" + stage, Reason: "execution checkpoint is unavailable", Cause: err}
}

// checkpointedClone resolves source for this claim. The first claim records
// the resolved identity; later claims check out the recorded commit (for git
// sources) and must reproduce the recorded tree exactly.
func (p *Pipeline) checkpointedClone(ctx context.Context, st *state, sg *saga.Saga) error {
	if !p.executionCheckpointsEnabled(st) {
		return p.clone(ctx, st, sg)
	}
	recordedCheckpoint, err := p.DB.LoadOperationCheckpoint(ctx, st.claim.OperationID(), store.CheckpointSource)
	if err != nil {
		return checkpointPending(st, store.CheckpointSource, err)
	}
	var recorded *sourceCheckpoint
	if recordedCheckpoint != nil {
		recorded = &sourceCheckpoint{}
		if err := json.Unmarshal(recordedCheckpoint.Outputs, recorded); err != nil {
			return fmt.Errorf("decode source checkpoint: %w", err)
		}
		if recorded.SourceKind == "git_clone" && isFullCommitSHA(recorded.CommitSHA) {
			st.sourceRef = recorded.CommitSHA
		}
	}
	if err := p.clone(ctx, st, sg); err != nil {
		return err
	}
	tree, err := treeDigest(st.workDir)
	if err != nil {
		return fmt.Errorf("source tree identity: %w", err)
	}
	current := sourceCheckpoint{SourceKind: st.sourceKind, CommitSHA: st.commitSHA, SourceRef: st.sourceRef, SourceDirty: st.sourceDirty, SourceChanges: append([]string{}, st.sourceChanges...), TreeDigest: tree}
	if recorded == nil {
		encoded, err := json.Marshal(current)
		if err != nil {
			return err
		}
		stored, err := p.DB.RecordOperationCheckpoint(ctx, st.claim, store.CheckpointSource, encoded)
		if err != nil && !errors.Is(err, store.ErrCheckpointConflict) {
			return checkpointPending(st, store.CheckpointSource, err)
		}
		recorded = &sourceCheckpoint{}
		if err := json.Unmarshal(stored.Outputs, recorded); err != nil {
			return fmt.Errorf("decode source checkpoint: %w", err)
		}
	}
	if err := sameSource(*recorded, current); err != nil {
		return err
	}
	// Provenance comes from the first resolution; a non-git source has a
	// synthetic per-claim commit label that the tree digest replaces.
	st.commitSHA, st.sourceRef, st.sourceChanges = recorded.CommitSHA, recorded.SourceRef, append([]string(nil), recorded.SourceChanges...)
	encoded, err := json.Marshal(recorded)
	if err != nil {
		return err
	}
	st.sourceIdentity = digestBytes(encoded)
	return nil
}

func sameSource(recorded, current sourceCheckpoint) error {
	switch {
	case recorded.SourceKind != current.SourceKind:
		return &SourceIdentityChangedError{Field: "kind"}
	case recorded.TreeDigest != current.TreeDigest:
		return &SourceIdentityChangedError{Field: "content"}
	case recorded.SourceDirty != current.SourceDirty || !equalStringLists(recorded.SourceChanges, current.SourceChanges):
		return &SourceIdentityChangedError{Field: "working-tree state"}
	case isFullCommitSHA(recorded.CommitSHA) && recorded.CommitSHA != current.CommitSHA:
		return &SourceIdentityChangedError{Field: "commit"}
	}
	return nil
}

// reuseCheckpointedBuild restores a build recorded by an earlier claim of the
// same operation. It reports whether the build step may be skipped.
func (p *Pipeline) reuseCheckpointedBuild(ctx context.Context, st *state, sg *saga.Saga) (bool, error) {
	if !p.executionCheckpointsEnabled(st) {
		return false, nil
	}
	if st.sourceIdentity == "" {
		return false, fmt.Errorf("build checkpoint requires a recorded source identity")
	}
	recorded, err := p.DB.LoadOperationCheckpoint(ctx, st.claim.OperationID(), store.CheckpointBuild)
	if err != nil {
		return false, checkpointPending(st, store.CheckpointBuild, err)
	}
	if recorded == nil {
		return false, nil
	}
	var build buildCheckpoint
	if err := json.Unmarshal(recorded.Outputs, &build); err != nil || build.ImageTag == "" {
		return false, fmt.Errorf("build checkpoint is malformed")
	}
	if build.SourceIdentity != st.sourceIdentity {
		return false, &SourceIdentityChangedError{Field: "identity for the recorded build"}
	}
	st.imageTag = build.ImageTag
	if sg != nil {
		sg.Log(ctx, "build.reused", fmt.Sprintf("reusing %s built by claim %d of this operation", build.ImageTag, recorded.ClaimGeneration), map[string]string{"imageTag": build.ImageTag})
	}
	return true, nil
}

// recordBuild checkpoints a completed build before any later stage runs.
func (p *Pipeline) recordBuild(ctx context.Context, st *state) error {
	if !p.executionCheckpointsEnabled(st) {
		return nil
	}
	encoded, err := json.Marshal(buildCheckpoint{ImageTag: st.imageTag, SourceIdentity: st.sourceIdentity})
	if err != nil {
		return err
	}
	if _, err := p.DB.RecordOperationCheckpoint(ctx, st.claim, store.CheckpointBuild, encoded); err != nil {
		return fmt.Errorf("record build checkpoint: %w", err)
	}
	return nil
}

func (p *Pipeline) runBuildCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if p.RunBuildCommand != nil {
		return p.RunBuildCommand(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// treeDigest identifies a checked-out tree by path, type, executable bit,
// symlink target and content, excluding the root .git entry. Special files
// are rejected rather than silently ignored.
func treeDigest(root string) (string, error) {
	hash := sha256.New()
	field := func(values ...string) {
		for _, value := range values {
			var length [8]byte
			binary.BigEndian.PutUint64(length[:], uint64(len(value)))
			hash.Write(length[:])
			hash.Write([]byte(value))
		}
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch mode := entry.Type(); {
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			field("symlink", relative, target)
		case entry.IsDir():
			field("dir", relative)
		case mode.IsRegular():
			info, err := entry.Info()
			if err != nil {
				return err
			}
			content, err := fileDigest(path)
			if err != nil {
				return err
			}
			executable := "0"
			if info.Mode().Perm()&0o111 != 0 {
				executable = "1"
			}
			field("file", relative, executable, content)
		default:
			return fmt.Errorf("unsupported file type at %s", relative)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func equalStringLists(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
