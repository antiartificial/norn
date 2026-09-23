package controlstore_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/controlstore"
	"norn/v2/api/hub"
	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

func TestConfigFromEnvDefaultsToPostgres(t *testing.T) {
	t.Setenv("NORN_CONTROL_BACKEND", "")
	t.Setenv("NORN_ETCD_PREFIX", "")
	t.Setenv("NORN_ETCD_ENDPOINTS", "")
	cfg := controlstore.ConfigFromEnv()
	if cfg.Backend != controlstore.BackendPostgres {
		t.Fatalf("default backend = %q, want postgres", cfg.Backend)
	}
	if cfg.EtcdPrefix != "/norn" {
		t.Fatalf("default etcd prefix = %q, want /norn", cfg.EtcdPrefix)
	}
}

func TestNewRejectsBadConfigs(t *testing.T) {
	if _, _, err := controlstore.New(nil, controlstore.Config{Backend: controlstore.BackendPostgres}); err == nil {
		t.Fatal("expected error: postgres backend with nil *store.DB")
	}
	if _, _, err := controlstore.New(nil, controlstore.Config{Backend: controlstore.BackendEtcd}); err == nil {
		t.Fatal("expected error: etcd backend without endpoints")
	}
	if _, _, err := controlstore.New(nil, controlstore.Config{Backend: "bogus"}); err == nil {
		t.Fatal("expected error: unknown backend")
	}
}

// TestControlStore_Etcd proves the composed etcd ControlStore satisfies every
// boundary by running all five shared conformance suites through it. Opt-in via
// NORN_TEST_ETCD_ENDPOINTS.
func TestControlStore_Etcd(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	const prefix = "/norn-cs"
	cfg := controlstore.Config{
		Backend:       controlstore.BackendEtcd,
		EtcdEndpoints: strings.Split(endpoints, ","),
		EtcdPrefix:    prefix,
	}
	cs, closer, err := controlstore.New(nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	wipeCli, err := clientv3.New(clientv3.Config{Endpoints: cfg.EtcdEndpoints, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wipeCli.Close() })
	wipe := func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := wipeCli.Delete(ctx, prefix, clientv3.WithPrefix()); err != nil {
			t.Fatalf("reset control store: %v", err)
		}
	}

	storetest.RunOperationStoreConformance(t, func(t *testing.T) store.OperationStore { wipe(t); return cs })
	storetest.RunDeploymentStoreConformance(t, func(t *testing.T) store.DeploymentStore { wipe(t); return cs })
	storetest.RunEventStoreConformance(t, func(t *testing.T) hub.EventStore { wipe(t); return cs })
	storetest.RunFleetAttemptStoreConformance(t, func(t *testing.T) store.FleetAttemptStore { wipe(t); return cs })
	storetest.RunMutationAuditStoreConformance(t, func(t *testing.T) store.MutationAuditStore { wipe(t); return cs })

	// The full store surface routes through the same composed ControlStore.
	storetest.RunIdentityStoreConformance(t, func(t *testing.T) store.IdentityStore { wipe(t); return cs })
	storetest.RunExecSessionStoreConformance(t,
		func(t *testing.T) store.ExecSessionStore { wipe(t); return cs },
		func(t *testing.T, deviceID string) {
			if err := cs.CreateAccessDevice(context.Background(), &store.AccessDevice{ID: deviceID, Name: "cs-device", CreatedAt: time.Now()}); err != nil {
				t.Fatalf("register device: %v", err)
			}
		})
	storetest.RunAuthAggregateConformance(t, func(t *testing.T) store.AuthStore { wipe(t); return cs })
	storetest.RunNotificationStoreConformance(t, func(t *testing.T) store.NotificationStore { wipe(t); return cs })
	storetest.RunWebhookStoreConformance(t, func(t *testing.T) store.WebhookStore { wipe(t); return cs })
	storetest.RunCronStoreConformance(t, func(t *testing.T) store.CronStore { wipe(t); return cs })
	storetest.RunFuncExecutionStoreConformance(t, func(t *testing.T) store.FuncExecutionStore { wipe(t); return cs })
	storetest.RunRecoveryDrillStoreConformance(t, func(t *testing.T) store.RecoveryDrillStore { wipe(t); return cs })
	storetest.RunAccessPatternStoreConformance(t, func(t *testing.T) store.AccessPatternStore { wipe(t); return cs })
	storetest.RunFleetGitHubDispatchStoreConformance(t, func(t *testing.T) store.FleetGitHubDispatchStore { wipe(t); return cs })
	storetest.RunBeaconStoreConformance(t, func(t *testing.T) store.BeaconStore { wipe(t); return cs })
}
