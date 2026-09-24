package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/startup"
)

func TestEtcdManagedCredentialBootstrapCreatesOnlyOneOwnerOnlyTokenFile(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	prefix := "/norn-test/bootstrap/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	secret := "etcd-bootstrap-managed-token-secret-000"
	output := filepath.Join(t.TempDir(), "initial-token")
	t.Setenv(startup.EtcdBootstrapTokenFileEnv, output)
	t.Setenv(startup.EtcdBootstrapSubjectEnv, "fleet-bootstrap")
	t.Setenv(startup.EtcdBootstrapScopesEnv, handler.ScopeAPIRead+","+handler.ScopeAPIWrite)
	t.Setenv(startup.EtcdBootstrapTTLEnv, "1h")
	backend := startup.ControlBackendConfig{Backend: startup.BackendEtcd, EtcdEndpoints: strings.Split(endpoints, ","), EtcdPrefix: prefix}
	handled, err := runEtcdManagedCredentialBootstrap([]string{etcdManagedCredentialBootstrapArgument}, &config.Config{APIToken: secret, RequireExplicitAuth: true}, backend)
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file info=%v err=%v", info, err)
	}
	tokenBytes, err := os.ReadFile(output)
	if err != nil || strings.TrimSpace(string(tokenBytes)) == "" {
		t.Fatalf("token output err=%v value=%q", err, tokenBytes)
	}
	if principal, ok := handler.VerifyAccessTokenWithIdentityStore(secret, strings.TrimSpace(string(tokenBytes)), time.Time{}, etcdstore.NewAuthStore(client, prefix)); !ok || !principal.Allows(handler.ScopeAPIWrite) {
		t.Fatalf("bootstrapped token did not verify: %#v ok=%v", principal, ok)
	}
	if _, err := runEtcdManagedCredentialBootstrap([]string{etcdManagedCredentialBootstrapArgument}, &config.Config{APIToken: secret, RequireExplicitAuth: true}, backend); err == nil {
		t.Fatal("second bootstrap unexpectedly succeeded")
	}
}

func TestEtcdManagedCredentialBootstrapRequiresExplicitArguments(t *testing.T) {
	handled, err := runEtcdManagedCredentialBootstrap([]string{etcdManagedCredentialBootstrapArgument}, &config.Config{}, startup.ControlBackendConfig{Backend: startup.BackendEtcd})
	if !handled || err == nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if handled, err := runEtcdManagedCredentialBootstrap(nil, nil, startup.ControlBackendConfig{}); handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}
