package cutover

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"norn/v2/api/archive"
)

func TestPhaseEvidenceArchiveRetainsExactEdgeAndRefusesReplacement(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := archive.OpenLocal(root, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	j, err := New(testIntent())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	data := []byte(`{"schemaVersion":"fixture/v1","observed":"source-fenced"}`)
	ref, err := RetainPhaseEvidence(ctx, store, j, PhaseQuiesce, data)
	if err != nil {
		t.Fatal(err)
	}
	readback, err := ReadPhaseEvidence(ctx, store, j, ref)
	if err != nil || !bytes.Equal(readback, data) {
		t.Fatalf("readback differs: %q, %v", readback, err)
	}
	if same, err := RetainPhaseEvidence(ctx, store, j, PhaseQuiesce, data); err != nil || same != ref {
		t.Fatalf("exact replay differs: %+v, %v", same, err)
	}
	if _, err := RetainPhaseEvidence(ctx, store, j, PhaseQuiesce, []byte(`{"observed":"different"}`)); !errors.Is(err, archive.ErrImmutableConflict) {
		t.Fatalf("replacement not refused: %v", err)
	}
	if _, err := RetainPhaseEvidence(ctx, store, j, PhaseFinalSync, data); !errors.Is(err, ErrTransition) {
		t.Fatalf("skipped phase wrote evidence: %v", err)
	}
	key, err := PhaseEvidenceKey(j, PhaseFinalSync)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(ctx, key, maxPhaseEvidenceBytes); !errors.Is(err, archive.ErrObjectNotFound) {
		t.Fatalf("skipped phase left an archive object: %v", err)
	}
	if _, err := RetainPhaseEvidence(ctx, store, j, PhaseQuiesce, make([]byte, maxPhaseEvidenceBytes+1)); !errors.Is(err, ErrTransition) {
		t.Fatalf("oversized evidence accepted: %v", err)
	}
}
