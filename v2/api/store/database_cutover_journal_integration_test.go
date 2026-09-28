package store

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

func TestDatabaseCutoverJournalPersistsCASAndRejectsResourceCollision(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	ctx := context.Background()
	db := &DB{Pool: pool}
	migrator, err := NewControlSchemaMigrator(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO operations(id,kind,app) VALUES ($1,'database.cutover','fixture')`, id); err != nil {
		t.Fatal(err)
	}
	intent := cutover.Intent{SchemaVersion: "norn.database-cutover/v1", OperationID: id, App: "fixture", LogicalDatabase: "appdb", CandidateRelease: "release-1", AuthorityGeneration: 3, WriterInventorySHA256: strings.Repeat("a", 64),
		Source: database.TargetIdentity{ServiceID: "mini", ServiceGeneration: 1, BindingID: "old", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "appdb", Role: "runtime"},
		Target: database.TargetIdentity{ServiceID: "fleet", ServiceGeneration: 1, BindingID: "new", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "appdb", Role: "runtime"}}
	j, err := db.prepareDatabaseCutoverJournal(ctx, intent)
	if err != nil || j.Revision != 1 {
		t.Fatalf("prepare %+v: %v", j, err)
	}
	if _, err := db.prepareDatabaseCutoverJournal(ctx, intent); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	changed := intent
	changed.Target.BindingGeneration++
	if _, err := db.prepareDatabaseCutoverJournal(ctx, changed); !errors.Is(err, errDatabaseCutoverJournalConflict) {
		t.Fatal("retargeted replay accepted")
	}
	secondID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO operations(id,kind,app) VALUES ($1,'database.cutover','fixture')`, secondID); err != nil {
		t.Fatal(err)
	}
	changed = intent
	changed.OperationID = secondID
	if _, err := db.prepareDatabaseCutoverJournal(ctx, changed); !errors.Is(err, errDatabaseCutoverJournalConflict) {
		t.Fatal("second active cutover of same database accepted")
	}

	if _, err := db.advanceDatabaseCutoverJournal(ctx, id, 1, cutover.PhaseActivate, strings.Repeat("b", 64)); !errors.Is(err, errDatabaseCutoverJournalConflict) {
		t.Fatal("skipped activation accepted")
	}
	const contenders = 2
	var wg sync.WaitGroup
	results := make(chan error, contenders)
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := db.advanceDatabaseCutoverJournal(ctx, id, 1, cutover.PhaseQuiesce, strings.Repeat("b", 64))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, errDatabaseCutoverJournalConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS results: successes=%d conflicts=%d", successes, conflicts)
	}
	readback, _, err := db.loadDatabaseCutoverJournal(ctx, id)
	if err != nil || readback.Phase != cutover.PhaseQuiesce || readback.Revision != 2 || readback.Receipts[cutover.PhaseQuiesce] != strings.Repeat("b", 64) {
		t.Fatalf("readback %+v: %v", readback, err)
	}
}
