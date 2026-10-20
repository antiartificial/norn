package observerclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"norn/v2/api/fleet/offline"
)

// BackendTranscriptReader must be configured with read-only credentials for
// the state bucket. Its methods must query the object store, never cached
// plan or runner receipts. ForeignLockAbsent accepts only an authenticated
// NotFound response for the native OpenTofu lock object.
type BackendTranscriptReader interface {
	offline.LatestBackendReader
	ForeignLockAbsent(context.Context, string, string) error
}

// EffectBackendReader captures exact state bytes independently of the runner.
// ExpectedAccount and the bucket/key pin are configured by the authority,
// rather than learned from the challenge. State larger than eight MiB fails
// closed because the observer journal has a 16 MiB backend transcript limit.
type EffectBackendReader struct {
	Reader          BackendTranscriptReader
	ExpectedAccount string
	Bucket          string
	Key             string
}

const maxEffectBackendTranscriptState = 8 << 20

var ErrEffectBackendRead = errors.New("effect backend state cannot be independently proven")

type effectBackendCapture struct {
	BackendTranscriptReader
	raw []byte
}

func (c *effectBackendCapture) Version(ctx context.Context, bucket, key, version string, size int64) ([]byte, error) {
	if size <= 0 || size > maxEffectBackendTranscriptState {
		return nil, ErrEffectBackendRead
	}
	raw, err := c.BackendTranscriptReader.Version(ctx, bucket, key, version, size)
	if err == nil {
		c.raw = append([]byte(nil), raw...)
	}
	return raw, err
}

// Read captures the version-pinned raw state, verifies its hash, lineage and
// serial against the exact challenge binding, and brackets the read with
// latest-object, versioning and foreign-lock checks. The returned bytes are
// journal evidence, not a grant or a substitute for the separate writer fence.
func (r EffectBackendReader) Read(ctx context.Context, challenge offline.EffectChallengeV1, binding offline.Binding) ([]byte, error) {
	bound := binding.Backend
	if r.Reader == nil || r.ExpectedAccount == "" || r.Bucket == "" || r.Key == "" || challenge.ID == "" ||
		binding.ProviderAccountUUID != r.ExpectedAccount || challenge.ProviderAccountUUID != r.ExpectedAccount ||
		bound.Bucket != r.Bucket || bound.Key != r.Key || challenge.CurrentBackend != bound ||
		challenge.PlanID != binding.PlanID || challenge.TargetID != binding.TargetID ||
		challenge.SourceCommit != binding.SourceCommit || challenge.SavedPlanSHA256 != binding.SavedPlanSHA256 ||
		challenge.EffectSHA256 != binding.EffectSHA256 || challenge.FenceGeneration != binding.FenceGeneration ||
		challenge.NodeName != binding.NodeName || challenge.DropletID != binding.DropletID {
		return nil, ErrEffectBackendRead
	}
	if err := r.Reader.ForeignLockAbsent(ctx, bound.Bucket, bound.Key); err != nil {
		return nil, fmt.Errorf("pre-read backend lock: %w", ErrEffectBackendRead)
	}
	capture := &effectBackendCapture{BackendTranscriptReader: r.Reader}
	if err := offline.VerifyLatestBackend(ctx, capture, bound); err != nil || len(capture.raw) == 0 {
		return nil, fmt.Errorf("version-pinned backend read: %w", ErrEffectBackendRead)
	}
	if err := r.Reader.ForeignLockAbsent(ctx, bound.Bucket, bound.Key); err != nil {
		return nil, fmt.Errorf("post-read backend lock: %w", ErrEffectBackendRead)
	}
	transcript, err := json.Marshal(struct {
		Schema              string          `json:"schema"`
		ChallengeID         string          `json:"challengeId"`
		ProviderAccountUUID string          `json:"providerAccountUUID"`
		Backend             offline.Backend `json:"backend"`
		RawState            []byte          `json:"rawState"`
		VersioningAndLatest string          `json:"versioningAndLatest"`
		OpenTofuLock        string          `json:"openTofuLock"`
	}{"norn.fleet-effect-backend-transcript/v1", challenge.ID, r.ExpectedAccount,
		bound, capture.raw, "verified-before-and-after", "authenticated-absent-before-and-after"})
	if err != nil || len(transcript) > 16<<20 {
		return nil, ErrEffectBackendRead
	}
	return transcript, nil
}
