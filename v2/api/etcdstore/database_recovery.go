package etcdstore

import (
	"context"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

// RecoveryInspector is an etcd-native store.DatabaseRecoveryInspector. It does
// NOT run the PostgreSQL archive/WAL/pg_stat_replication probe — those are
// PostgreSQL concepts with no etcd equivalent. Instead it reports etcd cluster
// membership as the durability posture: an etcd control backend's recovery model
// is quorum + periodic snapshots, not WAL archiving, so PITREnabled() is false by
// construction (the PG-only archive fields stay empty) and StreamingReplicas
// carries the count of started cluster members. This keeps readiness
// provider-independent without a mandatory SQL probe.
type RecoveryInspector struct {
	cluster clientv3.Cluster
}

// NewRecoveryInspector returns an etcd readiness inspector backed by the client's
// Cluster API.
func NewRecoveryInspector(cluster clientv3.Cluster) *RecoveryInspector {
	return &RecoveryInspector{cluster: cluster}
}

var _ store.DatabaseRecoveryInspector = (*RecoveryInspector)(nil)

func (r *RecoveryInspector) InspectDatabaseRecovery(ctx context.Context) (*store.DatabaseRecoveryStatus, error) {
	resp, err := r.cluster.MemberList(ctx)
	if err != nil {
		return nil, err
	}
	started := 0
	for _, m := range resp.Members {
		// A member with no started client URLs has not yet joined; count only
		// members that are actually serving.
		if !m.IsLearner && len(m.ClientURLs) > 0 {
			started++
		}
	}
	return &store.DatabaseRecoveryStatus{
		// Archive/WAL fields are PostgreSQL-only; left empty so PITREnabled() is
		// false. etcd durability is snapshot-based, reported by the runtime's own
		// etcd health/snapshot checks, not this probe.
		StreamingReplicas: started,
	}, nil
}
