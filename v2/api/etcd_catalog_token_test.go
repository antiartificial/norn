package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/startup"
	"norn/v2/api/store"
)

type catalogTokenStore struct {
	store.AuthStore
	record     *store.AccessToken
	revoked    string
	failRecord bool
}

func (s *catalogTokenStore) RecordAccessToken(_ context.Context, token *store.AccessToken) error {
	if s.failRecord {
		return errors.New("registry unavailable")
	}
	s.record = token
	return nil
}
func (s *catalogTokenStore) AccessTokenActive(_ context.Context, jti string) (bool, error) {
	return s.record != nil && s.record.JTI == jti && s.revoked == "", nil
}
func (s *catalogTokenStore) RevokeAccessToken(_ context.Context, jti string) ([]string, error) {
	s.revoked = jti
	return nil, nil
}

func TestIssueCatalogTokenPublishesOnlyExactScopesOwnerOnly(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "owner")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "catalog-operator.token")
	identities := &catalogTokenStore{}
	secret := strings.Repeat("k", 40)
	if err := issueCatalogToken(context.Background(), secret, identities, output); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("output mode=%v err=%v", info, err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	principal, ok := handler.VerifyAccessTokenWithIdentityStore(secret, strings.TrimSpace(string(raw)), time.Time{}, identities)
	if !ok || !principal.Allows(handler.ScopeAPIRead) || !principal.Allows(handler.ScopeCatalogActivate) || principal.Allows(handler.ScopeAPIWrite) || principal.Allows(handler.ScopePlatformOperate) || principal.CI != nil || identities.record.Subject != catalogTokenSubject || identities.record.ExpiresAt.Sub(identities.record.IssuedAt) != catalogTokenTTL {
		t.Fatal("catalog token did not retain exact non-CI scopes and TTL")
	}
	if err := issueCatalogToken(context.Background(), secret, identities, output); err == nil {
		t.Fatal("existing token file was replaced")
	}
}

func TestIssueCatalogTokenRequiresPrivateNewOutputAndEtcdMode(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "owner")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "catalog-operator.token")
	if err := validateCatalogTokenOutput(output); err == nil {
		t.Fatal("world-readable parent accepted")
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateCatalogTokenOutput(output); err != nil {
		t.Fatal(err)
	}
	t.Setenv(etcdCatalogTokenFileEnv, output)
	if handled, err := runEtcdCatalogTokenIssue([]string{etcdIssueCatalogTokenArgument}, &config.Config{}, startup.ControlBackendConfig{Backend: startup.BackendEtcd}); !handled || err == nil {
		t.Fatalf("unsafe mode: handled=%v err=%v", handled, err)
	}
	if handled, err := runEtcdCatalogTokenIssue(nil, nil, startup.ControlBackendConfig{}); handled || err != nil {
		t.Fatalf("ordinary runtime: handled=%v err=%v", handled, err)
	}
}

func TestCatalogTokenIssuerModeMatchesStagingControlProfiles(t *testing.T) {
	backend := startup.ControlBackendConfig{Backend: startup.BackendEtcd}
	base := config.Config{
		Profile: "production", Environment: "staging", EnvironmentExplicit: true,
		AppCatalogReadOnly: true, RequireExplicitAuth: true, APIToken: strings.Repeat("k", 40),
	}
	if !catalogTokenIssuerMode(&base, backend) {
		t.Fatal("normal production-profile Fleet control refused")
	}
	authority := base
	authority.Profile = "development"
	authority.FleetAuthorityOnly = true
	if !catalogTokenIssuerMode(&authority, backend) {
		t.Fatal("staging management authority refused")
	}
	for name, change := range map[string]func(*config.Config){
		"normal development profile": func(c *config.Config) { c.Profile = "development" },
		"production environment":     func(c *config.Config) { c.Environment = "production" },
		"implicit environment":       func(c *config.Config) { c.EnvironmentExplicit = false },
		"mutable catalog":            func(c *config.Config) { c.AppCatalogReadOnly = false },
		"implicit auth":              func(c *config.Config) { c.RequireExplicitAuth = false },
		"short signing key":          func(c *config.Config) { c.APIToken = "short" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			change(&candidate)
			if catalogTokenIssuerMode(&candidate, backend) {
				t.Fatal("unsafe issuer configuration accepted")
			}
		})
	}
	sourceValidation := backend
	sourceValidation.SourceValidation = true
	if catalogTokenIssuerMode(&base, sourceValidation) || catalogTokenIssuerMode(&base, startup.ControlBackendConfig{}) {
		t.Fatal("non-normal etcd backend accepted")
	}
}

func TestIssueCatalogTokenRegistryFailureLeavesNoFile(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "owner")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "catalog-operator.token")
	identities := &catalogTokenStore{failRecord: true}
	if err := issueCatalogToken(context.Background(), strings.Repeat("k", 40), identities, output); err == nil {
		t.Fatal("failed registry accepted")
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan output err=%v", err)
	}
}
