package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/startup"
	"norn/v2/api/store"
)

const etcdIssueAdminTokenArgument = "--norn-etcd-issue-admin-token"
const etcdAdminTokenFileEnv = "NORN_ETCD_ADMIN_TOKEN_FILE"
const adminTokenSubject = "work-staging-admin-operator"
const adminTokenTTL = 30 * time.Minute

// runEtcdAdminTokenIssue is an offline, owner-run command. It never opens an
// HTTP issuance route. An already bootstrapped, exact-signing-key etcd prefix
// is required; the initial bootstrap credential need not remain valid.
func runEtcdAdminTokenIssue(args []string, cfg *config.Config, backend startup.ControlBackendConfig) (bool, error) {
	if len(args) != 1 || args[0] != etcdIssueAdminTokenArgument {
		return false, nil
	}
	if !adminTokenIssuerMode(cfg, backend) {
		return true, errors.New("admin token issuance requires authenticated normal Fleet etcd")
	}
	output := os.Getenv(etcdAdminTokenFileEnv)
	if err := validateAdminTokenOutput(output); err != nil {
		return true, err
	}
	client, err := newEtcdClient(backend)
	if err != nil {
		return true, err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := requireInitialEtcdBootstrap(ctx, client, backend.EtcdPrefix, cfg.APIToken); err != nil {
		return true, err
	}
	return true, issueAdminToken(ctx, cfg.APIToken, etcdstore.NewAuthStore(client, backend.EtcdPrefix), output)
}

func adminTokenIssuerMode(cfg *config.Config, backend startup.ControlBackendConfig) bool {
	if cfg == nil || backend.Backend != startup.BackendEtcd || backend.SourceValidation ||
		!cfg.EnvironmentExplicit || cfg.EnvironmentID() != "staging" ||
		!cfg.IsAppCatalogReadOnly() || !cfg.RequireExplicitAuth || len(cfg.APIToken) < 32 {
		return false
	}
	// The management authority uses the development security profile. Fleet
	// controls use the production profile and must not impersonate the authority
	// merely to issue this one scoped, registry-backed token.
	if cfg.IsFleetAuthorityOnly() {
		return cfg.ProfileID() == "development"
	}
	return cfg.Production()
}

func validateAdminTokenOutput(output string) error {
	if !filepath.IsAbs(output) || filepath.Clean(output) != output || filepath.Base(output) != "admin-operator.token" {
		return fmt.Errorf("%s must be an absolute new admin-operator.token path", etcdAdminTokenFileEnv)
	}
	parent := filepath.Dir(output)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("admin token parent must be an owner-only directory")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return errors.New("admin token parent must be an owner-only directory")
	}
	if _, err := os.Lstat(output); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("admin token output must not exist")
	}
	return nil
}

func issueAdminToken(ctx context.Context, secret string, identities store.AuthStore, output string) (err error) {
	if err := validateAdminTokenOutput(output); err != nil {
		return err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("admin token output cannot be reserved")
	}
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(output)
		}
	}()
	token, record, err := handler.IssueManagedAccessToken(ctx, secret, identities, adminTokenSubject,
		[]string{handler.ScopeAdmin}, adminTokenTTL)
	if err != nil {
		return errors.New("admin token could not be recorded")
	}
	published := false
	defer func() {
		if !published {
			revokeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, revokeErr := identities.RevokeAccessToken(revokeCtx, record.JTI); revokeErr != nil {
				err = errors.Join(err, errors.New("unpublished admin token revocation unconfirmed"))
			}
		}
	}()
	if _, err = file.WriteString(token + "\n"); err != nil {
		return errors.New("admin token publication failed")
	}
	if err = file.Sync(); err != nil {
		return errors.New("admin token fsync failed")
	}
	if err = file.Close(); err != nil {
		return errors.New("admin token close failed")
	}
	directory, openErr := os.Open(filepath.Dir(output))
	if openErr != nil {
		return errors.New("admin token directory sync unavailable")
	}
	if syncErr := directory.Sync(); syncErr != nil {
		_ = directory.Close()
		return errors.New("admin token directory sync failed")
	}
	_ = directory.Close()
	published = true
	return nil
}
