package etcdstore_test

import (
	"testing"

	"norn/v2/api/etcdstore"
	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestCronStoreConformance_Etcd runs the shared cron-store suite against the
// etcd adapter. Opt-in via NORN_TEST_ETCD_ENDPOINTS.
func TestCronStoreConformance_Etcd(t *testing.T) {
	cli := etcdClientForTest(t)
	const prefix = "/norn-conf/cron"
	storetest.RunCronStoreConformance(t, func(t *testing.T) store.CronStore {
		resetPrefix(t, cli, prefix)
		return etcdstore.NewCronStore(cli, prefix)
	})
}

// TestFuncExecutionStoreConformance_Etcd runs the shared func-execution suite
// against the etcd adapter. Opt-in via NORN_TEST_ETCD_ENDPOINTS.
func TestFuncExecutionStoreConformance_Etcd(t *testing.T) {
	cli := etcdClientForTest(t)
	const prefix = "/norn-conf/func"
	storetest.RunFuncExecutionStoreConformance(t, func(t *testing.T) store.FuncExecutionStore {
		resetPrefix(t, cli, prefix)
		return etcdstore.NewFuncExecutionStore(cli, prefix)
	})
}
