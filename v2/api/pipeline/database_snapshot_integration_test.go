package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func (f *targetFixture) queueKey(t *testing.T, key, kind string, payload map[string]interface{}) (store.AcceptedOperation, error) {
	t.Helper()
	request := f.request
	request.Key = key
	operation := model.Operation{ID: uuid.NewString(), Kind: kind, App: f.app, SagaID: uuid.NewString(), Status: model.OperationQueued, Source: "control-api", Payload: payload, Metadata: map[string]interface{}{}, MaxAttempts: 1}
	return f.p.QueueOperation(context.Background(), operation, request)
}

func (f *targetFixture) withLegacyGeneration(generation uint64) database.Catalog {
	catalog := f.catalog
	legacy := *f.catalog.Profiles[0].LegacyPostgres
	legacy.Generation = generation
	catalog.Profiles = []database.DeploymentProfile{f.catalog.Profiles[0]}
	catalog.Profiles[0].LegacyPostgres = &legacy
	return catalog
}

func directoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// An identical retry after a catalog revision returns the original receipt
// and target; a changed request under the same key still conflicts; and the
// original work is fenced as stale at execution.
func TestDatabaseTargetsReplayAfterCatalogChangeReturnsOriginalReceipt(t *testing.T) {
	f := newTargetFixture(t)
	ctx := context.Background()
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, f.catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	payload := map[string]interface{}{"snapshot": f.appDBName + "_manual_20260922T120000.dump"}
	first, err := f.queueKey(t, "replay-after-catalog", "app.snapshot-restore", payload)
	if err != nil || first.Replayed {
		t.Fatalf("first acceptance = %+v, %v", first, err)
	}
	original, err := recordedTargetFromPayload(first.Operation.Payload)
	if err != nil || original == nil || original.CatalogRevision != 1 || original.Target.BindingGeneration != 1 {
		t.Fatalf("original target = %+v, %v", original, err)
	}
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 1, f.withLegacyGeneration(2), "operator"); err != nil {
		t.Fatal(err)
	}

	retry, err := f.queueKey(t, "replay-after-catalog", "app.snapshot-restore", payload)
	if err != nil || !retry.Replayed || retry.Operation.ID != first.Operation.ID || retry.AcceptanceIntentID != first.AcceptanceIntentID {
		t.Fatalf("identical retry after catalog change = %+v, %v", retry, err)
	}
	replayed, err := recordedTargetFromPayload(retry.Operation.Payload)
	if err != nil || replayed == nil || *replayed != *original {
		t.Fatalf("retry target = %+v, want original %+v (%v)", replayed, original, err)
	}
	changed := map[string]interface{}{"snapshot": f.appDBName + "_manual_20260922T120001.dump"}
	if _, err := f.queueKey(t, "replay-after-catalog", "app.snapshot-restore", changed); !errors.Is(err, store.ErrAcceptanceConflict) {
		t.Fatalf("changed request under the same key = %v", err)
	}
	var operations int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE app=$1`, f.app).Scan(&operations); err != nil || operations != 1 {
		t.Fatalf("operations after replay = %d, %v", operations, err)
	}

	var resolverErr *database.ResolverError
	if _, err := f.execute(t, first.Operation.ID); !errors.As(err, &resolverErr) || resolverErr.Code != database.CodeStaleTarget {
		t.Fatalf("stale replayed execution = %v", err)
	}
	if state := f.orderState(t); state != "original" {
		t.Fatalf("stale restore changed data: %q", state)
	}
}

// Generations above 2^53 survive acceptance, persistence, claim and
// execution exactly, and are recorded exactly in the snapshot sidecar.
func TestDatabaseTargetsExactLargeGenerationsSurviveAcceptanceAndExecution(t *testing.T) {
	f := newTargetFixture(t)
	ctx := context.Background()
	const serviceGeneration uint64 = 1<<53 + 1
	const bindingGeneration uint64 = 1<<63 + 3
	catalog := f.withLegacyGeneration(bindingGeneration)
	catalog.Services = append([]database.DatabaseService(nil), f.catalog.Services...)
	catalog.Services[0].Generation = serviceGeneration
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	accepted, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := f.db.GetOperation(ctx, accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := recordedTargetFromPayload(persisted.Payload)
	if err != nil || recorded == nil || recorded.Target.ServiceGeneration != serviceGeneration || recorded.Target.BindingGeneration != bindingGeneration {
		t.Fatalf("persisted target = %+v, %v", recorded, err)
	}
	result, err := f.execute(t, accepted.ID)
	if err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("large-generation execution = %+v, %v", result, err)
	}
	if generation, _ := result.Metadata["databaseBindingGeneration"].(uint64); generation != bindingGeneration {
		t.Fatalf("result generation = %#v", result.Metadata["databaseBindingGeneration"])
	}
	snapshot, _ := result.Metadata["snapshot"].(string)
	sidecar, err := readSidecar(snapshotLocation{dir: f.snapshots}, snapshot)
	if err != nil || sidecar == nil || sidecar.Target != recorded.Target {
		t.Fatalf("sidecar = %+v, %v", sidecar, err)
	}
	raw, err := os.ReadFile(filepath.Join(f.snapshots, snapshot+sidecarSuffix))
	if err != nil || !strings.Contains(string(raw), strconv.FormatUint(bindingGeneration, 10)) {
		t.Fatalf("sidecar does not carry the exact generation: %v", err)
	}
}

// Publication is sidecar-first then exclusive link, so each interruption
// point leaves a state a retry handles without fabricating provenance.
func TestDatabaseTargetSnapshotPublicationSurvivesInterruption(t *testing.T) {
	f := newTargetFixture(t)
	ctx := context.Background()
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, f.catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	accepted, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := f.p.findSpec(f.app)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := f.p.openDatabaseTarget(ctx, accepted.Payload, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	location, err := f.p.prepareSnapshotLocation(f.appDBName, bound)
	if err != nil {
		t.Fatal(err)
	}
	name := func(at time.Time) string {
		return fmt.Sprintf("%s_manual_%s.dump", f.appDBName, at.UTC().Format("20060102T150405"))
	}
	path := func(filename string) string { return filepath.Join(location.dir, filename) }
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	// Interrupted after the sidecar, before the link: the orphan keeps its
	// name and a retry publishes a verified pair under the next one.
	orphanAt := base
	orphan, _ := json.Marshal(snapshotSidecar{Schema: snapshotSidecarSchema, Target: bound.resolved.Target, CatalogRevision: 1, SHA256: strings.Repeat("0", 64), Size: 1})
	if err := os.WriteFile(path(name(orphanAt)+sidecarSuffix), orphan, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := createDataSnapshotAt(ctx, location, "manual", orphanAt, true)
	if err != nil || created.Filename != name(orphanAt.Add(time.Second)) {
		t.Fatalf("retry after orphan sidecar = %+v, %v", created, err)
	}
	if err := verifyBoundDump(location, created.Filename, created.Size); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path(name(orphanAt) + sidecarSuffix)); string(data) != string(orphan) {
		t.Fatal("orphan sidecar was rewritten")
	}

	// Completed publication: a replay reuses the verified pair and dumps nothing.
	reuseAt := base.Add(time.Hour)
	first, err := createDataSnapshotAt(ctx, location, "manual", reuseAt, true)
	if err != nil || first.Filename != name(reuseAt) {
		t.Fatalf("first publication = %+v, %v", first, err)
	}
	before := directoryNames(t, location.dir)
	again, err := createDataSnapshotAt(ctx, location, "manual", reuseAt, true)
	if err != nil || again.Filename != first.Filename || strings.Join(directoryNames(t, location.dir), ",") != strings.Join(before, ",") {
		t.Fatalf("replay reuse = %+v, %v", again, err)
	}
	// Bytes changed under a matching sidecar are never reused.
	data, err := os.ReadFile(path(first.Filename))
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(path(first.Filename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := createDataSnapshotAt(ctx, location, "manual", reuseAt, true); err == nil || !strings.Contains(err.Error(), "differs from its target sidecar") {
		t.Fatalf("tampered reuse = %v", err)
	}

	// A dump bound to another target under the op-derived name is left
	// alone; this target's dump gets a fresh name.
	foreignAt := base.Add(2 * time.Hour)
	if err := os.WriteFile(path(name(foreignAt)), []byte("foreign dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreignTarget := bound.resolved.Target
	foreignTarget.ServiceID = "other-server-pg"
	foreign, _ := json.Marshal(snapshotSidecar{Schema: snapshotSidecarSchema, Target: foreignTarget, CatalogRevision: 1, SHA256: strings.Repeat("1", 64), Size: 12})
	if err := os.WriteFile(path(name(foreignAt)+sidecarSuffix), foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := createDataSnapshotAt(ctx, location, "manual", foreignAt, true)
	if err != nil || fresh.Filename == name(foreignAt) {
		t.Fatalf("publication beside a foreign dump = %+v, %v", fresh, err)
	}
	if data, _ := os.ReadFile(path(name(foreignAt) + sidecarSuffix)); string(data) != string(foreign) {
		t.Fatal("foreign sidecar was rewritten")
	}

	// A dump without provenance under the op-derived name fails closed:
	// nothing is dumped, published or bound to it.
	unprovenAt := base.Add(3 * time.Hour)
	if err := os.WriteFile(path(name(unprovenAt)), []byte("unproven dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	before = directoryNames(t, location.dir)
	if _, err := createDataSnapshotAt(ctx, location, "manual", unprovenAt, true); err == nil || !strings.Contains(err.Error(), "without target provenance") {
		t.Fatalf("unproven reuse = %v", err)
	}
	if after := directoryNames(t, location.dir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("unproven reuse changed the namespace: %v -> %v", before, after)
	}

	// A fresh (non-replay) dump racing an unbound file at its candidate name
	// withdraws its sidecar there and publishes under the next name.
	raceAt := base.Add(4 * time.Hour)
	if err := os.WriteFile(path(name(raceAt)), []byte("unbound racer"), 0o600); err != nil {
		t.Fatal(err)
	}
	raced, err := createDataSnapshotAt(ctx, location, "manual", raceAt, false)
	if err != nil || raced.Filename != name(raceAt.Add(time.Second)) {
		t.Fatalf("publication beside an unbound racer = %+v, %v", raced, err)
	}
	if _, err := os.Stat(path(name(raceAt) + sidecarSuffix)); !os.IsNotExist(err) {
		t.Fatal("withdrawn sidecar left bound to an unrelated dump")
	}
	// Unbound files that were not inventoried at adoption are never listed.
	snapshots, err := listDataSnapshots(location)
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range snapshots {
		if snapshot.sidecar == nil && snapshot.adopted == nil {
			t.Fatalf("unbound dump listed: %s", snapshot.Filename)
		}
	}
}

// The flat legacy namespace has one recorded owner. Pre-v3 dumps present at
// adoption are restorable after digest verification; later unbound dumps and
// other owners are refused.
func TestLegacySnapshotNamespaceHasOneOwnerAndAdoptionInventory(t *testing.T) {
	f := newTargetFixture(t)
	ctx := context.Background()
	catalog := f.catalog
	other := *f.catalog.Profiles[0].LegacyPostgres
	other.MappingID = "other-legacy-pg"
	otherProfile := f.catalog.Profiles[0]
	otherProfile.ID = "mini-other"
	otherProfile.LegacyPostgres = &other
	catalog.Profiles = []database.DeploymentProfile{f.catalog.Profiles[0], otherProfile}
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	// Produce a real dump of the target, then place its bytes as a pre-v3
	// (sidecar-less) file in a fresh flat namespace.
	source, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.execute(t, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	dump, err := os.ReadFile(filepath.Join(f.snapshots, result.Metadata["snapshot"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := t.TempDir()
	preV3 := f.appDBName + "_manual_20250101T000000.dump"
	tamperLater := f.appDBName + "_manual_20250102T000000.dump"
	for _, filename := range []string{preV3, tamperLater} {
		if err := os.WriteFile(filepath.Join(legacyRoot, filename), dump, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.p.DatabaseTargets.SnapshotRoot = legacyRoot

	restore := func(filename string) error {
		t.Helper()
		op, err := f.queue(t, "app.snapshot-restore", map[string]interface{}{"snapshot": filename})
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.execute(t, op.ID)
		return err
	}
	if _, err := f.appDB.Exec(ctx, `UPDATE `+pgx.Identifier{f.appSchema, "orders"}.Sanitize()+` SET state='mutated'`); err != nil {
		t.Fatal(err)
	}
	if err := restore(preV3); err != nil {
		t.Fatalf("restore of an adopted pre-v3 dump = %v", err)
	}
	if state := f.orderState(t); state != "original" {
		t.Fatalf("adopted restore state = %q", state)
	}
	owner, err := readLegacyOwner(filepath.Join(legacyRoot, legacyOwnerDirectory, f.appDBName+".json"))
	if err != nil || owner == nil || owner.ProfileID != "mini-local" || owner.MappingID != "mini-legacy-pg" || len(owner.Inventory) != 2 {
		t.Fatalf("owner manifest = %+v, %v", owner, err)
	}

	if _, err := f.appDB.Exec(ctx, `UPDATE `+pgx.Identifier{f.appSchema, "orders"}.Sanitize()+` SET state='kept'`); err != nil {
		t.Fatal(err)
	}
	// An unbound dump written after adoption is not in the inventory.
	late := f.appDBName + "_manual_20250103T000000.dump"
	if err := os.WriteFile(filepath.Join(legacyRoot, late), dump, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restore(late); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("restore of a post-adoption unbound dump = %v", err)
	}
	// Inventoried bytes changed at the same size are refused before restore.
	changed := append([]byte(nil), dump...)
	changed[len(changed)/2] ^= 0xff
	if err := os.WriteFile(filepath.Join(legacyRoot, tamperLater), changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restore(tamperLater); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("restore of a changed adopted dump = %v", err)
	}
	if state := f.orderState(t); state != "kept" {
		t.Fatalf("refused restores changed data: %q", state)
	}

	// Another profile's legacy mapping cannot share the namespace.
	before := directoryNames(t, legacyRoot)
	f.p.DatabaseTargets.ProfileID = "mini-other"
	foreignOwner, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execute(t, foreignOwner.ID); !errors.Is(err, errLegacyNamespaceOwned) {
		t.Fatalf("second owner = %v", err)
	}
	if after := directoryNames(t, legacyRoot); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("refused owner changed the namespace: %v -> %v", before, after)
	}

	// The inventory belongs to the target adopted at generation 1: after a
	// generation bump the same owner keeps the namespace, but the adopted
	// dumps are foreign and neither restored nor pruned.
	f.p.DatabaseTargets.ProfileID = "mini-local"
	bumped := catalog
	bumpedLegacy := *catalog.Profiles[0].LegacyPostgres
	bumpedLegacy.Generation = 2
	bumped.Profiles = []database.DeploymentProfile{catalog.Profiles[0], catalog.Profiles[1]}
	bumped.Profiles[0].LegacyPostgres = &bumpedLegacy
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 1, bumped, "operator"); err != nil {
		t.Fatal(err)
	}
	if err := restore(preV3); !errors.Is(err, errSnapshotTargetMismatch) {
		t.Fatalf("restore of a dump adopted under generation 1 at generation 2 = %v", err)
	}
	prune, err := f.queue(t, "app.snapshot-prune", map[string]interface{}{"keep": 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execute(t, prune.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(legacyRoot, preV3)); err != nil {
		t.Fatalf("prune removed a foreign adopted dump: %v", err)
	}
	if state := f.orderState(t); state != "kept" {
		t.Fatalf("refused restore changed data: %q", state)
	}
}

// The pipeline routes snapshot, restore and migration to the server the
// catalog declares, a scoped second server with the same database and role
// names, while every ambient libpq setting points at the inherited server.
func TestDatabaseTargetsRouteToDeclaredServerNotAmbientOne(t *testing.T) {
	f := newTargetFixture(t)
	ctx := context.Background()
	scoped := pgtest.Start(t)
	scoped.CreateDatabase(t, f.appDBName)
	serverB, err := pgx.Connect(ctx, scoped.URL(f.appDBName))
	if err != nil {
		t.Fatal(err)
	}
	defer serverB.Close(context.Background())
	if f.appRole != scoped.User {
		if _, err := serverB.Exec(ctx, `CREATE ROLE `+pgx.Identifier{f.appRole}.Sanitize()+` LOGIN SUPERUSER`); err != nil {
			t.Fatal(err)
		}
	}
	identifier := pgx.Identifier{f.appSchema}.Sanitize()
	if _, err := serverB.Exec(ctx, `CREATE SCHEMA `+identifier+`; CREATE TABLE `+identifier+`.orders (id int, state text); INSERT INTO `+identifier+`.orders VALUES (1, 'server-b')`); err != nil {
		t.Fatal(err)
	}
	stateB := func() string {
		t.Helper()
		var state string
		if err := serverB.QueryRow(ctx, `SELECT state FROM `+identifier+`.orders WHERE id=1`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	catalog := f.catalog
	catalog.Services = append([]database.DatabaseService(nil), f.catalog.Services...)
	catalog.Services[0].ProviderRef = "local:scoped-server-b"
	catalog.Services[0].Endpoint = database.DatabaseEndpoint{Host: scoped.SocketDir, Port: scoped.Port}
	if _, err := f.db.ActivateDatabaseCatalog(ctx, 0, catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	// Every ambient default names server A (PGDATABASE and NORN_DATABASE_URL
	// are already set by the fixture).
	t.Setenv("PGHOST", f.appHost)
	t.Setenv("PGPORT", strconv.Itoa(f.appPort))
	t.Setenv("PGUSER", f.appRole)
	t.Setenv("PGPASSWORD", targetCanary)
	t.Setenv("PGOPTIONS", "-c search_path="+f.appSchema)

	snapshotOp, err := f.queue(t, "app.snapshot", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.execute(t, snapshotOp.ID)
	if err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("snapshot = %+v, %v", result, err)
	}
	snapshotFile, _ := result.Metadata["snapshot"].(string)
	if _, err := serverB.Exec(ctx, `UPDATE `+identifier+`.orders SET state='mutated-b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.appDB.Exec(ctx, `UPDATE `+identifier+`.orders SET state='server-a-untouched'`); err != nil {
		t.Fatal(err)
	}
	restoreOp, err := f.queue(t, "app.snapshot-restore", map[string]interface{}{"snapshot": snapshotFile})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.execute(t, restoreOp.ID); err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("restore = %+v, %v", result, err)
	}
	if state := stateB(); state != "server-b" {
		t.Fatalf("server B after restore = %q", state)
	}
	if state := f.orderState(t); state != "server-a-untouched" {
		t.Fatalf("restore reached server A: %q", state)
	}

	migrateOp, err := f.queue(t, "app.migrate", map[string]interface{}{"ref": ""})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.execute(t, migrateOp.ID); err != nil || result.Status != model.OperationSucceeded {
		t.Fatalf("migrate = %+v, %v", result, err)
	}
	var onB, onA bool
	if err := serverB.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, f.appSchema+".migrated").Scan(&onB); err != nil {
		t.Fatal(err)
	}
	if err := f.appDB.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, f.appSchema+".migrated").Scan(&onA); err != nil {
		t.Fatal(err)
	}
	if !onB || onA {
		t.Fatalf("migration landed on B=%v A=%v", onB, onA)
	}
}
