package etcdstore

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

func TestRootAccessTokenPreservesActorAcrossRotationEtcd(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx := context.Background()
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-tests/token-lineage/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	auth := NewAuthStore(client, prefix)
	for _, token := range []store.AccessToken{
		{JTI: "root"}, {JTI: "rotated-1", RotatedFrom: "root"}, {JTI: "rotated-2", RotatedFrom: "rotated-1"},
		{JTI: "orphan", RotatedFrom: "missing"}, {JTI: "cycle-a", RotatedFrom: "cycle-b"}, {JTI: "cycle-b", RotatedFrom: "cycle-a"},
	} {
		if err := auth.putJSON(ctx, auth.tokenKey(token.JTI), token); err != nil {
			t.Fatal(err)
		}
	}
	if root, err := auth.RootAccessToken(ctx, "rotated-2"); err != nil || root != "root" {
		t.Fatalf("rotated actor = %q err=%v", root, err)
	}
	for _, id := range []string{"orphan", "cycle-a", "missing", ""} {
		if root, err := auth.RootAccessToken(ctx, id); err == nil || root != "" {
			t.Fatalf("ambiguous actor %q resolved to %q err=%v", id, root, err)
		}
	}
}
