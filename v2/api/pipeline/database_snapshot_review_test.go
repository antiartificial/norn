package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

func TestReviewSnapshotReuseCannotAdoptUnprovenDump(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	filename := "review_manual_20260922T120000.dump"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("unrelated nonempty dump without provenance"), 0600); err != nil {
		t.Fatal(err)
	}
	target := database.TargetIdentity{ServiceID: "new-server", ServiceGeneration: 1, BindingID: "new-binding", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "review", Role: "app"}
	location := snapshotLocation{dir: dir, database: "review", bound: &boundDatabase{resolved: database.ResolvedBinding{Target: target}, recorded: recordedTarget{CatalogRevision: 1}}}
	if _, err := createDataSnapshotAt(context.Background(), location, "manual", at, true); err == nil {
		t.Fatal("snapshot reuse assigned current target provenance to an unproven existing dump")
	}
	if _, err := os.Stat(filepath.Join(dir, filename+sidecarSuffix)); !os.IsNotExist(err) {
		t.Fatal("reuse published target metadata for unproven bytes")
	}
}

func TestReviewDatabaseTargetLargeGeneration(t *testing.T) {
	f := newTargetFixture(t)
	const generation uint64 = 9007199254740993
	f.catalog.Services[0].Generation = generation
	if _, err := f.db.ActivateDatabaseCatalog(context.Background(), 0, f.catalog, "review"); err != nil {
		t.Fatal(err)
	}
	op, err := f.p.bindDatabaseTarget(context.Background(), model.Operation{Kind: "app.snapshot", App: f.app})
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := recordedTargetFromPayload(op.Payload)
	if err != nil || recorded == nil {
		t.Fatalf("read bound target: %v", err)
	}
	if recorded.Target.ServiceGeneration != generation {
		t.Fatalf("generation rounded: got %d, want %d", recorded.Target.ServiceGeneration, generation)
	}
}

func TestReviewLegacySnapshotAdoptionCannotCrossGeneration(t *testing.T) {
	dir := t.TempDir()
	filename := "review_manual_20260922T120000.dump"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("legacy dump for the original target"), 0600); err != nil {
		t.Fatal(err)
	}
	target := database.TargetIdentity{ServiceID: "server", ServiceGeneration: 1, BindingID: "legacy-map", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "review", Role: "app"}
	p := &Pipeline{DatabaseTargets: &DatabaseTargets{ProfileID: "mini", SnapshotRoot: dir}}
	bound := &boundDatabase{resolved: database.ResolvedBinding{Target: target, Legacy: true}}
	if _, err := p.prepareSnapshotLocation("review", bound); err != nil {
		t.Fatal(err)
	}
	bound.resolved.Target.ServiceGeneration = 2
	location, err := p.prepareSnapshotLocation("review", bound)
	if err != nil {
		return // Refusing the incompatible namespace is safe.
	}
	snapshot, err := findDataSnapshot(location, filename)
	if err != nil {
		return // Refusing the old snapshot is also safe.
	}
	if err := verifySnapshotContent(location, *snapshot); err == nil {
		t.Fatal("legacy snapshot adopted under generation 1 accepted for generation 2 without cross-target intent")
	}
}
