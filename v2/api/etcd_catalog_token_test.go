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
