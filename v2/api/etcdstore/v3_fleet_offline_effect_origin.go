package etcdstore

import (
	"context"

	"norn/v2/api/fleet/offline"
)

// offlineEffectSourceVerifier must use its own read-only GitHub App
// installation token. In production githubapp.Client implements this against
// the fixed Fleet repository and main branch; the runner cannot supply proof.
type offlineEffectSourceVerifier interface {
	VerifyOfflineEffectSource(context.Context, string) error
}

// originCheckedEffectReobserver removes any delegate-supplied origin claim and
// sets it only after a fresh authenticated read of the exact binding source.
// It is a composable slice, not a public grant route or live runner wiring.
type originCheckedEffectReobserver struct {
	remaining offlineEffectReobserver
	origin    offlineEffectSourceVerifier
}

func (r originCheckedEffectReobserver) reobserveEffect(ctx context.Context, binding offline.Binding, attempt offline.Attempt) (offline.EffectObservation, error) {
	if r.remaining == nil || r.origin == nil {
		return offline.EffectObservation{}, offline.ErrEffectProofMissing
	}
	observation, err := r.remaining.reobserveEffect(ctx, binding, attempt)
	if err != nil {
		return offline.EffectObservation{}, err
	}
	observation.TrustedOrigin = false
	if err := r.origin.VerifyOfflineEffectSource(ctx, binding.SourceCommit); err != nil {
		return offline.EffectObservation{}, offline.ErrEffectProofMissing
	}
	observation.TrustedOrigin = true
	observation.EvidenceSHA256 = offlineEffectDigest("github-protected-source", observation.EvidenceSHA256, "antiartificial/norn-fleet", "main", binding.SourceCommit)
	return observation, nil
}
