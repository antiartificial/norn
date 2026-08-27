package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotInventoryAndPruneAreDeterministic(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("snapshots", 0o750); err != nil {
		t.Fatal(err)
	}
	files := []string{
		"orders_manual_20260825T140000.dump",
		"orders_manual_20260824T140000.dump",
		"orders_pre-migrate_20260823T140000.dump",
		"another_manual_20260826T140000.dump",
		"orders_bad.dump",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join("snapshots", name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshots, err := listDataSnapshots("orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 3 || snapshots[0].Timestamp != "20260825T140000" {
		t.Fatalf("unexpected inventory: %#v", snapshots)
	}
	pruned, err := pruneDataSnapshots("orders", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 1 || pruned[0] != "orders_pre-migrate_20260823T140000.dump" {
		t.Fatalf("unexpected prune receipt: %#v", pruned)
	}
	if _, err := os.Stat(filepath.Join("snapshots", "another_manual_20260826T140000.dump")); err != nil {
		t.Fatalf("another database snapshot was touched: %v", err)
	}
}

func TestSnapshotInventoryRejectsUnsafeNamesAndIgnoresSymlinks(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := listDataSnapshots("../outside"); err == nil {
		t.Fatal("expected unsafe database name to be rejected")
	}
	if err := os.MkdirAll("snapshots", 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "sensitive")
	if err := os.WriteFile(target, []byte("not a dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join("snapshots", "orders_manual_20260825T140000.dump")); err != nil {
		t.Fatal(err)
	}
	snapshots, err := listDataSnapshots("orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("snapshot symlink must not be inventoried: %#v", snapshots)
	}
}

func TestFindDataSnapshotRejectsAmbiguousTimestamp(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("snapshots", 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"orders_manual_20260825T140000.dump",
		"orders_pre-migrate_20260825T140000.dump",
	} {
		if err := os.WriteFile(filepath.Join("snapshots", name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := findDataSnapshot("orders", "20260825T140000"); err == nil {
		t.Fatal("expected duplicate timestamps to be rejected as ambiguous")
	}
	target, err := findDataSnapshot("orders", "orders_pre-migrate_20260825T140000.dump")
	if err != nil || target.Filename != "orders_pre-migrate_20260825T140000.dump" {
		t.Fatalf("expected exact inventory filename to disambiguate restore: target=%#v err=%v", target, err)
	}
}
