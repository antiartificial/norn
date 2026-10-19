package store

import "context"

// DesiredReplicaReader exposes explicit operator scale intent to deployment
// translation without tying callers to PostgreSQL.
type DesiredReplicaReader interface {
	DesiredReplicaCounts(context.Context, string, string) (map[string]int, error)
}

// FencedDesiredReplicaWriter is the atomic completion boundary required by an
// etcd app.scale worker. The implementation must persist the desired count and
// terminal operation receipt in one transaction while the claim, app lock, and
// control authority are all current.
type FencedDesiredReplicaWriter interface {
	FinishScaleClaimedOperationWithAppLock(context.Context, OperationClaim, AppOperationLock, string, string, string, string, int, string, map[string]interface{}) error
}

var _ DesiredReplicaReader = (*DB)(nil)
