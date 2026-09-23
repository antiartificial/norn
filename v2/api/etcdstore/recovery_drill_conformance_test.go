package etcdstore_test

import (
	"testing"

	"norn/v2/api/etcdstore"
	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestRecoveryDrillStoreConformance_Etcd runs the shared recovery-drill suite
// against the etcd adapter. Opt-in via NORN_TEST_ETCD_ENDPOINTS.
func TestRecoveryDrillStoreConformance_Etcd(t *testing.T) {
	cli := etcdClientForTest(t)
	const prefix = "/norn-conf/drill"
	storetest.RunRecoveryDrillStoreConformance(t, func(t *testing.T) store.RecoveryDrillStore {
		resetPrefix(t, cli, prefix)
		return etcdstore.NewRecoveryDrillStore(cli, prefix)
	})
}
