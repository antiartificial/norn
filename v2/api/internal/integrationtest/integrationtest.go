// Package integrationtest gives new Fleet controller suites one place to
// read their required test services. It never falls back to an in-memory
// fake: a helper either connects to the real service or fails the test. See
// docs/v3/fleet-controller/plan.md, "Conventions (all WPs)" (WP1).
package integrationtest

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/startup"
	"norn/v2/api/store"
)

// RequireIntegrationEnv is set by v2/scripts/go-test-strict before every run.
// While it is set, a missing test service is a hard failure (t.Fatal)
// instead of a skip, so a regex that only ever matches skipped tests cannot
// pass silently.
const RequireIntegrationEnv = "NORN_TEST_REQUIRE_INTEGRATION"

func missing(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv(RequireIntegrationEnv) != "" {
		t.Fatal(reason)
	}
	t.Skip(reason)
}

// PG connects to NORN_TEST_DATABASE_URL, migrates a schema scoped to this
// test, and returns the durable store. The schema is dropped in cleanup.
func PG(t *testing.T) *store.DB {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("NORN_TEST_DATABASE_URL"))
	if databaseURL == "" {
		missing(t, "NORN_TEST_DATABASE_URL is not set")
		return nil
	}
	ctx := context.Background()
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schemaName := "integrationtest_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE") })
	testConfig := adminConfig.Copy()
	if testConfig.ConnConfig.RuntimeParams == nil {
		testConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	testConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	if testConfig.MaxConns < 4 {
		testConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(ctx, testConfig)
	if err != nil {
		t.Fatal(err)
	}
	db := &store.DB{Pool: pool}
	t.Cleanup(db.Close)
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// Etcd connects to the TLS RBAC fixture named by NORN_TEST_ETCD_TLS_* when
// NORN_TEST_ETCD_TLS_ENDPOINTS is set, otherwise to the plain endpoints in
// NORN_TEST_ETCD_ENDPOINTS. It returns a client and a key prefix unique to
// this test; both are torn down in cleanup.
func Etcd(t *testing.T) (*clientv3.Client, string) {
	t.Helper()
	client := EtcdClient(t)
	if client == nil {
		return nil, ""
	}
	_, prefixBase, _ := etcdBackend()
	prefix := strings.TrimSuffix(prefixBase, "/") + "/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	return client, prefix
}

// EtcdClient returns one more independent client to the same etcd as Etcd,
// with the same TLS and RBAC settings, closed in cleanup. Suites that stand in
// for several API processes use it instead of dialing plain endpoints.
func EtcdClient(t *testing.T) *clientv3.Client {
	t.Helper()
	backend, _, ok := etcdBackend()
	if !ok {
		missing(t, "neither NORN_TEST_ETCD_TLS_ENDPOINTS nor NORN_TEST_ETCD_ENDPOINTS is set")
		return nil
	}
	tlsConfig, err := backend.EtcdTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := clientv3.New(clientv3.Config{
		Endpoints: backend.EtcdEndpoints, DialTimeout: 5 * time.Second,
		TLS: tlsConfig, Username: backend.EtcdUsername, Password: backend.EtcdPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func etcdBackend() (startup.ControlBackendConfig, string, bool) {
	if endpoints := splitEtcdEndpoints(os.Getenv("NORN_TEST_ETCD_TLS_ENDPOINTS")); len(endpoints) > 0 {
		prefixBase := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_TLS_PREFIX"))
		if prefixBase == "" {
			prefixBase = "/norn-test"
		}
		return startup.ControlBackendConfig{
			Backend: startup.BackendEtcd, EtcdEndpoints: endpoints,
			EtcdCAFile:   strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_TLS_CA_FILE")),
			EtcdCertFile: strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_TLS_CERT_FILE")),
			EtcdKeyFile:  strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_TLS_KEY_FILE")),
			EtcdUsername: strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_TLS_USERNAME")),
			EtcdPassword: os.Getenv("NORN_TEST_ETCD_TLS_PASSWORD"),
		}, prefixBase, true
	}
	if endpoints := splitEtcdEndpoints(os.Getenv("NORN_TEST_ETCD_ENDPOINTS")); len(endpoints) > 0 {
		return startup.ControlBackendConfig{Backend: startup.BackendEtcd, EtcdEndpoints: endpoints}, "/norn-test", true
	}
	return startup.ControlBackendConfig{}, "", false
}

func splitEtcdEndpoints(raw string) []string {
	var endpoints []string
	for _, endpoint := range strings.Split(raw, ",") {
		if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints
}
