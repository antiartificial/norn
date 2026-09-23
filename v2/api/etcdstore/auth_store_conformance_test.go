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
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   strings.Split(endpoints, ","),
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	const prefix = "/norn-conf/auth"
	reset := func(t *testing.T) *etcdstore.AuthStore {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := cli.Delete(ctx, prefix, clientv3.WithPrefix()); err != nil {
			t.Fatalf("reset etcd prefix: %v", err)
		}
		return etcdstore.NewAuthStore(cli, prefix)
	}

	t.Run("Identity", func(t *testing.T) {
		storetest.RunIdentityStoreConformance(t, func(t *testing.T) store.IdentityStore { return reset(t) })
	})
	t.Run("ExecSession", func(t *testing.T) {
		storetest.RunExecSessionStoreConformance(t,
			func(t *testing.T) store.ExecSessionStore { return reset(t) },
			func(t *testing.T, deviceID string) {
				// Register the device on the same prefix WITHOUT resetting it.
				s := etcdstore.NewAuthStore(cli, prefix)
				if err := s.CreateAccessDevice(context.Background(), &store.AccessDevice{ID: deviceID, Name: "conf-device", CreatedAt: time.Now()}); err != nil {
					t.Fatalf("register device: %v", err)
				}
			})
	})
	t.Run("Aggregate", func(t *testing.T) {
		storetest.RunAuthAggregateConformance(t, func(t *testing.T) store.AuthStore { return reset(t) })
	})
}
