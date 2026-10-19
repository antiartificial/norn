package etcdstore

import (
	"context"
	"strings"
	"time"

	"norn/v2/api/fleet/offline"
)

type OfflineRunnerIdentity struct{ Subject, TokenID, DeviceID string }

func (i OfflineRunnerIdentity) valid() bool {
	return i.Subject != "" && i.TokenID != "" && i.DeviceID != "" &&
		!strings.ContainsAny(i.Subject+i.TokenID+i.DeviceID, " \t\r\n")
}

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

func (a *OfflineEffectAuthority) Issue(ctx context.Context, planID, attemptID string, identity OfflineRunnerIdentity, now time.Time) (offline.EffectGrant, error) {
	if a == nil || a.store == nil || a.observer == nil || !identity.valid() {
		return offline.EffectGrant{}, offline.ErrEffectProofMissing
	}
	return a.store.issueOfflineEffectGrant(ctx, planID, attemptID, identity, now, a.observer)
}

func (a *OfflineEffectAuthority) Consume(ctx context.Context, planID, attemptID, grantID string, identity OfflineRunnerIdentity, now time.Time) (offline.EffectGrant, error) {
	if a == nil || a.store == nil || a.backend == nil || !identity.valid() {
		return offline.EffectGrant{}, offline.ErrEffectProofMissing
	}
	return a.store.consumeOfflineEffectGrant(ctx, planID, attemptID, grantID, identity, now, a.backend)
}

func (a *OfflineEffectAuthority) Recover(ctx context.Context, planID, attemptID, grantID string, identity OfflineRunnerIdentity) (offline.EffectGrant, error) {
	grant, err := a.RecoverAttempt(ctx, planID, attemptID, identity)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if grant.ID != grantID {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	return grant, nil
}

func (a *OfflineEffectAuthority) RecoverAttempt(ctx context.Context, planID, attemptID string, identity OfflineRunnerIdentity) (offline.EffectGrant, error) {
	if a == nil || a.store == nil || !identity.valid() {
		return offline.EffectGrant{}, offline.ErrEffectProofMissing
	}
	return a.store.RecoverOfflineEffectGrant(ctx, planID, attemptID, identity)
}
