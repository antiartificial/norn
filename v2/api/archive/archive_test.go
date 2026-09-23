package archive

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"norn/v2/api/saga"
)

func openTestStore(t *testing.T, capacity int64) (*LocalStore, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "archive")
	store, err := OpenLocal(root, capacity)
	if err != nil {
		t.Fatal(err)
	}
	return store, root
}

func TestLocalStoreIsImmutableVerifiedAndBounded(t *testing.T) {
	ctx := context.Background()
	store, root := openTestStore(t, 1024)
	info, err := store.PutImmutable(ctx, "evidence/saga/a/s1/000001.json", []byte("first"))
	if err != nil || info.Size != 5 || len(info.SHA256) != 64 {
		t.Fatalf("put = %+v, %v", info, err)
	}
	// Identical content is a verified duplicate; different content never
	// replaces the object.
	if again, err := store.PutImmutable(ctx, "evidence/saga/a/s1/000001.json", []byte("first")); err != nil || again != info {
		t.Fatalf("duplicate = %+v, %v", again, err)
	}
	if _, err := store.PutImmutable(ctx, "evidence/saga/a/s1/000001.json", []byte("other")); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("replacement = %v", err)
	}
	data, got, err := store.Get(ctx, info.Key, 1024)
	if err != nil || string(data) != "first" || got.SHA256 != info.SHA256 {
		t.Fatalf("get = %q %+v, %v", data, got, err)
	}
	if err := store.Verify(ctx, info); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "evidence/saga/a/s1/000001.json")
	stat, _ := os.Stat(path)
	if stat.Mode().Perm() != 0o400 {
		t.Fatalf("object mode = %v", stat.Mode())
	}
	// Corruption (a privileged rewrite) is detected, never silently served
	// as the recorded object.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("firsX"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(ctx, info); !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("corrupt verify = %v", err)
	}
	if _, err := store.PutImmutable(ctx, info.Key, []byte("first")); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("duplicate over corrupt object = %v", err)
	}
	// Bounds: read limit, capacity, keys.
	if _, _, err := store.Get(ctx, info.Key, 2); !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("bounded get = %v", err)
	}
	if _, err := store.PutImmutable(ctx, "evidence/big", make([]byte, 2048)); !errors.Is(err, ErrArchiveFull) {
		t.Fatalf("capacity = %v", err)
	}
	for _, key := range []string{"../escape", "/abs", "Upper/x", "a//b", "a/../b", ""} {
		if _, err := store.PutImmutable(ctx, key, []byte("x")); err == nil {
			t.Fatalf("key %q accepted", key)
		}
	}
	if _, _, err := store.Get(ctx, "evidence/missing", 10); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("missing = %v", err)
	}
	// Symlinks and FIFOs are never followed or read.
	if err := os.Symlink(path, filepath.Join(root, "evidence/link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(ctx, "evidence/link", 1024); err == nil {
		t.Fatal("symlink followed")
	}
	if err := syscall.Mkfifo(filepath.Join(root, "evidence/fifo"), 0o600); err == nil {
		done := make(chan error, 1)
		go func() { _, _, err := store.Get(ctx, "evidence/fifo", 1024); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("fifo read as an object")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("fifo read blocked")
		}
	}
	keys, err := store.List(ctx, "evidence/saga/")
	if err != nil || strings.Join(keys, ",") != "evidence/saga/a/s1/000001.json" {
		t.Fatalf("list = %v, %v", keys, err)
	}
	// The root must be owner-only.
	loose := t.TempDir()
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLocal(loose, 10); err == nil {
		t.Fatal("group-readable archive root accepted")
	}
}

// Two stores opened on one root (as two processes would) share the capacity
// bound: the flock serializes check-and-publish across open descriptions,
// so concurrent writers can never exceed the bound together.
func TestLocalStoreCapacityHoldsAcrossInstances(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "archive")
	first, err := OpenLocal(root, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenLocal(root, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	results := make(chan error, 40)
	for index := range 40 {
		store := first
		if index%2 == 1 {
			store = second
		}
		go func() {
			_, err := store.PutImmutable(ctx, "evidence/c/"+string(rune('a'+index%26))+string(rune('a'+index/26)), make([]byte, 30))
			results <- err
		}()
	}
	stored := 0
	for range 40 {
		switch err := <-results; {
		case err == nil:
			stored++
		case !errors.Is(err, ErrArchiveFull):
			t.Fatal(err)
		}
	}
	used, err := first.usage()
	if err != nil || stored != 3 || used != 90 {
		t.Fatalf("stored %d objects, %d bytes (%v); capacity 100 allows exactly 3", stored, used, err)
	}
	// Newly created ancestor directories exist as real directories.
	if info, err := os.Lstat(filepath.Join(root, "evidence", "c")); err != nil || !info.IsDir() {
		t.Fatalf("ancestor = %v, %v", info, err)
	}
}

func TestBundleSealOpenDetectsTampering(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	bundle := &Bundle{Subject: Subject{Kind: "saga", ID: "s-1", App: "shop", OperationID: "op-1", OperationKind: "app.deploy"}, Sequence: 1,
		Events: []saga.Event{
			{ID: "e1", SagaID: "s-1", Timestamp: now, App: "shop", Action: "step.start", Message: "clone", Metadata: map[string]string{"step": "clone"}},
			{ID: "e2", SagaID: "s-1", Timestamp: now.Add(time.Second), App: "shop", Action: "step.complete", Message: "clone"},
		},
		Acceptance: &SignedAcceptance{IntentID: "i-1", CanonicalBytes: []byte(`{"signed":"exact bytes"}`), Signature: "sig"},
		SealedAt:   now}
	encoded, err := Seal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(encoded)
	if err != nil || opened.Cutoff.EventCount != 2 || string(opened.Acceptance.CanonicalBytes) != `{"signed":"exact bytes"}` || !opened.Cutoff.LastTimestamp.Equal(now.Add(time.Second)) {
		t.Fatalf("open = %+v, %v", opened, err)
	}
	for name, mutate := range map[string]func(string) string{
		"event message":  func(s string) string { return strings.Replace(s, `"clone"`, `"CLONE"`, 1) },
		"cutoff":         func(s string) string { return strings.Replace(s, `"eventCount":2`, `"eventCount":1`, 1) },
		"unknown field":  func(s string) string { return strings.Replace(s, `{"schema"`, `{"extra":1,"schema"`, 1) },
		"trailing bytes": func(s string) string { return s + "{}" },
	} {
		if _, err := Open([]byte(mutate(string(encoded)))); err == nil {
			t.Errorf("%s tampering accepted", name)
		}
	}
	signed := *opened
	acceptance := *opened.Acceptance
	acceptance.CanonicalBytes = []byte(`{"signed":"other"}`)
	signed.Acceptance = &acceptance
	reencoded, _ := json.Marshal(&signed)
	if _, err := Open(reencoded); err == nil {
		t.Fatal("changed signed bytes accepted")
	}
	foreign := *bundle
	foreign.Events = append([]saga.Event(nil), bundle.Events...)
	foreign.Events[0].SagaID = "other"
	if _, err := Seal(&foreign); err == nil {
		t.Fatal("event from another saga sealed")
	}
}
