package retention

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/archive"
)

// A bundle carrying a genuinely signed acceptance of a different operation
// (valid digests and signature) is refused on read-back and index recovery:
// signed content must name this bundle's operation, saga and payload.
func TestArchivedAcceptanceIsBoundToItsOperationNotJustSigned(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	first := f.finishedSaga("bound", 1)
	second := f.finishedSaga("bound", 1)
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 2 {
		t.Fatalf("publish = %+v, %v", report, err)
	}
	firstIntent, secondIntent := f.intents(first.SagaID)[0], f.intents(second.SagaID)[0]
	firstBundle, err := LoadBundle(ctx, f.objects, firstIntent)
	if err != nil {
		t.Fatal(err)
	}
	secondBundle, err := LoadBundle(ctx, f.objects, secondIntent)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.archiver.verifyAcceptance(ctx, firstBundle); err != nil {
		t.Fatalf("genuine acceptance refused: %v", err)
	}

	forge := func(mutate func(*archive.Bundle)) (*LocalBundle, error) {
		copy := *firstBundle
		mutate(&copy)
		data, err := archive.Seal(&copy)
		if err != nil {
			t.Fatal(err)
		}
		objects, err := archive.OpenLocal(filepath.Join(t.TempDir(), "forged"), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { objects.Close() })
		info, err := objects.PutImmutable(ctx, firstIntent.ObjectKey, data)
		if err != nil {
			t.Fatal(err)
		}
		_, err = (&Archiver{Archive: objects, Signer: f.signer}).readBack(ctx, info, firstIntent)
		return &LocalBundle{Objects: objects}, err
	}
	// Another operation's signed acceptance, spliced in unchanged.
	if _, err := forge(func(b *archive.Bundle) { b.Acceptance = secondBundle.Acceptance }); err == nil || !strings.Contains(err.Error(), "does not name this operation") {
		t.Fatalf("swapped acceptance = %v", err)
	}
	// The same signed acceptance over a rewritten operation payload.
	if _, err := forge(func(b *archive.Bundle) {
		var row map[string]any
		_ = json.Unmarshal(b.Operation, &row)
		row["payload"] = map[string]any{"injected": "value"}
		b.Operation, _ = json.Marshal(row)
	}); err == nil || !strings.Contains(err.Error(), "payload differs") {
		t.Fatalf("rewritten payload = %v", err)
	}
	// A signed acceptance whose canonical bytes were altered (digest breaks).
	if _, err := forge(func(b *archive.Bundle) {
		acceptance := *b.Acceptance
		acceptance.CanonicalBytes = []byte(strings.Replace(string(acceptance.CanonicalBytes), first.ID, second.ID, 1))
		b.Acceptance = &acceptance
	}); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("altered canonical bytes = %v", err)
	}
	// Index recovery applies the same binding.
	swapped, _ := forge(func(b *archive.Bundle) { b.Acceptance = secondBundle.Acceptance })
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM evidence_archive_intents`); err != nil {
		t.Fatal(err)
	}
	recovery, err := RestoreIndex(ctx, f.db, swapped.Objects, nil)
	if err != nil || recovery.Restored != 0 || len(recovery.Rejected) != 1 {
		t.Fatalf("restore of a forged bundle = %+v, %v", recovery, err)
	}
}

// LocalBundle keeps a forged store alive for follow-up checks.
type LocalBundle struct{ Objects *archive.LocalStore }
