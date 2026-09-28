package etcdstore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"norn/v2/api/cutover"
	"norn/v2/api/database"
)

func etcdTestCutoverIntent() cutover.Intent {
	return cutover.Intent{SchemaVersion: "norn.database-cutover/v1", OperationID: uuid.NewString(), App: "fixture", LogicalDatabase: "appdb", CandidateRelease: "release-1", AuthorityGeneration: 3, WriterInventorySHA256: strings.Repeat("a", 64),
		Source: database.TargetIdentity{ServiceID: "mini", ServiceGeneration: 1, BindingID: "old", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "appdb", Role: "runtime"},
		Target: database.TargetIdentity{ServiceID: "fleet", ServiceGeneration: 1, BindingID: "new", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "appdb", Role: "runtime"}}
}

func TestEtcdDatabaseCutoverJournalCASAndResourceExclusion(t *testing.T) {
	adapter, client, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	intent := etcdTestCutoverIntent()
	j, err := adapter.prepareDatabaseCutoverJournal(ctx, intent)
	if err != nil || j.Revision != 1 {
		t.Fatalf("prepare %+v: %v", j, err)
	}
	if _, err := adapter.prepareDatabaseCutoverJournal(ctx, intent); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	changed := intent
	changed.Target.BindingGeneration++
	if _, err := adapter.prepareDatabaseCutoverJournal(ctx, changed); !errors.Is(err, errEtcdCutoverJournalConflict) {
		t.Fatal("retargeted replay accepted")
	}
	changed = intent
	changed.OperationID = uuid.NewString()
	if _, err := adapter.prepareDatabaseCutoverJournal(ctx, changed); !errors.Is(err, errEtcdCutoverJournalConflict) {
		t.Fatal("second active cutover accepted")
	}
	if _, err := adapter.advanceDatabaseCutoverJournal(ctx, intent.OperationID, 1, cutover.PhaseActivate, strings.Repeat("b", 64)); !errors.Is(err, errEtcdCutoverJournalConflict) {
		t.Fatal("skipped activation accepted")
	}

	const contenders = 2
	var wg sync.WaitGroup
	results := make(chan error, contenders)
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := adapter.advanceDatabaseCutoverJournal(ctx, intent.OperationID, 1, cutover.PhaseQuiesce, strings.Repeat("b", 64))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, errEtcdCutoverJournalConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS successes=%d conflicts=%d", successes, conflicts)
	}
	readback, _, err := adapter.loadDatabaseCutoverJournal(ctx, intent.OperationID)
	if err != nil || readback.Phase != cutover.PhaseQuiesce || readback.Revision != 2 || readback.Receipts[cutover.PhaseQuiesce] != strings.Repeat("b", 64) {
		t.Fatalf("readback %+v: %v", readback, err)
	}
	if _, err := client.Delete(ctx, adapter.cutoverActiveResourceKey(intent.App, intent.LogicalDatabase)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.loadDatabaseCutoverJournal(ctx, intent.OperationID); !errors.Is(err, errEtcdCutoverJournalConflict) {
		t.Fatal("unowned journal accepted")
	}
}
