package etcdstore

import (
	"context"
	"time"

	"norn/v2/api/fleet/offline"
)

// offlineEffectEpochReader reads the authority epoch independently of the
// runner. Grant issuance compares it with the held fence in its final CAS.
type offlineEffectEpochReader interface {
	ReadOfflineEffectEpoch(context.Context) (int64, error)
}

// offlineEffectObserverDependencies are authority-owned read-only clients.
// This is deliberately private: there is no transport accepting runner proof.
type offlineEffectObserverDependencies struct {
	origin   offlineEffectSourceVerifier
	provider offlineProviderInventoryReader
	topology offlineProviderTopology
	backend  offline.LatestBackendReader
	workload offline.WorkloadSnapshotSource
	epoch    offlineEffectEpochReader
	clock    func() time.Time
}

// newOfflineEffectReobserver composes the independent proof slices. Missing
// live clients cannot be replaced with a runner-supplied observation.
func newOfflineEffectReobserver(d offlineEffectObserverDependencies) (offlineEffectReobserver, error) {
	if d.origin == nil || d.provider == nil || d.backend == nil || d.workload == nil || d.epoch == nil || d.clock == nil ||
		d.topology.AccountUUID == "" || d.topology.ProjectID == "" || d.topology.VPCUUID == "" || len(d.topology.RequiredTags) == 0 {
		return nil, offline.ErrEffectProofMissing
	}
	var observer offlineEffectReobserver = workloadCheckedEffectReobserver{source: d.workload, epoch: d.epoch, clock: d.clock}
	observer = backendCheckedEffectReobserver{remaining: observer, backend: d.backend}
	observer = providerCheckedEffectReobserver{remaining: observer, provider: d.provider, topology: d.topology}
	observer = originCheckedEffectReobserver{remaining: observer, origin: d.origin}
	return observer, nil
}

type workloadCheckedEffectReobserver struct {
	source offline.WorkloadSnapshotSource
	epoch  offlineEffectEpochReader
	clock  func() time.Time
}

func (r workloadCheckedEffectReobserver) reobserveEffect(ctx context.Context, b offline.Binding, a offline.Attempt) (offline.EffectObservation, error) {
	if r.source == nil || r.epoch == nil || r.clock == nil || b.Validate() != nil ||
		a.PlanID != b.PlanID || a.TargetID != b.TargetID || a.FenceGeneration != b.FenceGeneration || a.BindingSHA256 == "" {
		return offline.EffectObservation{}, offline.ErrEffectProofMissing
	}
	now := r.clock().UTC()
	evidence, err := offline.ObserveWorkload(ctx, r.source, now)
	if err != nil {
		return offline.EffectObservation{}, offline.ErrEffectProofMissing
	}
	epoch, err := r.epoch.ReadOfflineEffectEpoch(ctx)
	if err != nil || epoch < 1 {
		return offline.EffectObservation{}, offline.ErrEffectProofMissing
	}
	return offline.EffectObservation{PlanID: b.PlanID, AttemptID: a.ID, BindingSHA256: a.BindingSHA256,
		TargetID: b.TargetID, SourceCommit: b.SourceCommit, SavedPlanSHA256: b.SavedPlanSHA256,
		EffectSHA256: b.EffectSHA256, Backend: b.Backend, ProviderAccountUUID: b.ProviderAccountUUID,
		NodeName: b.NodeName, DropletID: b.DropletID, FenceGeneration: b.FenceGeneration,
		AuthorityEpoch: epoch, ObservedAt: evidence.ObservedAt, EvidenceSHA256: evidence.SHA256,
		AllNamespacesInspected: evidence.AllNamespacesInspected, App03Empty: evidence.App03Empty,
		SurvivingPlacement: evidence.SurvivingPlacement, SurvivingHeadroom: evidence.SurvivingHeadroom,
		ServicePassing: evidence.ServicePassing}, nil
}
