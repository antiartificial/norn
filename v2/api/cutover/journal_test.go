package cutover

import (
	"errors"
	"strings"
	"testing"

	"norn/v2/api/database"
)

func testIntent() Intent {
	return Intent{SchemaVersion: "norn.database-cutover/v1", OperationID: "op-1", App: "fixture", LogicalDatabase: "appdb", CandidateRelease: "sha256:release", AuthorityGeneration: 7, WriterInventorySHA256: strings.Repeat("a", 64),
		Source: database.TargetIdentity{ServiceID: "mini", ServiceGeneration: 1, BindingID: "old", BindingGeneration: 2, Engine: database.EnginePostgreSQL, Database: "appdb", Role: "runtime"},
		Target: database.TargetIdentity{ServiceID: "fleet", ServiceGeneration: 1, BindingID: "new", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "appdb", Role: "runtime"}}
}

func TestJournalRejectsStaleAndOutOfOrderPromotion(t *testing.T) {
	j, err := New(testIntent())
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("b", 64)
	if _, err := j.Advance(1, PhaseActivate, digest); !errors.Is(err, ErrTransition) {
		t.Fatal("activation skipped source fence and final sync")
	}
	if _, err := j.Advance(0, PhaseQuiesce, digest); !errors.Is(err, ErrTransition) {
		t.Fatal("zero revision accepted")
	}
	for _, next := range []Phase{PhaseQuiesce, PhaseFinalSync, PhaseActivate, PhaseVerify, PhaseAccept} {
		old := j
		j, err = j.Advance(j.Revision, next, digest)
		if err != nil {
			t.Fatalf("advance %s: %v", next, err)
		}
		if _, err := old.Advance(j.Revision, next, digest); !errors.Is(err, ErrTransition) {
			t.Fatal("stale writer accepted")
		}
		if len(old.Receipts) != int(old.Revision-1) {
			t.Fatal("advance mutated prior journal")
		}
	}
	if _, err := j.Advance(j.Revision, PhaseAccept, digest); !errors.Is(err, ErrTransition) {
		t.Fatal("accepted journal advanced again")
	}
}

func TestJournalRejectsIdentityAndEvidenceDrift(t *testing.T) {
	intent := testIntent()
	intent.Target = intent.Source
	if _, err := New(intent); !errors.Is(err, ErrTransition) {
		t.Fatal("same target accepted")
	}
	intent = testIntent()
	intent.Target.Engine = database.EngineMySQL
	if _, err := New(intent); !errors.Is(err, ErrTransition) {
		t.Fatal("mixed engine accepted")
	}
	j, _ := New(testIntent())
	if _, err := j.Advance(1, PhaseQuiesce, "not-a-digest"); !errors.Is(err, ErrTransition) {
		t.Fatal("unbound evidence accepted")
	}
	j, _ = j.Advance(1, PhaseQuiesce, strings.Repeat("c", 64))
	delete(j.Receipts, PhaseQuiesce)
	if _, err := j.Advance(2, PhaseFinalSync, strings.Repeat("d", 64)); !errors.Is(err, ErrTransition) {
		t.Fatal("missing prior receipt accepted")
	}
}
