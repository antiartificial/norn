package cutover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"norn/v2/api/archive"
)

const maxPhaseEvidenceBytes int64 = 1 << 20

// PhaseEvidenceKey is the immutable archive location for one journal edge.
// It contains no app, provider, or credential names.
func PhaseEvidenceKey(j Journal, next Phase) (string, error) {
	if err := j.Validate(); err != nil {
		return "", err
	}
	if !validPhase(next) || next == PhasePrepare {
		return "", fmt.Errorf("%w: invalid evidence phase", ErrTransition)
	}
	digest, err := IntentSHA256(j.Intent)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("database-cutover/v1/%s/%d/%s", digest, j.Revision, next), nil
}

// RetainPhaseEvidence publishes bounded, immutable bytes and verifies their
// readback before returning a journal reference. The caller must separately
// validate that the bytes prove the named external effect and contain no
// secrets. This function grants no authority to advance a claimed journal.
func RetainPhaseEvidence(ctx context.Context, store archive.Store, j Journal, next Phase, data []byte) (PhaseEvidenceReference, error) {
	if store == nil || len(data) == 0 || int64(len(data)) > maxPhaseEvidenceBytes {
		return PhaseEvidenceReference{}, fmt.Errorf("%w: missing archive or invalid evidence size", ErrTransition)
	}
	digest := sha256.Sum256(data)
	want := hex.EncodeToString(digest[:])
	ref, err := NewPhaseEvidenceReference(j, next, want)
	if err != nil {
		return PhaseEvidenceReference{}, err
	}
	if _, err := j.AdvanceWithEvidence(ref); err != nil {
		return PhaseEvidenceReference{}, err
	}
	key, err := PhaseEvidenceKey(j, next)
	if err != nil {
		return PhaseEvidenceReference{}, err
	}
	info, err := store.PutImmutable(ctx, key, data)
	if err != nil {
		return PhaseEvidenceReference{}, err
	}
	if info.Key != key || info.SHA256 != want || info.Size != int64(len(data)) {
		return PhaseEvidenceReference{}, fmt.Errorf("%w: archive publication identity differs", archive.ErrObjectCorrupt)
	}
	if err := store.Verify(ctx, info); err != nil {
		return PhaseEvidenceReference{}, err
	}
	return ref, nil
}

// ReadPhaseEvidence retrieves exactly the bytes named by a journal reference.
// It proves retention and integrity, not the truth of an external effect.
func ReadPhaseEvidence(ctx context.Context, reader archive.Reader, j Journal, ref PhaseEvidenceReference) ([]byte, error) {
	if reader == nil {
		return nil, fmt.Errorf("%w: missing archive reader", ErrTransition)
	}
	key, err := PhaseEvidenceKey(j, ref.NextPhase)
	if err != nil {
		return nil, err
	}
	if _, err := j.AdvanceWithEvidence(ref); err != nil {
		return nil, err
	}
	data, info, err := reader.Get(ctx, key, maxPhaseEvidenceBytes)
	if err != nil {
		return nil, err
	}
	if info.Key != key || info.SHA256 != ref.EvidenceSHA256 || info.Size != int64(len(data)) || int64(len(data)) > maxPhaseEvidenceBytes {
		return nil, fmt.Errorf("%w: phase evidence identity differs", archive.ErrObjectCorrupt)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != ref.EvidenceSHA256 {
		return nil, fmt.Errorf("%w: phase evidence digest differs", archive.ErrObjectCorrupt)
	}
	return data, nil
}
