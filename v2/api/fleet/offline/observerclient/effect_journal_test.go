package observerclient

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEffectJournalOwnerOnlyCreateOnceAndExactReplay(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "evidence")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	j := FileEffectJournal{Directory: directory}
	key := "33333333-3333-4333-8333-333333333333"
	raw := []byte(`{"source":"independent"}`)
	first, err := j.WriteOnce(context.Background(), key, raw)
	if err != nil || first != digestEffectBytes(raw) {
		t.Fatalf("first write: %s %v", first, err)
	}
	info, err := os.Stat(filepath.Join(directory, key+".json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("record mode: %v %v", info, err)
	}
	second, err := j.WriteOnce(context.Background(), key, bytes.Clone(raw))
	if err != nil || second != first {
		t.Fatalf("exact replay: %s %v", second, err)
	}
	if _, err := j.WriteOnce(context.Background(), key, []byte(`{"source":"changed"}`)); err == nil {
		t.Fatal("accepted changed replay")
	}
	if _, err := j.WriteOnce(context.Background(), "../escape", raw); err == nil {
		t.Fatal("accepted path escape")
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := j.WriteOnce(context.Background(), "44444444-4444-4444-8444-444444444444", raw); err == nil {
		t.Fatal("accepted public journal directory")
	}
}
