package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/saga"
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

func (s reviewSnapshotObjects) PutObjectIfAbsent(ctx context.Context, bucket, key, path string) error {
	if _, exists := s[key]; exists {
		return os.ErrExist
	}
	return s.PutObject(ctx, bucket, key, path)
}

type expiringSnapshotObjects struct {
	reviewSnapshotObjects
	afterWrite  func(context.Context) error
	expireAfter int
	writes      int
}

func (s *expiringSnapshotObjects) PutObjectIfAbsent(ctx context.Context, bucket, key, path string) error {
	if err := s.reviewSnapshotObjects.PutObjectIfAbsent(ctx, bucket, key, path); err != nil {
		return err
	}
	s.writes++
	if s.afterWrite != nil && s.writes == s.expireAfter {
		callback := s.afterWrite
		s.afterWrite = nil
		return callback(ctx)
	}
	return nil
}

func TestClaimedSnapshotImport(t *testing.T) {
	f := newNamedFixture(t)
	ctx := context.Background()
	created, err := f.queue(t, "app.snapshot", map[string]interface{}{"database": "primary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execute(t, created.ID); err != nil {
		t.Fatal(err)
	}
	objects := reviewSnapshotObjects{}
	_, key, err := f.p.ExportTargetSnapshot(ctx, f.spec, "primary", "", objects, "review")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.p.AppsDir, f.app, "infraspec.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("snapshots:\n  exportBucket: review\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	f.spec, err = model.LoadInfraSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	f.p.SnapshotObjects = objects
	f.p.DatabaseTargets.SnapshotRoot = t.TempDir()
	accepted, err := f.queue(t, "app.snapshot-import", map[string]interface{}{"database": "primary", "bucket": "review", "key": key})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.execute(t, accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.OperationSucceeded || result.Metadata["key"] != key {
		t.Fatalf("claimed import result = %+v", result)
	}
	if _, err := f.p.ImportTargetSnapshot(ctx, f.spec, "primary", objects, "review", key); err != nil {
		t.Fatalf("claimed operation did not publish a valid target snapshot: %v", err)
	}
}

func TestClaimedSnapshotExportUsesPrivateRemoteKey(t *testing.T) {
	f := newNamedFixture(t)
	op, err := f.queue(t, "app.snapshot", map[string]interface{}{"database": "primary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execute(t, op.ID); err != nil {
		t.Fatal(err)
	}
	groups, err := f.p.TargetSnapshots(context.Background(), f.spec)
	if err != nil {
		t.Fatal(err)
	}
	var filename string
	for _, group := range groups {
		if group.Database == "primary" && len(group.Snapshots) > 0 {
			filename = group.Snapshots[0].Filename
		}
	}
	if filename == "" {
		t.Fatal("snapshot inventory did not find the created dump")
	}
	objects := reviewSnapshotObjects{}
	manifest, key, err := f.p.ExportTargetSnapshotClaimed(context.Background(), f.spec, "primary", filename, objects, "review", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(key, "/operations/"+op.ID+"/") || manifest.Filename != filename || len(objects[key]) == 0 || len(objects[key+snapshotManifestSuffix]) == 0 {
		t.Fatalf("claimed export key=%q manifest=%+v", key, manifest)
	}
	analytics, err := f.queue(t, "app.snapshot", map[string]interface{}{"database": "analytics"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execute(t, analytics.ID); err != nil {
		t.Fatal(err)
	}
	groups, err = f.p.TargetSnapshots(context.Background(), f.spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		if group.Database == "analytics" && len(group.Snapshots) > 0 {
			_, otherKey, err := f.p.ExportTargetSnapshotClaimed(context.Background(), f.spec, "analytics", group.Snapshots[0].Filename, objects, "review", op.ID)
			if err != nil || otherKey == key || !strings.Contains(otherKey, "/databases/analytics/") {
				t.Fatalf("same deployment operation conflated database keys %q and %q: %v", key, otherKey, err)
			}
		}
	}
	if _, replayKey, err := f.p.ExportTargetSnapshotClaimed(context.Background(), f.spec, "primary", filename, objects, "review", op.ID); err != nil || replayKey != key {
		t.Fatalf("same operation did not verify its pinned export: %q, %v", replayKey, err)
	}
}

func TestClaimedSnapshotExportOperation(t *testing.T) {
	f := newNamedFixture(t)
	created, err := f.queue(t, "app.snapshot", map[string]interface{}{"database": "primary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execute(t, created.ID); err != nil {
		t.Fatal(err)
	}
	groups, err := f.p.TargetSnapshots(context.Background(), f.spec)
	if err != nil {
		t.Fatal(err)
	}
	var filename string
	for _, group := range groups {
		if group.Database == "primary" && len(group.Snapshots) > 0 {
			filename = group.Snapshots[0].Filename
		}
	}
	if filename == "" {
		t.Fatal("no pinned snapshot")
	}
	path := filepath.Join(f.p.AppsDir, f.app, "infraspec.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("snapshots:\n  exportBucket: review\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	f.spec, err = model.LoadInfraSpec(path)
	if err != nil {
		t.Fatal(err)
	}
	objects := reviewSnapshotObjects{}
	f.p.SnapshotObjects = objects
	accepted, err := f.queue(t, "app.snapshot-export", map[string]interface{}{"database": "primary", "bucket": "review", "snapshot": filename})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.execute(t, accepted.ID)
	if err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("claimed export = %+v, %v", result, err)
	}
	key, _ := result.Metadata["key"].(string)
	if !strings.Contains(key, "/operations/"+accepted.ID+"/") || len(objects[key]) == 0 || len(objects[key+snapshotManifestSuffix]) == 0 {
		t.Fatalf("claimed export missing verified objects at %q", key)
	}
}

func TestPredeploySnapshotAutoExportUsesClaimedPublication(t *testing.T) {
	f := newNamedFixture(t)
	f.spec.Snapshots = &model.SnapshotPolicy{ExportBucket: "review"}
	objects := reviewSnapshotObjects{}
	f.p.SnapshotObjects = objects
	op, err := f.queue(t, "app.snapshot", map[string]interface{}{"database": "primary"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	claimed, claim, err := f.db.ClaimNextOperation(ctx, "predeploy-export-worker", 60_000_000_000, []string{"app.snapshot"})
	if err != nil || claimed == nil || claimed.ID != op.ID {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	set, err := f.p.openDatabaseTargets(ctx, claimed.Payload, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	target := set.named["primary"]
	st := &state{spec: f.spec, claim: claim, commitSHA: "abc1234", operationStartedAt: op.StartedAt}
	sg := saga.NewWithID(f.p.SagaStore, op.SagaID, f.app, "pipeline", "deploy")
	if err := f.p.snapshotTarget(ctx, st, sg, target.resolved.Target.Database, target, "abc1234"); err != nil {
		t.Fatal(err)
	}
	if err := f.p.snapshotTarget(ctx, st, sg, target.resolved.Target.Database, target, "abc1234"); err != nil {
		t.Fatalf("predeploy snapshot replay: %v", err)
	}
	location, err := f.p.prepareSnapshotLocation(target.resolved.Target.Database, target)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := listDataSnapshots(location)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("replayed predeploy snapshots = %+v, %v", snapshots, err)
	}
	st.operationStartedAt = time.Time{}
	if err := f.p.snapshotTarget(ctx, st, sg, target.resolved.Target.Database, target, "abc1234"); err == nil || !strings.Contains(err.Error(), "start time is unavailable") {
		t.Fatalf("missing replay identity = %v", err)
	}
	found := false
	for key := range objects {
		if strings.Contains(key, "/operations/"+op.ID+"/databases/primary/") && strings.HasSuffix(key, snapshotManifestSuffix) {
			found = true
		}
	}
	if !found {
		t.Fatal("predeploy snapshot did not publish an operation-bound manifest")
	}
}

func TestPredeploySnapshotDoesNotAdvanceAfterClaimLostDuringExport(t *testing.T) {
	for _, expireAfter := range []int{1, 2} {
		t.Run(fmt.Sprintf("after-remote-write-%d", expireAfter), func(t *testing.T) {
			f := newNamedFixture(t)
			f.spec.Snapshots = &model.SnapshotPolicy{ExportBucket: "review"}
			op, err := f.queue(t, "app.snapshot", map[string]interface{}{"database": "primary"})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			claimed, claim, err := f.db.ClaimNextOperation(ctx, "predeploy-expiry-worker", time.Minute, []string{"app.snapshot"})
			if err != nil || claimed == nil || claimed.ID != op.ID {
				t.Fatalf("claim = %+v, %v", claimed, err)
			}
			set, err := f.p.openDatabaseTargets(ctx, claimed.Payload, f.spec)
			if err != nil {
				t.Fatal(err)
			}
			defer set.Close()
			objects := &expiringSnapshotObjects{reviewSnapshotObjects: reviewSnapshotObjects{}, expireAfter: expireAfter, afterWrite: func(ctx context.Context) error {
				_, err := f.db.Pool.Exec(ctx, `UPDATE operations SET locked_until = clock_timestamp() - interval '1 second' WHERE id = $1`, op.ID)
				return err
			}}
			f.p.SnapshotObjects = objects
			st := &state{spec: f.spec, claim: claim, commitSHA: "abc1234", operationStartedAt: op.StartedAt}
			sg := saga.NewWithID(f.p.SagaStore, op.SagaID, f.app, "pipeline", "deploy")
			target := set.named["primary"]
			err = f.p.snapshotTarget(ctx, st, sg, target.resolved.Target.Database, target, "abc1234")
			if err == nil || (expireAfter == 1 && !errors.Is(err, errPredeploySnapshotClaimLost)) || (expireAfter == 2 && !strings.Contains(err.Error(), "export claim is no longer current")) {
				t.Fatalf("stale deploy advanced after remote export: %v", err)
			}
			if len(objects.reviewSnapshotObjects) != expireAfter {
				t.Fatalf("remote objects after claim loss = %d, want %d", len(objects.reviewSnapshotObjects), expireAfter)
			}
		})
	}
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

func TestClaimedLegacyExportImportVerifiesRemoteBytes(t *testing.T) {
	const name = "shop_abc1234_20260925T120000.dump"
	const operationID = "c348f65c-4a3e-48d9-9d92-03c25a9b42e6"
	local := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, name), []byte("verified legacy dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	objects := reviewSnapshotObjects{}
	startedAt := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	key, err := exportLegacySnapshotClaimed(context.Background(), objects, "review", "demo", "shop", name, local, operationID, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if replayKey, err := exportLegacySnapshotClaimed(context.Background(), objects, "review", "demo", "shop", name, local, operationID, startedAt); err != nil || replayKey != key {
		t.Fatalf("legacy export replay = %q, %v", replayKey, err)
	}
	if _, err := LegacySnapshotKeyName(key, "demo", "shop"); err != nil {
		t.Fatal(err)
	}
	imported := t.TempDir()
	if got, err := importLegacySnapshot(context.Background(), objects, "review", key, "demo", "shop", imported); err != nil || got != name {
		t.Fatalf("verified import = %q, %v", got, err)
	}
	objects[key] = []byte("tampered legacy dump")
	if _, err := importLegacySnapshot(context.Background(), objects, "review", key, "demo", "shop", t.TempDir()); err == nil {
		t.Fatal("tampered remote legacy dump was imported")
	}
}

func TestPinnedLegacySnapshotRefusesExistingUnboundDump(t *testing.T) {
	location := snapshotLocation{dir: t.TempDir(), database: "shop"}
	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	name := "shop_effect-accepted_20260925T120000.dump"
	if err := os.WriteFile(filepath.Join(location.dir, name), []byte("unbound dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := createPinnedLegacySnapshotAt(context.Background(), location, "effect-accepted", at); err == nil || !strings.Contains(err.Error(), "inspect before retry") {
		t.Fatalf("unbound replay = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(location.dir, name))
	if err != nil || string(data) != "unbound dump" {
		t.Fatalf("existing dump changed: %q, %v", data, err)
	}
}

func TestClaimedLegacyExportOperation(t *testing.T) {
	p, db, request := acceptancePipelineFixture(t)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("snapshots", 0o750); err != nil {
		t.Fatal(err)
	}
	const name = "shop_abc1234_20260925T120000.dump"
	if err := os.WriteFile(filepath.Join("snapshots", name), []byte("claimed legacy dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.AppsDir = t.TempDir()
	appDir := filepath.Join(p.AppsDir, "demo")
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), []byte("name: demo\ndeploy: true\nprocesses:\n  web:\n    command: ./web\ninfrastructure:\n  postgres:\n    database: shop\nsnapshots:\n  exportBucket: review\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	objects := reviewSnapshotObjects{}
	p.SnapshotObjects = objects
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.snapshot-export", App: "demo", SagaID: uuid.NewString(), Status: model.OperationQueued,
		Source: "control-api", Payload: map[string]interface{}{"bucket": "review", "snapshot": name}, Metadata: map[string]interface{}{}, MaxAttempts: 1}
	accepted, err := p.QueueOperation(context.Background(), operation, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(context.Background(), "legacy-export-worker", 60_000_000_000, []string{"app.snapshot-export"})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	result, err := p.ExecuteOperation(context.Background(), claimed, claim)
	if err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("legacy claimed export = %+v, %v", result, err)
	}
	key, _ := result.Metadata["key"].(string)
	if !strings.Contains(key, "/operations/"+accepted.Operation.ID+"/") || len(objects[key]) == 0 || len(objects[key+snapshotManifestSuffix]) == 0 {
		t.Fatalf("legacy claimed export missing objects at %q", key)
	}
}
