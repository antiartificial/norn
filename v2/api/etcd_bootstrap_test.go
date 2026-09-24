package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if _, err := runEtcdManagedCredentialBootstrap([]string{etcdManagedCredentialBootstrapArgument}, &config.Config{APIToken: secret, RequireExplicitAuth: true}, backend); err != nil {
		t.Fatalf("exact retry unexpectedly failed: %v", err)
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

func TestEtcdManagedCredentialBootstrapRecoversAfterPublicationCrash(t *testing.T) {
	client := newEtcdBootstrapTestClient(t)
	prefix := "/norn-test/bootstrap-recovery/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	req := etcdBootstrapRequest{Output: filepath.Join(t.TempDir(), "initial-token"), Subject: "fleet-bootstrap", Scopes: []string{handler.ScopeAPIRead, handler.ScopeAPIWrite}, TTL: time.Hour}
	secret := "etcd-bootstrap-recovery-managed-token-secret-000"
	publishErr := errors.New("simulated crash before publication")
	var first string
	if err := bootstrapEtcdManagedCredential(context.Background(), client, prefix, secret, req, func(output, token string) error {
		first = token
		if err := os.WriteFile(output, []byte(token+"\n"), 0o600); err != nil {
			return err
		}
		return publishErr
	}); !errors.Is(err, publishErr) {
		t.Fatalf("first attempt error=%v, want publication crash", err)
	}
	entries, err := client.Get(context.Background(), prefix, clientv3.WithPrefix())
	if err != nil || len(entries.Kvs) != 2 {
		t.Fatalf("accepted state after publication crash entries=%d err=%v, want marker and token", len(entries.Kvs), err)
	}
	if err := bootstrapEtcdManagedCredential(context.Background(), client, prefix, secret, req, publishBootstrapTokenFile); err != nil {
		t.Fatalf("retry after publication crash: %v", err)
	}
	publishedBytes, err := os.ReadFile(req.Output)
	if err != nil {
		t.Fatalf("read resumed publication: %v", err)
	}
	published := strings.TrimSpace(string(publishedBytes))
	if published != first {
		t.Fatal("retry did not recreate the exact accepted bearer")
	}
	if principal, ok := handler.VerifyAccessTokenWithIdentityStore(secret, published, time.Time{}, etcdstore.NewAuthStore(client, prefix)); !ok || !principal.Allows(handler.ScopeAPIWrite) {
		t.Fatalf("recreated token did not verify: %#v ok=%v", principal, ok)
	}
}

func TestEtcdManagedCredentialBootstrapConcurrentAcceptanceCreatesOneCredential(t *testing.T) {
	client := newEtcdBootstrapTestClient(t)
	prefix := "/norn-test/bootstrap-concurrency/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	req := etcdBootstrapRequest{Output: filepath.Join(t.TempDir(), "initial-token"), Subject: "fleet-bootstrap", Scopes: []string{handler.ScopeAPIRead, handler.ScopeAPIWrite}, TTL: time.Hour}
	secret := "etcd-bootstrap-concurrency-managed-token-secret-000"
	const attempts = 8
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- bootstrapEtcdManagedCredential(context.Background(), client, prefix, secret, req, func(string, string) error { return nil })
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent bootstrap: %v", err)
		}
	}
	entries, err := client.Get(context.Background(), prefix, clientv3.WithPrefix())
	if err != nil || len(entries.Kvs) != 2 {
		t.Fatalf("concurrent bootstrap entries=%d err=%v, want one marker and one token", len(entries.Kvs), err)
	}
}

func TestEtcdManagedCredentialBootstrapRefusesMarkerTokenMismatch(t *testing.T) {
	client := newEtcdBootstrapTestClient(t)
	prefix := "/norn-test/bootstrap-mismatch/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	req := etcdBootstrapRequest{Output: filepath.Join(t.TempDir(), "initial-token"), Subject: "fleet-bootstrap", Scopes: []string{handler.ScopeAPIRead}, TTL: time.Hour}
	secret := "etcd-bootstrap-marker-mismatch-secret-000"
	record, err := acceptInitialEtcdManagedCredential(context.Background(), client, prefix, secret, req)
	if err != nil {
		t.Fatal(err)
	}
	tampered := record.Token
	tampered.Subject = "different-subject"
	encoded, err := json.Marshal(&tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(context.Background(), etcdBootstrapTokenKey(prefix, record.Token.JTI), string(encoded)); err != nil {
		t.Fatal(err)
	}
	err = bootstrapEtcdManagedCredential(context.Background(), client, prefix, secret, req, func(string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "marker and token registry disagree") {
		t.Fatalf("mismatched marker/token error=%v", err)
	}
}

func TestRequireInitialEtcdBootstrapAcceptsOnlyConsistentRecord(t *testing.T) {
	client := newEtcdBootstrapTestClient(t)
	prefix := "/norn-test/bootstrap-required/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	secret := "etcd-bootstrap-required-secret-000"
	if err := requireInitialEtcdBootstrap(context.Background(), client, prefix, secret); err == nil {
		t.Fatal("normal etcd runtime bootstrap check accepted an empty prefix")
	}
	req := etcdBootstrapRequest{Output: filepath.Join(t.TempDir(), "initial-token"), Subject: "fleet-bootstrap", Scopes: []string{handler.ScopeAPIRead}, TTL: time.Hour}
	if _, err := acceptInitialEtcdManagedCredential(context.Background(), client, prefix, secret, req); err != nil {
		t.Fatal(err)
	}
	if err := requireInitialEtcdBootstrap(context.Background(), client, prefix, secret); err != nil {
		t.Fatalf("normal etcd runtime bootstrap check rejected accepted state: %v", err)
	}
}

func TestEtcdManagedCredentialBootstrapBindsSigningKey(t *testing.T) {
	client := newEtcdBootstrapTestClient(t)
	prefix := "/norn-test/bootstrap-signing-key/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	req := etcdBootstrapRequest{Output: filepath.Join(t.TempDir(), "initial-token"), Subject: "fleet-bootstrap", Scopes: []string{handler.ScopeAPIRead}, TTL: time.Hour}
	secretA := "etcd-bootstrap-signing-key-a-secret-000"
	secretB := "etcd-bootstrap-signing-key-b-secret-000"
	if err := bootstrapEtcdManagedCredential(context.Background(), client, prefix, secretA, req, func(string, string) error { return errors.New("simulated publication crash") }); err == nil {
		t.Fatal("initial acceptance unexpectedly succeeded through simulated crash")
	}
	if err := bootstrapEtcdManagedCredential(context.Background(), client, prefix, secretB, req, func(string, string) error { return nil }); err == nil || !strings.Contains(err.Error(), "accepted bootstrap parameters differ") {
		t.Fatalf("changed signing key retry error=%v", err)
	}
	if err := requireInitialEtcdBootstrap(context.Background(), client, prefix, secretB); err == nil {
		t.Fatal("Fleet runtime accepted bootstrap signed by a different key")
	}
	if err := requireInitialEtcdBootstrap(context.Background(), client, prefix, secretA); err != nil {
		t.Fatalf("Fleet runtime rejected bootstrap signed by its bound key: %v", err)
	}
}

func TestRequireInitialEtcdBootstrapRejectsExpiredOrRevokedInitialCredential(t *testing.T) {
	client := newEtcdBootstrapTestClient(t)
	prefix := "/norn-test/bootstrap-expired/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	secret := "etcd-bootstrap-expired-secret-000"
	req := etcdBootstrapRequest{Output: filepath.Join(t.TempDir(), "initial-token"), Subject: "fleet-bootstrap", Scopes: []string{handler.ScopeAPIRead}, TTL: time.Hour}
	record, err := acceptInitialEtcdManagedCredential(context.Background(), client, prefix, secret, req)
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Hour).UTC()
	record.Token.ExpiresAt = expired
	record.Token.RevokedAt = &expired
	marker, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	token, err := json.Marshal(&record.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Txn(context.Background()).Then(clientv3.OpPut(etcdBootstrapMarkerKey(prefix), string(marker)), clientv3.OpPut(etcdBootstrapTokenKey(prefix, record.Token.JTI), string(token))).Commit(); err != nil {
		t.Fatal(err)
	}
	if err := requireInitialEtcdBootstrap(context.Background(), client, prefix, secret); err == nil {
		t.Fatal("Fleet runtime accepted an expired and revoked initial credential")
	}
}

func TestEtcdManagedCredentialBootstrapRangeCompareRejectsWriterBeforeCommit(t *testing.T) {
	client := newEtcdBootstrapTestClient(t)
	prefix := "/norn-test/bootstrap-range-compare/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	marker := etcdBootstrapMarkerKey(prefix)
	txn := client.Txn(context.Background()).If(clientv3.Compare(clientv3.Version(prefix+"/"), "=", 0).WithPrefix()).Then(clientv3.OpPut(marker, "must-not-commit"))
	if _, err := client.Put(context.Background(), prefix+"/writer-between-plan-and-commit", "unexpected"); err != nil {
		t.Fatal(err)
	}
	result, err := txn.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Succeeded {
		t.Fatal("range absence compare accepted a writer inserted before commit")
	}
}

func TestParseEtcdBootstrapRequestRequiresPracticalTTL(t *testing.T) {
	values := map[string]string{
		startup.EtcdBootstrapTokenFileEnv: "/tmp/norn-bootstrap-token",
		startup.EtcdBootstrapSubjectEnv:   "fleet-bootstrap",
		startup.EtcdBootstrapScopesEnv:    handler.ScopeAPIRead,
		startup.EtcdBootstrapTTLEnv:       "1ns",
	}
	getenv := func(key string) string { return values[key] }
	if _, err := parseEtcdBootstrapRequest(getenv); err == nil {
		t.Fatal("sub-second bootstrap TTL was accepted")
	}
	values[startup.EtcdBootstrapTTLEnv] = minimumBootstrapTTL.String()
	if _, err := parseEtcdBootstrapRequest(getenv); err != nil {
		t.Fatalf("practical bootstrap TTL rejected: %v", err)
	}
}

func TestPublishBootstrapTokenFileNeverClobbers(t *testing.T) {
	output := filepath.Join(t.TempDir(), "initial-token")
	if err := publishBootstrapTokenFile(output, "first-token"); err != nil {
		t.Fatal(err)
	}
	if err := publishBootstrapTokenFile(output, "second-token"); err == nil {
		t.Fatal("second publication unexpectedly replaced token file")
	}
	contents, err := os.ReadFile(output)
	if err != nil || string(contents) != "first-token\n" {
		t.Fatalf("token file contents=%q err=%v", contents, err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode=%v err=%v", info.Mode(), err)
	}
}

func TestPublishBootstrapTokenFileResumesExactExistingPublication(t *testing.T) {
	output := filepath.Join(t.TempDir(), "initial-token")
	if err := os.WriteFile(output, []byte("accepted-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishBootstrapTokenFile(output, "accepted-token"); err != nil {
		t.Fatalf("resume exact publication: %v", err)
	}
	contents, err := os.ReadFile(output)
	if err != nil || string(contents) != "accepted-token\n" {
		t.Fatalf("resumed contents=%q err=%v", contents, err)
	}
}

func newEtcdBootstrapTestClient(t *testing.T) *clientv3.Client {
	t.Helper()
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
