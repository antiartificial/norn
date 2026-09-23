package etcdstore_test

import (
	"context"
	"testing"

	"norn/v2/api/etcdstore"
)

// TestRecoveryInspector_Etcd verifies the etcd readiness inspector reports live
// cluster membership without running a PostgreSQL probe. Opt-in via
// NORN_TEST_ETCD_ENDPOINTS.
func TestRecoveryInspector_Etcd(t *testing.T) {
	cli := etcdClientForTest(t)
	inspector := etcdstore.NewRecoveryInspector(cli)
	status, err := inspector.InspectDatabaseRecovery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.StreamingReplicas < 1 {
		t.Fatalf("expected at least one started etcd member, got %d", status.StreamingReplicas)
	}
	// etcd durability is snapshot-based, not PG PITR; the archive fields stay
	// empty so PITREnabled() is false.
	if status.PITREnabled() {
		t.Fatal("etcd inspector must not report PostgreSQL PITR enabled")
	}
	if status.ArchiveMode != "" || status.WALLevel != "" {
		t.Fatalf("PG-only fields should be empty: %+v", status)
	}
}
