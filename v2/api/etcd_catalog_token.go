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

const etcdIssueCatalogTokenArgument = "--norn-etcd-issue-catalog-token"
const etcdCatalogTokenFileEnv = "NORN_ETCD_CATALOG_TOKEN_FILE"
const catalogTokenSubject = "work-staging-catalog-operator"
const catalogTokenTTL = 30 * time.Minute

// runEtcdCatalogTokenIssue is an offline, owner-run command. It never opens an
// HTTP issuance route. An already bootstrapped, exact-signing-key etcd prefix
// is required; the initial bootstrap credential need not remain valid.
func runEtcdCatalogTokenIssue(args []string, cfg *config.Config, backend startup.ControlBackendConfig) (bool, error) {
	if len(args) != 1 || args[0] != etcdIssueCatalogTokenArgument {
		return false, nil
	}
	if !catalogTokenIssuerMode(cfg, backend) {
		return true, errors.New("catalog token issuance requires authenticated normal Fleet etcd")
	}
	output := os.Getenv(etcdCatalogTokenFileEnv)
	if err := validateCatalogTokenOutput(output); err != nil {
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
	return true, issueCatalogToken(ctx, cfg.APIToken, etcdstore.NewAuthStore(client, backend.EtcdPrefix), output)
}

func catalogTokenIssuerMode(cfg *config.Config, backend startup.ControlBackendConfig) bool {
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

func validateCatalogTokenOutput(output string) error {
	if !filepath.IsAbs(output) || filepath.Clean(output) != output || filepath.Base(output) != "catalog-operator.token" {
		return fmt.Errorf("%s must be an absolute new catalog-operator.token path", etcdCatalogTokenFileEnv)
	}
	parent := filepath.Dir(output)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("catalog token parent must be an owner-only directory")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return errors.New("catalog token parent must be an owner-only directory")
	}
	if _, err := os.Lstat(output); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("catalog token output must not exist")
	}
	return nil
}

func issueCatalogToken(ctx context.Context, secret string, identities store.AuthStore, output string) (err error) {
	if err := validateCatalogTokenOutput(output); err != nil {
		return err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("catalog token output cannot be reserved")
	}
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(output)
		}
	}()
	token, record, err := handler.IssueManagedAccessToken(ctx, secret, identities, catalogTokenSubject,
		[]string{handler.ScopeAPIRead, handler.ScopeCatalogActivate}, catalogTokenTTL)
	if err != nil {
		return errors.New("catalog token could not be recorded")
	}
	published := false
	defer func() {
		if !published {
			revokeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, revokeErr := identities.RevokeAccessToken(revokeCtx, record.JTI); revokeErr != nil {
				err = errors.Join(err, errors.New("unpublished catalog token revocation unconfirmed"))
			}
		}
	}()
	if _, err = file.WriteString(token + "\n"); err != nil {
		return errors.New("catalog token publication failed")
	}
	if err = file.Sync(); err != nil {
		return errors.New("catalog token fsync failed")
	}
	if err = file.Close(); err != nil {
		return errors.New("catalog token close failed")
	}
	directory, openErr := os.Open(filepath.Dir(output))
	if openErr != nil {
		return errors.New("catalog token directory sync unavailable")
	}
	if syncErr := directory.Sync(); syncErr != nil {
		_ = directory.Close()
		return errors.New("catalog token directory sync failed")
	}
	_ = directory.Close()
	published = true
	return nil
}
