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

type adminTokenStore struct {
	store.AuthStore
	record     *store.AccessToken
	revoked    string
	failRecord bool
	onRecord   func() error
}

func (s *adminTokenStore) RecordAccessToken(_ context.Context, token *store.AccessToken) error {
	if s.failRecord {
		return errors.New("registry unavailable")
	}
	s.record = token
	if s.onRecord != nil {
		return s.onRecord()
	}
	return nil
}
func (s *adminTokenStore) AccessTokenActive(_ context.Context, jti string) (bool, error) {
	return s.record != nil && s.record.JTI == jti && s.revoked == "", nil
}
func (s *adminTokenStore) RevokeAccessToken(_ context.Context, jti string) ([]string, error) {
	s.revoked = jti
	return nil, nil
}

func TestIssueAdminTokenPublishesOnlyExactScopesOwnerOnly(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "owner")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "admin-operator.token")
	identities := &adminTokenStore{}
	secret := strings.Repeat("k", 40)
	if err := issueAdminToken(context.Background(), secret, identities, output); err != nil {
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
	if !ok || len(principal.Scopes) != 1 || principal.Scopes[0] != handler.ScopeAdmin || principal.CI != nil || identities.record.Subject != adminTokenSubject || identities.record.ExpiresAt.Sub(identities.record.IssuedAt) != adminTokenTTL {
		t.Fatal("admin token did not retain exact non-CI scopes and TTL")
	}
	if err := issueAdminToken(context.Background(), secret, identities, output); err == nil {
		t.Fatal("existing token file was replaced")
	}
}

func TestIssueAdminTokenRequiresPrivateNewOutputAndEtcdMode(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "owner")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "admin-operator.token")
	if err := validateAdminTokenOutput(output); err == nil {
		t.Fatal("world-readable parent accepted")
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminTokenOutput(output); err != nil {
		t.Fatal(err)
	}
	t.Setenv(etcdAdminTokenFileEnv, output)
	if handled, err := runEtcdAdminTokenIssue([]string{etcdIssueAdminTokenArgument}, &config.Config{}, startup.ControlBackendConfig{Backend: startup.BackendEtcd}); !handled || err == nil {
		t.Fatalf("unsafe mode: handled=%v err=%v", handled, err)
	}
	if handled, err := runEtcdAdminTokenIssue(nil, nil, startup.ControlBackendConfig{}); handled || err != nil {
		t.Fatalf("ordinary runtime: handled=%v err=%v", handled, err)
	}
}

func TestAdminTokenIssuerModeMatchesStagingControlProfiles(t *testing.T) {
	backend := startup.ControlBackendConfig{Backend: startup.BackendEtcd}
	base := config.Config{
		Profile: "production", Environment: "staging", EnvironmentExplicit: true,
		AppCatalogReadOnly: true, RequireExplicitAuth: true, APIToken: strings.Repeat("k", 40),
	}
	if !adminTokenIssuerMode(&base, backend) {
		t.Fatal("normal production-profile Fleet control refused")
	}
	authority := base
	authority.Profile = "development"
	authority.FleetAuthorityOnly = true
	if !adminTokenIssuerMode(&authority, backend) {
		t.Fatal("staging management authority refused")
	}
	for name, change := range map[string]func(*config.Config){
		"normal development profile": func(c *config.Config) { c.Profile = "development" },
		"production environment":     func(c *config.Config) { c.Environment = "production" },
		"implicit environment":       func(c *config.Config) { c.EnvironmentExplicit = false },
		"mutable admin":              func(c *config.Config) { c.AppCatalogReadOnly = false },
		"implicit auth":              func(c *config.Config) { c.RequireExplicitAuth = false },
		"short signing key":          func(c *config.Config) { c.APIToken = "short" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			change(&candidate)
			if adminTokenIssuerMode(&candidate, backend) {
				t.Fatal("unsafe issuer configuration accepted")
			}
		})
	}
	sourceValidation := backend
	sourceValidation.SourceValidation = true
	if adminTokenIssuerMode(&base, sourceValidation) || adminTokenIssuerMode(&base, startup.ControlBackendConfig{}) {
		t.Fatal("non-normal etcd backend accepted")
	}
}

func TestIssueAdminTokenRegistryFailureLeavesNoFile(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "owner")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "admin-operator.token")
	identities := &adminTokenStore{failRecord: true}
	if err := issueAdminToken(context.Background(), strings.Repeat("k", 40), identities, output); err == nil {
		t.Fatal("failed registry accepted")
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan output err=%v", err)
	}
}

func TestIssueAdminTokenFinalPathAbsentUntilCompleteAndExclusive(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "owner")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "admin-operator.token")
	identities := &adminTokenStore{onRecord: func() error {
		if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("final output visible before issuance completed: %v", err)
		}
		return os.WriteFile(output, []byte("existing owner content"), 0600)
	}}
	if err := issueAdminToken(context.Background(), strings.Repeat("k", 40), identities, output); err == nil {
		t.Fatal("exclusive publication overwrote a competing file")
	}
	if identities.record == nil || identities.revoked != identities.record.JTI {
		t.Fatal("unpublished managed token was not revoked")
	}
	raw, err := os.ReadFile(output)
	if err != nil || string(raw) != "existing owner content" {
		t.Fatalf("competing output changed: content=%q err=%v", raw, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "admin-operator.token" {
		t.Fatalf("temporary bearer remained: entries=%v err=%v", entries, err)
	}
}
