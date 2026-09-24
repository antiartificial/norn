package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/startup"
)

const etcdManagedCredentialBootstrapArgument = "--norn-etcd-bootstrap"

// runEtcdManagedCredentialBootstrap creates the first revocable API credential
// for an empty Norn prefix. It writes the opaque token only to an exclusively
// created owner-only file; logs and stdout contain no credential material.
func runEtcdManagedCredentialBootstrap(args []string, cfg *config.Config, backend startup.ControlBackendConfig) (bool, error) {
	if len(args) != 1 || args[0] != etcdManagedCredentialBootstrapArgument {
		return false, nil
	}
	if cfg == nil || backend.Backend != startup.BackendEtcd || backend.SourceValidation {
		return true, fmt.Errorf("%s requires normal NORN_CONTROL_BACKEND=etcd", etcdManagedCredentialBootstrapArgument)
	}
	output := strings.TrimSpace(os.Getenv(startup.EtcdBootstrapTokenFileEnv))
	subject := strings.TrimSpace(os.Getenv(startup.EtcdBootstrapSubjectEnv))
	scopes := splitBootstrapScopes(os.Getenv(startup.EtcdBootstrapScopesEnv))
	ttl, err := time.ParseDuration(strings.TrimSpace(os.Getenv(startup.EtcdBootstrapTTLEnv)))
	if output == "" || !filepath.IsAbs(output) {
		return true, fmt.Errorf("%s must be an absolute, new token file path", startup.EtcdBootstrapTokenFileEnv)
	}
	if subject == "" || len(scopes) == 0 || err != nil || ttl <= 0 || ttl > 72*time.Hour {
		return true, fmt.Errorf("%s, %s, and a 1ns..72h %s are required", startup.EtcdBootstrapSubjectEnv, startup.EtcdBootstrapScopesEnv, startup.EtcdBootstrapTTLEnv)
	}
	if !cfg.RequireExplicitAuth || len(cfg.APIToken) < 32 {
		return true, fmt.Errorf("bootstrap requires NORN_REQUIRE_EXPLICIT_AUTH=true and a 32-byte NORN_API_TOKEN")
	}
	client, err := newEtcdClient(backend)
	if err != nil {
		return true, err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entries, err := client.Get(ctx, backend.EtcdPrefix, clientv3.WithPrefix(), clientv3.WithLimit(1))
	if err != nil {
		return true, fmt.Errorf("check empty etcd prefix: %w", err)
	}
	if len(entries.Kvs) != 0 {
		return true, fmt.Errorf("refusing bootstrap: %s is not empty", startup.EtcdPrefixEnv)
	}
	marker := strings.TrimRight(backend.EtcdPrefix, "/") + "/v3/bootstrap/initial-managed-credential"
	reservation, err := client.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(marker), "=", 0)).Then(clientv3.OpPut(marker, subject)).Commit()
	if err != nil {
		return true, fmt.Errorf("reserve bootstrap: %w", err)
	}
	if !reservation.Succeeded {
		return true, fmt.Errorf("refusing bootstrap: initial managed credential is already reserved")
	}
	identities := etcdstore.NewAuthStore(client, backend.EtcdPrefix)
	token, record, err := handler.IssueManagedAccessToken(ctx, cfg.APIToken, identities, subject, scopes, ttl)
	if err != nil {
		_, _ = client.Delete(context.Background(), marker)
		return true, fmt.Errorf("issue managed credential: %w", err)
	}
	cleanup := func() {
		_, _ = identities.RevokeAccessToken(context.Background(), record.JTI)
		_, _ = client.Delete(context.Background(), marker)
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		cleanup()
		return true, fmt.Errorf("create %s: %w", startup.EtcdBootstrapTokenFileEnv, err)
	}
	if _, err = file.WriteString(token + "\n"); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(output)
		cleanup()
		return true, fmt.Errorf("write bootstrap token: %w", err)
	}
	if closeErr != nil {
		_ = os.Remove(output)
		cleanup()
		return true, fmt.Errorf("close bootstrap token: %w", closeErr)
	}
	return true, nil
}

func splitBootstrapScopes(raw string) []string {
	var scopes []string
	for _, scope := range strings.Split(raw, ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}
