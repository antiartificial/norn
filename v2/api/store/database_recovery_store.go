package store

import "context"

// DatabaseRecoveryInspector is the control-backend readiness boundary: it reports
// the durability posture of the control store. On PostgreSQL this is archive
// mode / WAL level / streaming replica count (PITR readiness). It is deliberately
// backend-specific — an etcd control backend reports etcd-native readiness
// (member/quorum health, snapshot-based durability) rather than running the
// PostgreSQL SQL probe, per the v3 roadmap's "remove mandatory SQL probes from
// etcd deployments without weakening admission" (M3 / P5).
type DatabaseRecoveryInspector interface {
	InspectDatabaseRecovery(ctx context.Context) (*DatabaseRecoveryStatus, error)
}

// Compile-time proof that the PostgreSQL adapter satisfies the boundary.
var _ DatabaseRecoveryInspector = (*DB)(nil)
