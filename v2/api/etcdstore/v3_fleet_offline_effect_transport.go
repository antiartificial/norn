package etcdstore

import (
	"context"
	"time"

	"norn/v2/api/fleet/offline"
)

// OfflineEffectAuthority is the narrow transport boundary. Callers cannot
// provide an observation or backend proof in an HTTP request. Constructing it
// requires independently authenticated, read-only authority clients.
type OfflineEffectAuthority struct {
	store    *V3OperationStore
	observer offlineEffectReobserver
	backend  offline.LatestBackendReader
}

// NewOfflineEffectRecovery exposes only immutable grant history. It cannot
// issue or consume and remains usable if observer credentials rotate after a
// response was lost.
func NewOfflineEffectRecovery(store *V3OperationStore) *OfflineEffectAuthority {
	if store == nil {
		return nil
	}
	return &OfflineEffectAuthority{store: store}
}

// OfflineEffectReaders are authority-owned clients; no runner-supplied
// receipt, provider credential, or provider mutation capability belongs here.
type OfflineEffectReaders struct {
	Origin interface {
		VerifyOfflineEffectSource(context.Context, string) error
	}
	Provider interface {
		ObserveInventory(context.Context) (string, []OfflineProviderDroplet, error)
	}
	Backend  offline.LatestBackendReader
	Workload offline.WorkloadSnapshotSource
	Epoch    interface {
		ReadOfflineEffectEpoch(context.Context) (int64, error)
	}
	Topology OfflineProviderTopology
	Clock    func() time.Time
}

// OfflineProviderDroplet and OfflineProviderTopology expose only read-only
// observation data to the authority composition point.
type OfflineProviderDroplet = offlineProviderDroplet
type OfflineProviderTopology = offlineProviderTopology

func NewOfflineEffectAuthority(store *V3OperationStore, readers OfflineEffectReaders) (*OfflineEffectAuthority, error) {
	if store == nil {
		return nil, offline.ErrEffectProofMissing
	}
	observer, err := newOfflineEffectReobserver(offlineEffectObserverDependencies{
		origin: readers.Origin, provider: readers.Provider, topology: readers.Topology,
		backend: readers.Backend, workload: readers.Workload, epoch: readers.Epoch, clock: readers.Clock,
	})
	if err != nil {
		return nil, err
	}
	return &OfflineEffectAuthority{store: store, observer: observer, backend: readers.Backend}, nil
}

func (a *OfflineEffectAuthority) Issue(ctx context.Context, planID, attemptID, principal string, now time.Time) (offline.EffectGrant, error) {
	if a == nil || a.store == nil || a.observer == nil {
		return offline.EffectGrant{}, offline.ErrEffectProofMissing
	}
	return a.store.issueOfflineEffectGrant(ctx, planID, attemptID, principal, now, a.observer)
}

func (a *OfflineEffectAuthority) Consume(ctx context.Context, planID, attemptID, grantID, principal string, now time.Time) (offline.EffectGrant, error) {
	if a == nil || a.store == nil || a.backend == nil {
		return offline.EffectGrant{}, offline.ErrEffectProofMissing
	}
	return a.store.consumeOfflineEffectGrant(ctx, planID, attemptID, grantID, principal, now, a.backend)
}

func (a *OfflineEffectAuthority) Recover(ctx context.Context, planID, attemptID, grantID, principal string) (offline.EffectGrant, error) {
	grant, err := a.RecoverAttempt(ctx, planID, attemptID, principal)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if grant.ID != grantID {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	return grant, nil
}

func (a *OfflineEffectAuthority) RecoverAttempt(ctx context.Context, planID, attemptID, principal string) (offline.EffectGrant, error) {
	if a == nil || a.store == nil {
		return offline.EffectGrant{}, offline.ErrEffectProofMissing
	}
	grant, err := a.store.GetOfflineEffectGrant(ctx, planID)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	attempt, err := a.store.GetOfflineAttempt(ctx, planID)
	if err != nil || grant.AttemptID != attemptID || attempt.ID != attemptID ||
		principal == "" || attempt.PrincipalSubject != principal || attempt.BindingSHA256 != grant.BindingSHA256 ||
		attempt.FenceGeneration != grant.FenceGeneration || attempt.TargetID != grant.TargetID {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	return grant, nil
}
