package etcdstore_test

import (
	"testing"

	"norn/v2/api/etcdstore"
	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestBeaconStoreConformance_Etcd runs the shared beacon suite against the etcd
// adapter. Opt-in via NORN_TEST_ETCD_ENDPOINTS.
func TestBeaconStoreConformance_Etcd(t *testing.T) {
	cli := etcdClientForTest(t)
	const prefix = "/norn-conf/beacon"
	storetest.RunBeaconStoreConformance(t, func(t *testing.T) store.BeaconStore {
		resetPrefix(t, cli, prefix)
		return etcdstore.NewBeaconStore(cli, prefix)
	})
}
