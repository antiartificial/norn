package etcdstore_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestAuthStoreConformance_Etcd runs the whole auth aggregate against the etcd
// adapter: the boundary-local IdentityStore and ExecSessionStore suites plus the
// cross-boundary aggregate suite that pins the ADR 0007 atomic-revocation
// invariant. Passing proves identity + exec-sessions run on etcd with
// revoke-plus-cancel committed as one multi-key transaction. Opt-in via
// NORN_TEST_ETCD_ENDPOINTS.
func TestAuthStoreConformance_Etcd(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	cli, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	prefix := "/norn-conf/auth/" + uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Delete(ctx, prefix, clientv3.WithPrefix()); err != nil {
		t.Fatal(err)
	}
	storetest.RunAuthAggregateConformance(t, func(t *testing.T) store.AuthStore {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := cli.Delete(ctx, prefix, clientv3.WithPrefix()); err != nil {
			t.Fatal(err)
		}
		return etcdstore.NewAuthStore(cli, prefix)
	})
}
