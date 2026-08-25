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
)

type dataSnapshot struct {
	Filename  string
	Timestamp string
	Size      int64
}

var snapshotCommitLabelPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func (p *Pipeline) executeDataOperation(ctx context.Context, op *model.Operation, spec *model.InfraSpec, sg *saga.Saga) error {
	database, err := postgresDatabase(spec)
	if err != nil {
		return err
	}
	metadata := map[string]interface{}{"database": database}
	finish := func(message string, values map[string]interface{}) error {
		for key, value := range values {
			metadata[key] = value
		}
		return p.DB.FinishOperation(ctx, op.ID, model.OperationSucceeded, message, metadata)
	}

	switch op.Kind {
	case "app.snapshot":
		created, err := createDataSnapshotAt(ctx, database, "manual", op.StartedAt.UTC(), true)
		if err != nil {
			return err
		}
		_ = sg.Log(ctx, "snapshot.created", "manual database snapshot created", map[string]string{"snapshot": created.Filename, "database": database})
		p.broadcastDataEvent("snapshot.created", spec.App, map[string]string{"snapshot": created.Filename, "database": database, "operationId": op.ID})
		return finish("snapshot created for "+spec.App, map[string]interface{}{"snapshot": created.Filename})

	case "app.snapshot-prune":
		keep := intFromMap(op.Payload, "keep")
		if keep < 1 {
			return fmt.Errorf("snapshot retention keep must be at least 1")
		}
		pruned, err := pruneDataSnapshots(database, keep)
		if err != nil {
			return err
		}
		_ = sg.Log(ctx, "snapshot.retention", fmt.Sprintf("pruned %d database snapshots", len(pruned)), map[string]string{"database": database, "keep": fmt.Sprintf("%d", keep)})
		p.broadcastDataEvent("snapshot.retention", spec.App, map[string]string{"database": database, "keep": fmt.Sprintf("%d", keep), "operationId": op.ID})
		return finish(fmt.Sprintf("snapshot retention kept %d for %s", keep, spec.App), map[string]interface{}{"keep": keep, "pruned": pruned})

	case "app.snapshot-restore":
		identifier := stringFromMap(op.Payload, "snapshot")
		if identifier == "" {
			identifier = stringFromMap(op.Payload, "timestamp") // pre-v2.20 compatibility
		}
		target, err := findDataSnapshot(database, identifier)
		if err != nil {
			return err
		}
		safety, err := createDataSnapshot(ctx, database, "pre-restore")
		if err != nil {
			return fmt.Errorf("pre-restore snapshot: %w", err)
		}
		cmd := exec.CommandContext(ctx, "pg_restore", "--single-transaction", "--clean", "--if-exists", "-d", database, filepath.Join("snapshots", target.Filename))
		if output, restoreErr := cmd.CombinedOutput(); restoreErr != nil {
			return fmt.Errorf("pg_restore: %s", strings.TrimSpace(string(output)))
		}
		_ = sg.Log(ctx, "snapshot.restored", "database snapshot restored", map[string]string{"snapshot": target.Filename, "preRestoreSnapshot": safety.Filename, "database": database})
		p.broadcastDataEvent("snapshot.restored", spec.App, map[string]string{"snapshot": target.Filename, "preRestoreSnapshot": safety.Filename, "operationId": op.ID})
		return finish("snapshot restored for "+spec.App, map[string]interface{}{"snapshot": target.Filename, "preRestoreSnapshot": safety.Filename})

	case "app.migrate":
		if strings.TrimSpace(spec.Migrations) == "" {
			return fmt.Errorf("app has no migrations command")
		}
		st := &state{spec: spec, commitSHA: op.Ref, sourceRef: op.Ref}
		defer func() {
			if st.workDir != "" {
				_ = os.RemoveAll(st.workDir)
			}
		}()
		if err := p.clone(ctx, st, sg); err != nil {
			return fmt.Errorf("prepare migration source: %w", err)
		}
		safety, err := createDataSnapshot(ctx, database, "pre-migrate")
		if err != nil {
			return fmt.Errorf("pre-migration snapshot: %w", err)
		}
		if err := p.migrate(ctx, st, sg); err != nil {
			return err
		}
		_ = sg.Log(ctx, "migration.completed", "schema migration completed", map[string]string{"snapshot": safety.Filename, "database": database, "ref": op.Ref})
		p.broadcastDataEvent("migration.completed", spec.App, map[string]string{"snapshot": safety.Filename, "ref": op.Ref, "operationId": op.ID})
		return finish("schema migration completed for "+spec.App, map[string]interface{}{"snapshot": safety.Filename, "commitSha": st.commitSHA})
	default:
		return fmt.Errorf("unsupported data operation kind %s", op.Kind)
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

func createDataSnapshot(ctx context.Context, database, label string) (*dataSnapshot, error) {
	return createDataSnapshotAt(ctx, database, label, time.Now().UTC(), false)
}

func createDataSnapshotAt(ctx context.Context, database, label string, createdAt time.Time, reuse bool) (*dataSnapshot, error) {
	if !model.IsSafePostgresDatabaseName(database) {
		return nil, fmt.Errorf("unsafe postgres database name")
	}
	if err := os.MkdirAll("snapshots", 0o750); err != nil {
		return nil, fmt.Errorf("create snapshots directory: %w", err)
	}
	timestamp := createdAt.UTC().Format("20060102T150405")
	filename := fmt.Sprintf("%s_%s_%s.dump", database, label, timestamp)
	path := filepath.Join("snapshots", filename)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("snapshot target %s is not a regular file", filename)
		}
		if reuse && info.Size() > 0 {
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

	temporary, err := os.CreateTemp("snapshots", ".norn-snapshot-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("reserve snapshot file: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, fmt.Errorf("close snapshot reservation: %w", err)
	}
	defer os.Remove(temporaryPath)

	cmd := exec.CommandContext(ctx, "pg_dump", "-Fc", "-d", database, "-f", temporaryPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pg_dump: %s", strings.TrimSpace(string(output)))
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

	// Hard-linking publishes the completed dump without overwriting an existing
	// snapshot. If two safe snapshots land in the same second, advance the
	// display timestamp until an unused filename is found.
	for offset := 0; offset < 1000; offset++ {
		candidateTime := createdAt.UTC().Add(time.Duration(offset) * time.Second)
		candidateTimestamp := candidateTime.Format("20060102T150405")
		candidateFilename := fmt.Sprintf("%s_%s_%s.dump", database, label, candidateTimestamp)
		candidatePath := filepath.Join("snapshots", candidateFilename)
		if linkErr := os.Link(temporaryPath, candidatePath); linkErr == nil {
			return &dataSnapshot{Filename: candidateFilename, Timestamp: candidateTimestamp, Size: info.Size()}, nil
		} else if !errors.Is(linkErr, fs.ErrExist) {
			return nil, fmt.Errorf("publish snapshot: %w", linkErr)
		}
		if reuse && offset == 0 {
			existing, statErr := os.Lstat(candidatePath)
			if statErr == nil && existing.Mode().IsRegular() && existing.Size() > 0 {
				return &dataSnapshot{Filename: candidateFilename, Timestamp: candidateTimestamp, Size: existing.Size()}, nil
			}
		}
	}
	return nil, fmt.Errorf("could not allocate a unique snapshot filename")
}

func listDataSnapshots(database string) ([]dataSnapshot, error) {
	if !model.IsSafePostgresDatabaseName(database) {
		return nil, fmt.Errorf("unsafe postgres database name")
	}
	entries, err := os.ReadDir("snapshots")
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
		result = append(result, dataSnapshot{Filename: entry.Name(), Timestamp: timestamp, Size: info.Size()})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Timestamp == result[j].Timestamp {
			return result[i].Filename < result[j].Filename
		}
		return result[i].Timestamp > result[j].Timestamp
	})
	return result, nil
}

func findDataSnapshot(database, identifier string) (*dataSnapshot, error) {
	snapshots, err := listDataSnapshots(database)
	if err != nil {
		return nil, err
	}
	for i := range snapshots {
		if snapshots[i].Filename == identifier {
			return &snapshots[i], nil
		}
	}
	var target *dataSnapshot
	for i := range snapshots {
		if snapshots[i].Timestamp == identifier {
			if target != nil {
				return nil, fmt.Errorf("snapshot timestamp %s is ambiguous; retry with its inventory filename", identifier)
			}
			target = &snapshots[i]
		}
	}
	if target != nil {
		return target, nil
	}
	return nil, fmt.Errorf("snapshot %s was not found", identifier)
}

func pruneDataSnapshots(database string, keep int) ([]string, error) {
	snapshots, err := listDataSnapshots(database)
	if err != nil {
		return nil, err
	}
	if len(snapshots) <= keep {
		return []string{}, nil
	}
	pruned := make([]string, 0, len(snapshots)-keep)
	for _, snapshot := range snapshots[keep:] {
		if err := os.Remove(filepath.Join("snapshots", snapshot.Filename)); err != nil {
			return pruned, fmt.Errorf("prune snapshot %s: %w", snapshot.Filename, err)
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
