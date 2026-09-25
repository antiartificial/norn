package pipeline

import (
	"context"
	"errors"
	"os"
	"testing"
)

// Match storage.Client.GetObject's exclusive destination publication.
type reviewSnapshotObjects map[string][]byte

func (s reviewSnapshotObjects) PutObject(_ context.Context, _, key, path string) error {
	data, err := os.ReadFile(path)
	if err == nil {
		s[key] = data
	}
	return err
}

func (s reviewSnapshotObjects) GetObject(_ context.Context, _, key, path string) error {
	data, ok := s[key]
	if !ok {
		return os.ErrNotExist
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func TestReviewSnapshotImportSupportsExclusiveObjectDownload(t *testing.T) {
	f := newNamedFixture(t)
	ctx := context.Background()
	op, err := f.queue(t, "app.snapshot", map[string]interface{}{"database": "primary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execute(t, op.ID); err != nil {
		t.Fatal(err)
	}
	objects := reviewSnapshotObjects{}
	_, key, err := f.p.ExportTargetSnapshot(ctx, f.spec, "primary", "", objects, "review")
	if err != nil {
		t.Fatal(err)
	}
	f.p.DatabaseTargets.SnapshotRoot = t.TempDir()
	if _, err := f.p.ImportTargetSnapshot(ctx, f.spec, "primary", objects, "review", key); err != nil {
		t.Fatalf("import incompatible with exclusive object download: %v", err)
	}
}

func TestLegacyImportPublishesExclusively(t *testing.T) {
	directory := t.TempDir()
	key := "snapshots/demo/shop_abc1234_20260925T120000.dump"
	objects := reviewSnapshotObjects{key: []byte("dump")}
	name, err := importLegacySnapshot(context.Background(), objects, "review", key, "demo", "shop", directory)
	if err != nil || name != "shop_abc1234_20260925T120000.dump" {
		t.Fatalf("import = %q, %v", name, err)
	}
	if _, err := importLegacySnapshot(context.Background(), objects, "review", key, "demo", "shop", directory); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second import must preserve the first publication: %v", err)
	}
	if _, err := importLegacySnapshot(context.Background(), objects, "review", "snapshots/other/shop_abc1234_20260925T120000.dump", "demo", "shop", directory); err == nil {
		t.Fatal("foreign app key accepted")
	}
}
