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

func etcdClientForTest(t *testing.T) *clientv3.Client {
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

func resetPrefix(t *testing.T, cli *clientv3.Client, prefix string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Delete(ctx, prefix, clientv3.WithPrefix()); err != nil {
		t.Fatalf("reset etcd prefix: %v", err)
	}
}

// TestNotificationStoreConformance_Etcd runs the shared notification suite
// against the etcd adapter. Opt-in via NORN_TEST_ETCD_ENDPOINTS.
func TestNotificationStoreConformance_Etcd(t *testing.T) {
	cli := etcdClientForTest(t)
	const prefix = "/norn-conf/notify"
	storetest.RunNotificationStoreConformance(t, func(t *testing.T) store.NotificationStore {
		resetPrefix(t, cli, prefix)
		return etcdstore.NewNotificationStore(cli, prefix)
	})
}

// TestWebhookStoreConformance_Etcd runs the shared webhook suite against the
// etcd adapter. Opt-in via NORN_TEST_ETCD_ENDPOINTS.
func TestWebhookStoreConformance_Etcd(t *testing.T) {
	cli := etcdClientForTest(t)
	const prefix = "/norn-conf/webhook"
	storetest.RunWebhookStoreConformance(t, func(t *testing.T) store.WebhookStore {
		resetPrefix(t, cli, prefix)
		return etcdstore.NewWebhookStore(cli, prefix)
	})
}
