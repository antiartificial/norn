package observerclient

import (
	"bytes"
	"context"
	"errors"
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

func TestEffectJournalPublishFailureLeavesNoFinalAndCanRetry(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "evidence")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	key := "55555555-5555-4555-8555-555555555555"
	raw := []byte(`{"source":"complete"}`)
	failing := FileEffectJournal{Directory: directory, beforePublish: func() error { return errors.New("simulated publish failure") }}
	if _, err := failing.WriteOnce(context.Background(), key, raw); err == nil {
		t.Fatal("publish failure was ignored")
	}
	if _, err := os.Stat(filepath.Join(directory, key+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial final visible: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary file was not cleaned: %v %+v", err, entries)
	}
	j := FileEffectJournal{Directory: directory}
	if _, err := j.WriteOnce(context.Background(), key, raw); err != nil {
		t.Fatalf("retry: %v", err)
	}
	stored, err := j.Read(context.Background(), key)
	if err != nil || !bytes.Equal(stored, raw) {
		t.Fatalf("complete retry: %v", err)
	}
}

func TestEffectJournalNoReplaceRacePreservesExistingRecord(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "evidence")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	key := "66666666-6666-4666-8666-666666666666"
	final := filepath.Join(directory, key+".json")
	prior := []byte(`{"source":"other writer"}`)
	j := FileEffectJournal{Directory: directory, beforePublish: func() error { return os.WriteFile(final, prior, 0600) }}
	if _, err := j.WriteOnce(context.Background(), key, []byte(`{"source":"mine"}`)); err == nil {
		t.Fatal("overwrote competing record")
	}
	stored, err := os.ReadFile(final)
	if err != nil || !bytes.Equal(stored, prior) {
		t.Fatalf("competing record changed: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file leaked: %v %+v", err, entries)
	}
}
