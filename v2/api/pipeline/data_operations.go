package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

type dataSnapshot struct {
	Filename  string
	Timestamp string
	Size      int64
	// sidecar is the target binding recorded with the dump, when present;
	// foreign means it names a different target than the location's.
	sidecar *snapshotSidecar
	foreign bool
	// adopted is the ownership-inventory entry for a pre-v3 unbound dump in a
	// legacy-mapped namespace.
	adopted *legacySnapshot
}

var snapshotCommitLabelPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func (p *Pipeline) executeDataOperation(ctx context.Context, op *model.Operation, claim store.OperationClaim, spec *model.InfraSpec, sg *saga.Saga) (*OperationResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	set, err := p.openDatabaseTargets(ctx, op.Payload, spec)
	if err != nil {
		return nil, err
	}
	defer set.Close()
	var database string
	var bound *boundDatabase
	metadata := map[string]interface{}{}
	if spec.NamedDatabases() {
		logical := spec.EffectiveMigrationDatabase()
		if op.Kind != "app.migrate" {
			if logical, err = selectedDatabase(spec, op.Payload); err != nil {
				return nil, err
			}
		}
		if bound = set.named[logical]; bound == nil {
			return nil, &DatabaseTargetError{Reason: fmt.Sprintf("database %q was not bound at acceptance", logical)}
		}
		metadata["logicalDatabase"] = logical
	} else {
		if database, err = postgresDatabase(spec); err != nil {
			return nil, err
		}
		if set != nil {
			bound = set.legacy
		}
	}
	metadata["database"] = database
	if bound != nil {
		required := map[string][]dbCapability{
			"app.snapshot": {dbSnapshot}, "app.snapshot-prune": {dbSnapshot}, "app.snapshot-import": {dbSnapshot}, "app.snapshot-export": {dbSnapshot},
			"app.snapshot-restore": {dbRestore, dbSnapshot}, "app.migrate": {dbMigration, dbSnapshot},
		}[op.Kind]
		if err := bound.requireCapabilities(required...); err != nil {
			return nil, err
		}
		database = bound.resolved.Target.Database
		metadata["database"] = database
		metadata["databaseBinding"] = bound.resolved.Target.BindingID
		metadata["databaseBindingGeneration"] = bound.resolved.Target.BindingGeneration
	}
	location, err := p.prepareSnapshotLocation(database, bound)
	if err != nil {
		return nil, err
	}
	finish := func(message string, values map[string]interface{}, publish func(context.Context)) *OperationResult {
		for key, value := range values {
			metadata[key] = value
		}
		return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: message, Metadata: metadata, publish: publish}
	}

	switch op.Kind {
	case "app.snapshot":
		if p.SnapshotEffects == nil || !p.SnapshotEffects.available() {
			return nil, &SnapshotExecutionUnavailableError{}
		}
		created, _, err := p.executeAttestedSnapshot(ctx, op, claim, bound, location)
		if err != nil {
			return nil, err
		}
		return finish("snapshot created for "+spec.App, map[string]interface{}{"snapshot": created.Filename}, func(publishCtx context.Context) {
			// Publish executes only after the worker's terminal claim CAS. A later
			// startup reconciliation retries this cleanup if this process crashes.
			_ = p.SnapshotEffects.DiscardPublishedArtifact(publishCtx, op.ID)
			_ = sg.Log(publishCtx, "snapshot.created", "manual database snapshot created", map[string]string{"snapshot": created.Filename, "database": database})
			p.broadcastDataEvent("snapshot.created", spec.App, map[string]string{"snapshot": created.Filename, "database": database, "operationId": op.ID})
		}), nil

	case "app.snapshot-prune":
		keep := intFromMap(op.Payload, "keep")
		if keep < 1 {
			return nil, fmt.Errorf("snapshot retention keep must be at least 1")
		}
		pruned, err := pruneDataSnapshots(location, keep)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return finish(fmt.Sprintf("snapshot retention kept %d for %s", keep, spec.App), map[string]interface{}{"keep": keep, "pruned": pruned}, func(publishCtx context.Context) {
			_ = sg.Log(publishCtx, "snapshot.retention", fmt.Sprintf("pruned %d database snapshots", len(pruned)), map[string]string{"database": database, "keep": fmt.Sprintf("%d", keep)})
			p.broadcastDataEvent("snapshot.retention", spec.App, map[string]string{"database": database, "keep": fmt.Sprintf("%d", keep), "operationId": op.ID})
		}), nil

	case "app.snapshot-import":
		if p.SnapshotObjects == nil || spec.Snapshots == nil || spec.Snapshots.ExportBucket == "" {
			return nil, fmt.Errorf("snapshot import object storage is unavailable")
		}
		bucket, key := stringFromMap(op.Payload, "bucket"), stringFromMap(op.Payload, "key")
		if bucket != spec.Snapshots.ExportBucket || !strings.HasPrefix(key, "snapshots/"+spec.App+"/") || strings.Contains(key, "..") {
			return nil, fmt.Errorf("signed snapshot import bucket or key differs from current app configuration")
		}
		if spec.NamedDatabases() || bound != nil {
			logical := stringFromMap(op.Payload, "database")
			manifest, err := p.ImportTargetSnapshot(ctx, spec, logical, p.SnapshotObjects, bucket, key)
			if err != nil {
				return nil, err
			}
			return finish("snapshot imported for "+spec.App, map[string]interface{}{"snapshot": manifest.Filename, "key": key, "bucket": bucket}, func(publishCtx context.Context) {
				_ = sg.Log(publishCtx, "snapshot.imported", "target-bound snapshot imported", map[string]string{"snapshot": manifest.Filename, "key": key})
				p.broadcastDataEvent("snapshot.imported", spec.App, map[string]string{"snapshot": manifest.Filename, "operationId": op.ID})
			}), nil
		}
		filename, err := importLegacySnapshot(ctx, p.SnapshotObjects, bucket, key, spec.App, database, location.dir)
		if err != nil {
			return nil, err
		}
		return finish("snapshot imported for "+spec.App, map[string]interface{}{"snapshot": filename, "key": key, "bucket": bucket}, func(publishCtx context.Context) {
			_ = sg.Log(publishCtx, "snapshot.imported", "legacy snapshot imported", map[string]string{"snapshot": filename, "key": key})
			p.broadcastDataEvent("snapshot.imported", spec.App, map[string]string{"snapshot": filename, "operationId": op.ID})
		}), nil

	case "app.snapshot-export":
		if spec.Snapshots == nil || spec.Snapshots.ExportBucket == "" || stringFromMap(op.Payload, "bucket") != spec.Snapshots.ExportBucket {
			return nil, fmt.Errorf("signed snapshot export bucket differs from current app configuration")
		}
		logical, filename := stringFromMap(op.Payload, "database"), stringFromMap(op.Payload, "snapshot")
		if bound == nil && !spec.NamedDatabases() {
			createOnly, ok := p.SnapshotObjects.(snapshotCreateOnlyObjectStore)
			if !ok {
				return nil, fmt.Errorf("legacy snapshot export object store lacks create-only publication")
			}
			key, err := exportLegacySnapshotClaimed(ctx, createOnly, spec.Snapshots.ExportBucket, spec.App, database, filename, location.dir, op.ID, op.StartedAt)
			if err != nil {
				return nil, err
			}
			return finish("snapshot exported for "+spec.App, map[string]interface{}{"snapshot": filename, "key": key, "bucket": spec.Snapshots.ExportBucket}, func(publishCtx context.Context) {
				_ = sg.Log(publishCtx, "snapshot.exported", "legacy snapshot exported", map[string]string{"snapshot": filename, "key": key})
				p.broadcastDataEvent("snapshot.exported", spec.App, map[string]string{"snapshot": filename, "operationId": op.ID})
			}), nil
		}
		manifest, key, err := p.ExportTargetSnapshotClaimed(ctx, spec, logical, filename, p.SnapshotObjects, spec.Snapshots.ExportBucket, op.ID)
		if err != nil {
			return nil, err
		}
		return finish("snapshot exported for "+spec.App, map[string]interface{}{"snapshot": manifest.Filename, "key": key, "bucket": spec.Snapshots.ExportBucket}, func(publishCtx context.Context) {
			_ = sg.Log(publishCtx, "snapshot.exported", "target-bound snapshot exported", map[string]string{"snapshot": manifest.Filename, "key": key})
			p.broadcastDataEvent("snapshot.exported", spec.App, map[string]string{"snapshot": manifest.Filename, "operationId": op.ID})
		}), nil

	case "app.snapshot-restore":
		identifier := stringFromMap(op.Payload, "snapshot")
		if identifier == "" {
			identifier = stringFromMap(op.Payload, "timestamp") // pre-v2.20 compatibility
		}
		target, err := findDataSnapshot(location, identifier)
		if err != nil {
			return nil, err
		}
		if err := verifySnapshotContent(location, *target); err != nil {
			return nil, err
		}
		safety, err := createDataSnapshot(ctx, location, "pre-restore")
		if err != nil {
			return nil, fmt.Errorf("pre-restore snapshot: %w", err)
		}
		if err := restoreDataSnapshot(ctx, location, *target); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return finish("snapshot restored for "+spec.App, map[string]interface{}{"snapshot": target.Filename, "preRestoreSnapshot": safety.Filename}, func(publishCtx context.Context) {
			_ = sg.Log(publishCtx, "snapshot.restored", "database snapshot restored", map[string]string{"snapshot": target.Filename, "preRestoreSnapshot": safety.Filename, "database": database})
			p.broadcastDataEvent("snapshot.restored", spec.App, map[string]string{"snapshot": target.Filename, "preRestoreSnapshot": safety.Filename, "operationId": op.ID})
		}), nil

	case "app.migrate":
		if strings.TrimSpace(spec.Migrations) == "" {
			return nil, fmt.Errorf("app has no migrations command")
		}
		st := &state{spec: spec, commitSHA: op.Ref, sourceRef: op.Ref, database: set, databaseOpened: true}
		defer func() {
			if st.workDir != "" {
				_ = os.RemoveAll(st.workDir)
			}
		}()
		if err := p.clone(ctx, st, sg); err != nil {
			return nil, fmt.Errorf("prepare migration source: %w", err)
		}
		safety, err := createDataSnapshot(ctx, location, "pre-migrate")
		if err != nil {
			return nil, fmt.Errorf("pre-migration snapshot: %w", err)
		}
		if err := p.migrate(ctx, st, sg); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return finish("schema migration completed for "+spec.App, map[string]interface{}{"snapshot": safety.Filename, "commitSha": st.commitSHA}, func(publishCtx context.Context) {
			_ = sg.Log(publishCtx, "migration.completed", "schema migration completed", map[string]string{"snapshot": safety.Filename, "database": database, "ref": op.Ref})
			p.broadcastDataEvent("migration.completed", spec.App, map[string]string{"snapshot": safety.Filename, "ref": op.Ref, "operationId": op.ID})
		}), nil
	default:
		return nil, fmt.Errorf("unsupported data operation kind %s", op.Kind)
	}
}

func (p *Pipeline) broadcastDataEvent(eventType, app string, payload map[string]string) {
	if p.WS != nil {
		p.WS.Broadcast(hub.Event{Type: eventType, AppID: app, Payload: payload})
	}
}

func postgresDatabase(spec *model.InfraSpec) (string, error) {
	if spec == nil || spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil || strings.TrimSpace(spec.Infrastructure.Postgres.Database) == "" {
		return "", fmt.Errorf("app has no postgres database")
	}
	database := spec.Infrastructure.Postgres.Database
	if !model.IsSafePostgresDatabaseName(database) {
		return "", fmt.Errorf("app postgres database name is unsafe for snapshot storage")
	}
	return database, nil
}

func legacySnapshotLocation(database string) snapshotLocation {
	return snapshotLocation{dir: defaultSnapshotRoot, database: database}
}

func createDataSnapshot(ctx context.Context, location snapshotLocation, label string) (*dataSnapshot, error) {
	return createDataSnapshotAt(ctx, location, label, time.Now().UTC(), false)
}

func createDataSnapshotAt(ctx context.Context, location snapshotLocation, label string, createdAt time.Time, reuse bool) (*dataSnapshot, error) {
	return createDataSnapshotAtMode(ctx, location, label, createdAt, reuse, false)
}

// Pinned publications must never advance to a second filename on replay.
func createPinnedDataSnapshotAt(ctx context.Context, location snapshotLocation, label string, createdAt time.Time) (*dataSnapshot, error) {
	if location.bound == nil {
		return nil, fmt.Errorf("pinned snapshot requires target provenance")
	}
	return createDataSnapshotAtMode(ctx, location, label, createdAt, true, true)
}

// Legacy dumps have no target sidecar. A replay must stop at its operation
// name instead of trusting existing bytes or creating a second safety dump.
func createPinnedLegacySnapshotAt(ctx context.Context, location snapshotLocation, label string, createdAt time.Time) (*dataSnapshot, error) {
	if location.bound != nil {
		return nil, fmt.Errorf("legacy snapshot must not have a target binding")
	}
	return createDataSnapshotAtMode(ctx, location, label, createdAt, false, true)
}

func createDataSnapshotAtMode(ctx context.Context, location snapshotLocation, label string, createdAt time.Time, reuse, pinned bool) (*dataSnapshot, error) {
	database, directory := location.database, location.dir
	if !model.IsSafePostgresDatabaseName(database) {
		return nil, fmt.Errorf("unsafe postgres database name")
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, fmt.Errorf("create snapshots directory: %w", err)
	}
	timestamp := createdAt.UTC().Format("20060102T150405")
	filename := fmt.Sprintf("%s_%s_%s.dump", database, label, timestamp)
	path := filepath.Join(directory, filename)
	if info, err := os.Lstat(path); err == nil {
		if pinned && location.bound == nil {
			return nil, fmt.Errorf("pinned legacy snapshot already exists; inspect before retry")
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("snapshot target %s is not a regular file", filename)
		}
		if reuse && info.Size() > 0 && location.bound != nil {
			// A replayed operation's dump is reused only with verified
			// provenance for this target. Unknown bytes fail closed; a dump
			// bound to another target is left alone and a new name is chosen.
			switch err := verifyBoundDump(location, filename, info.Size()); {
			case err == nil:
				return &dataSnapshot{Filename: filename, Timestamp: timestamp, Size: info.Size()}, nil
			case errors.Is(err, errSnapshotTargetMismatch):
				if pinned {
					return nil, fmt.Errorf("pinned snapshot name belongs to another target: %w", err)
				}
				reuse = false
			default:
				return nil, err
			}
		} else if reuse && info.Size() > 0 {
			return &dataSnapshot{Filename: filename, Timestamp: timestamp, Size: info.Size()}, nil
		}
		if reuse {
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("remove incomplete snapshot %s: %w", filename, err)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect snapshot target: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".norn-snapshot-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("reserve snapshot file: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, fmt.Errorf("close snapshot reservation: %w", err)
	}
	defer os.Remove(temporaryPath)

	if location.bound != nil {
		session := location.bound.session
		args := append([]string{"-Fc", "-d", session.ServiceArgument(), "-f", temporaryPath}, location.dumpScope...)
		if output, err := runCaptured(session.Command(ctx, "pg_dump", args...)); err != nil {
			return nil, fmt.Errorf("pg_dump: %s", session.RedactCaptured(output))
		}
	} else {
		cmd := exec.CommandContext(ctx, "pg_dump", "-Fc", "-d", database, "-f", temporaryPath)
		if output, err := runCaptured(cmd); err != nil {
			return nil, fmt.Errorf("pg_dump: %s", strings.TrimSpace(output.String()))
		}
	}
	info, err := os.Lstat(temporaryPath)
	if err != nil {
		return nil, fmt.Errorf("stat snapshot: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, fmt.Errorf("pg_dump did not create a non-empty regular snapshot")
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return nil, fmt.Errorf("secure snapshot permissions: %w", err)
	}
	digest := ""
	if location.bound != nil {
		if digest, err = fileSHA256(temporaryPath); err != nil {
			return nil, err
		}
	}

	// Hard-linking publishes the completed dump without overwriting an existing
	// snapshot. If two safe snapshots land in the same second, advance the
	// display timestamp until an unused filename is found. A bound dump's
	// sidecar is written exclusively first, so a published bound dump always
	// has its provenance and an interrupted publication leaves at most an
	// orphan sidecar, never an unbound dump in a target namespace.
	for offset := 0; offset < 1000; offset++ {
		if pinned && offset != 0 {
			return nil, fmt.Errorf("pinned snapshot name is unavailable")
		}
		candidateTime := createdAt.UTC().Add(time.Duration(offset) * time.Second)
		candidateTimestamp := candidateTime.Format("20060102T150405")
		candidateFilename := fmt.Sprintf("%s_%s_%s.dump", database, label, candidateTimestamp)
		candidatePath := filepath.Join(directory, candidateFilename)
		if location.bound != nil {
			if err := writeSidecarExclusive(location, candidateFilename, digest, info.Size()); errors.Is(err, fs.ErrExist) {
				continue
			} else if err != nil {
				return nil, err
			}
		}
		if linkErr := os.Link(temporaryPath, candidatePath); linkErr == nil {
			return &dataSnapshot{Filename: candidateFilename, Timestamp: candidateTimestamp, Size: info.Size()}, nil
		} else {
			if location.bound != nil {
				// Our fresh sidecar must never describe a different dump.
				_ = os.Remove(filepath.Join(directory, candidateFilename+sidecarSuffix))
			}
			if !errors.Is(linkErr, fs.ErrExist) {
				return nil, fmt.Errorf("publish snapshot: %w", linkErr)
			}
		}
		if reuse && offset == 0 && location.bound == nil {
			existing, statErr := os.Lstat(candidatePath)
			if statErr == nil && existing.Mode().IsRegular() && existing.Size() > 0 {
				return &dataSnapshot{Filename: candidateFilename, Timestamp: candidateTimestamp, Size: existing.Size()}, nil
			}
		}
	}
	return nil, fmt.Errorf("could not allocate a unique snapshot filename")
}

func listDataSnapshots(location snapshotLocation) ([]dataSnapshot, error) {
	database := location.database
	if !model.IsSafePostgresDatabaseName(database) {
		return nil, fmt.Errorf("unsafe postgres database name")
	}
	entries, err := os.ReadDir(location.dir)
	if os.IsNotExist(err) {
		return []dataSnapshot{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]dataSnapshot, 0)
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(entry.Name(), database+"_") || !strings.HasSuffix(entry.Name(), ".dump") {
			continue
		}
		parts := strings.Split(strings.TrimSuffix(entry.Name(), ".dump"), "_")
		if len(parts) < 3 {
			continue
		}
		timestamp := parts[len(parts)-1]
		if len(timestamp) != 15 || timestamp[8] != 'T' {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		snapshot := dataSnapshot{Filename: entry.Name(), Timestamp: timestamp, Size: info.Size()}
		if location.bound != nil {
			sidecar, err := readSidecar(location, entry.Name())
			if err != nil {
				return nil, err
			}
			snapshot.sidecar = sidecar
			if sidecar != nil {
				snapshot.foreign = sidecar.Target != location.bound.resolved.Target
			} else {
				// Unbound dumps are restorable only when inventoried at
				// adoption by this legacy namespace's owner, at that size,
				// and only for the exact target they were adopted for.
				adopted, ok := location.adopted[entry.Name()]
				if !ok || adopted.Size != info.Size() {
					continue
				}
				snapshot.adopted = &adopted
				snapshot.foreign = location.adoptedTarget != location.bound.resolved.Target
			}
		}
		result = append(result, snapshot)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Timestamp == result[j].Timestamp {
			return result[i].Filename < result[j].Filename
		}
		return result[i].Timestamp > result[j].Timestamp
	})
	return result, nil
}

func findDataSnapshot(location snapshotLocation, identifier string) (*dataSnapshot, error) {
	snapshots, err := listDataSnapshots(location)
	if err != nil {
		return nil, err
	}
	var target *dataSnapshot
	for i := range snapshots {
		if snapshots[i].Filename == identifier {
			target = &snapshots[i]
			break
		}
	}
	if target == nil {
		for i := range snapshots {
			if snapshots[i].Timestamp == identifier {
				if target != nil {
					return nil, fmt.Errorf("snapshot timestamp %s is ambiguous; retry with its inventory filename", identifier)
				}
				target = &snapshots[i]
			}
		}
	}
	if target == nil {
		return nil, fmt.Errorf("snapshot %s was not found", identifier)
	}
	if target.foreign {
		// Cross-target restore needs explicit, reviewed intent; a matching
		// file name is never proof of the intended target.
		return nil, fmt.Errorf("snapshot %s: %w", target.Filename, errSnapshotTargetMismatch)
	}
	return target, nil
}

func pruneDataSnapshots(location snapshotLocation, keep int) ([]string, error) {
	all, err := listDataSnapshots(location)
	if err != nil {
		return nil, err
	}
	snapshots := make([]dataSnapshot, 0, len(all))
	for _, snapshot := range all {
		if !snapshot.foreign {
			snapshots = append(snapshots, snapshot)
		}
	}
	if len(snapshots) <= keep {
		return []string{}, nil
	}
	pruned := make([]string, 0, len(snapshots)-keep)
	for _, snapshot := range snapshots[keep:] {
		if err := os.Remove(filepath.Join(location.dir, snapshot.Filename)); err != nil {
			return pruned, fmt.Errorf("prune snapshot %s: %w", snapshot.Filename, err)
		}
		if snapshot.sidecar != nil {
			_ = os.Remove(filepath.Join(location.dir, snapshot.Filename+sidecarSuffix))
		}
		pruned = append(pruned, snapshot.Filename)
	}
	return pruned, nil
}

func intFromMap(values map[string]interface{}, key string) int {
	switch value := values[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}
