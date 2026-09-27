package archive

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"norn/v2/api/internal/s3emulator"
)

func emulatedStore(t *testing.T, mutate func(*ObjectStoreConfig)) (*ObjectStore, *s3emulator.Emulator, error) {
	t.Helper()
	emulator, server := s3emulator.Start("norn-evidence", "archive-writer")
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	config := ObjectStoreConfig{Endpoint: endpoint.Host, Bucket: "norn-evidence", Prefix: "prod", Region: "us-east-1",
		AccessKey: "archive-writer", SecretKey: "archive-secret", Transport: server.Client().Transport}
	if mutate != nil {
		mutate(&config)
	}
	store, err := OpenObjectStore(context.Background(), config)
	return store, emulator, err
}

// Emulator tests of the Fleet adapter's logic. They do not qualify any
// real object service.
func TestObjectStoreIsImmutableVerifiedAndBoundedAgainstEmulator(t *testing.T) {
	ctx := context.Background()
	store, emulator, err := emulatedStore(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := "evidence/saga/shop/s1/000001.json"
	info, err := store.PutImmutable(ctx, key, []byte("first"))
	if err != nil || info.Size != 5 {
		t.Fatalf("put = %+v, %v", info, err)
	}
	if !contains(emulator.Keys(), "prod/"+key) {
		t.Fatalf("object not under the configured prefix: %v", emulator.Keys())
	}
	// A verified duplicate succeeds; different bytes never replace it.
	if again, err := store.PutImmutable(ctx, key, []byte("first")); err != nil || again != info {
		t.Fatalf("duplicate = %+v, %v", again, err)
	}
	for _, other := range []string{"other", "first-and-longer"} {
		if _, err := store.PutImmutable(ctx, key, []byte(other)); !errors.Is(err, ErrImmutableConflict) {
			t.Fatalf("replacement with %q = %v", other, err)
		}
	}
	data, got, err := store.Get(ctx, key, 1024)
	if err != nil || string(data) != "first" || got != info {
		t.Fatalf("get = %q %+v, %v", data, got, err)
	}
	if err := store.Verify(ctx, info); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(ctx, key, 2); !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("bounded get = %v", err)
	}
	if _, _, err := store.Get(ctx, "evidence/missing", 10); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("missing = %v", err)
	}
	for _, bad := range []string{"../escape", "Upper/x", "a//b", ""} {
		if _, err := store.PutImmutable(ctx, bad, []byte("x")); err == nil {
			t.Fatalf("key %q accepted", bad)
		}
	}
	keys, err := store.List(ctx, "evidence/")
	if err != nil || strings.Join(keys, ",") != key {
		t.Fatalf("list (probe excluded, prefix stripped) = %v, %v", keys, err)
	}
	// Corruption: served bytes that differ from the recorded digest are
	// refused by Get and Verify, and a privileged rewrite is detected.
	emulator.Configure(func(e *s3emulator.Emulator) { e.CorruptReads = true })
	if _, _, err := store.Get(ctx, key, 1024); !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("corrupted read = %v", err)
	}
	emulator.Configure(func(e *s3emulator.Emulator) { e.CorruptReads = false })
	emulator.Tamper("prod/"+key, []byte("firsX"))
	if err := store.Verify(ctx, info); !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("tampered verify = %v", err)
	}
	if _, err := store.PutImmutable(ctx, key, []byte("first")); !errors.Is(err, ErrImmutableConflict) && !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("duplicate over tampered object = %v", err)
	}
	// Outage and quota are distinct errors, never conflicts or success.
	emulator.Configure(func(e *s3emulator.Emulator) { e.FailWrites = true })
	if _, err := store.PutImmutable(ctx, "evidence/outage", []byte("x")); err == nil || errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("outage = %v", err)
	}
	emulator.Configure(func(e *s3emulator.Emulator) { e.FailWrites, e.QuotaBytes = false, 8 })
	if _, err := store.PutImmutable(ctx, "evidence/quota", []byte("0123456789")); !errors.Is(err, ErrArchiveFull) {
		t.Fatalf("quota = %v", err)
	}
}

func TestObjectStoreRefusesUnsafeOrMisconfiguredStores(t *testing.T) {
	ctx := context.Background()
	// A store that overwrites despite If-None-Match cannot hold immutable
	// evidence: opening it fails.
	emulator, server := s3emulator.Start("norn-evidence", "archive-writer")
	defer server.Close()
	emulator.Configure(func(e *s3emulator.Emulator) { e.IgnoreConditional = true })
	endpoint, _ := url.Parse(server.URL)
	config := ObjectStoreConfig{Endpoint: endpoint.Host, Bucket: "norn-evidence", Region: "us-east-1", AccessKey: "archive-writer", SecretKey: "s", Transport: server.Client().Transport}
	if _, err := OpenObjectStore(ctx, config); err == nil || !strings.Contains(err.Error(), "immutable publication cannot be guaranteed") {
		t.Fatalf("non-conditional store = %v", err)
	}
	emulator.Configure(func(e *s3emulator.Emulator) { e.IgnoreConditional = false })
	for name, mutate := range map[string]func(*ObjectStoreConfig){
		"missing bucket":    func(c *ObjectStoreConfig) { c.Bucket = "absent" },
		"wrong credentials": func(c *ObjectStoreConfig) { c.AccessKey = "application-key" },
		"no region":         func(c *ObjectStoreConfig) { c.Region = "" },
		"unclean prefix":    func(c *ObjectStoreConfig) { c.Prefix = "../x" },
	} {
		candidate := config
		mutate(&candidate)
		if _, err := OpenObjectStore(ctx, candidate); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := OpenObjectStore(ctx, config); err != nil {
		t.Fatalf("valid configuration = %v", err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
