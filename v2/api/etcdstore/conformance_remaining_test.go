package etcdstore_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// etcdClientOrSkip returns a client to the test etcd cluster, or skips.
func etcdClientOrSkip(t *testing.T) *clientv3.Client {
	t.Helper()
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   strings.Split(endpoints, ","),
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func wipeEtcd(t *testing.T, cli *clientv3.Client, prefix string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Delete(ctx, prefix, clientv3.WithPrefix()); err != nil {
		t.Fatalf("reset etcd prefix %s: %v", prefix, err)
	}
}

func TestDeploymentStoreConformance_Etcd(t *testing.T) {
	cli := etcdClientOrSkip(t)
	const prefix = "/norn-conf/deploy"
	storetest.RunDeploymentStoreConformance(t, func(t *testing.T) store.DeploymentStore {
		wipeEtcd(t, cli, prefix)
		return etcdstore.NewDeploymentStore(cli, prefix)
	})
}

func TestFleetAttemptStoreConformance_Etcd(t *testing.T) {
	cli := etcdClientOrSkip(t)
	const prefix = "/norn-conf/fleet"
	storetest.RunFleetAttemptStoreConformance(t, func(t *testing.T) store.FleetAttemptStore {
		wipeEtcd(t, cli, prefix)
		return etcdstore.NewFleetAttemptStore(cli, prefix)
	})
}

func TestMutationAuditStoreConformance_Etcd(t *testing.T) {
	cli := etcdClientOrSkip(t)
	const prefix = "/norn-conf/maudit"
	storetest.RunMutationAuditStoreConformance(t, func(t *testing.T) store.MutationAuditStore {
		wipeEtcd(t, cli, prefix)
		return etcdstore.NewMutationAuditStore(cli, prefix)
	})
}
