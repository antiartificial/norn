package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/store"
)

func TestPlatformReleaseGenesisImportEtcdIdempotence(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/platform-genesis/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("platform-genesis-test-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	request := PlatformReleaseGenesisImport{Name: "genesis-test", IntentDigest: strings.Repeat("1", 64), IntentBytesBase64: "a", IntentSignatureBase64: "b", CompletionBytesBase64: "c", CompletionSignatureBase64: "d", CheckpointsJSON: json.RawMessage(`[]`), CheckpointCount: 1, CheckpointsSHA256: strings.Repeat("2", 64), SignerPublicKeySHA256: strings.Repeat("3", 64), OperatorSubject: "operator", OperatorDeviceID: "operator-device"}
	got, created, err := s.ImportPlatformReleaseGenesis(context.Background(), request)
	if err != nil || !created {
		t.Fatalf("create: %+v %v", got, err)
	}
	got, created, err = s.ImportPlatformReleaseGenesis(context.Background(), request)
	if err != nil || created {
		t.Fatalf("replay: %+v %v", got, err)
	}
	changed := request
	changed.CompletionBytesBase64 = "changed"
	if _, _, err = s.ImportPlatformReleaseGenesis(context.Background(), changed); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("accepted changed receipt: %v", err)
	}
	intent := validPlatformReleaseIntent()
	intent.Name = request.Name
	if _, _, err = s.EnsurePlatformReleaseIntent(context.Background(), intent); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("normal intent shadowed genesis: %v", err)
	}
}
