package etcdstore_test

import (
	"testing"

	"norn/v2/api/etcdstore"
	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestAccessPatternStoreConformance_Etcd runs the shared access-pattern suite
// against the etcd adapter. Opt-in via NORN_TEST_ETCD_ENDPOINTS.
func TestAccessPatternStoreConformance_Etcd(t *testing.T) {
	cli := etcdClientForTest(t)
	const prefix = "/norn-conf/access"
	storetest.RunAccessPatternStoreConformance(t, func(t *testing.T) store.AccessPatternStore {
		resetPrefix(t, cli, prefix)
		return etcdstore.NewAccessPatternStore(cli, prefix)
	})
}
