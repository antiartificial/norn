package etcdstore_test

import (
	"testing"

	"norn/v2/api/etcdstore"
	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestFleetGitHubDispatchStoreConformance_Etcd runs the shared dispatch suite
// against the etcd adapter. Opt-in via NORN_TEST_ETCD_ENDPOINTS.
func TestFleetGitHubDispatchStoreConformance_Etcd(t *testing.T) {
	cli := etcdClientForTest(t)
	const prefix = "/norn-conf/ghdispatch"
	storetest.RunFleetGitHubDispatchStoreConformance(t, func(t *testing.T) store.FleetGitHubDispatchStore {
		resetPrefix(t, cli, prefix)
		return etcdstore.NewFleetGitHubDispatchStore(cli, prefix)
	})
}
