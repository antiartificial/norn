package handler

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
)

func TestManagedTokenUsesEtcdIdentityAggregate(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-conf/managed-token/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	identities := etcdstore.NewAuthStore(client, prefix)
	const secret = "managed-token-etcd-integration-secret"
	token, record, err := IssueManagedAccessToken(context.Background(), secret, identities, "etcd operator", []string{ScopePlatformOperate}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	principal, ok := VerifyAccessTokenWithIdentityStore(secret, token, time.Time{}, identities)
	if !ok || principal.TokenID != record.JTI || principal.Source != AccessPrincipalSourceManagedToken || !principal.Allows(ScopePlatformOperate) {
		t.Fatalf("managed etcd token verification = %+v, %v", principal, ok)
	}
	if _, ok := VerifyAccessTokenWithIdentityStore(secret, token, time.Time{}, nil); ok {
		t.Fatal("managed token authenticated without a durable identity store")
	}
	if _, ok := VerifyAccessTokenWithIdentityStore(secret+"wrong", token, time.Time{}, identities); ok {
		t.Fatal("managed token authenticated under another signing key")
	}
	if _, err := identities.RevokeAccessToken(context.Background(), record.JTI); err != nil {
		t.Fatal(err)
	}
	if _, ok := VerifyAccessTokenWithIdentityStore(secret, token, time.Time{}, identities); ok {
		t.Fatal("revoked managed token authenticated")
	}
}
