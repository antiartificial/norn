package pipeline

import (
	"context"
	"fmt"
	"path/filepath"

	"norn/v2/api/model"
	"norn/v2/api/saga"
)

func (p *Pipeline) snapshot(ctx context.Context, st *state, sg *saga.Saga) error {
	if !st.spec.DeclaresDatabase() {
		return nil // skip
	}
	bound, err := p.databaseForState(ctx, st)
	if err != nil {
		return err
	}
	sha := st.commitSHA
	if len(sha) > 12 {
		sha = sha[:12]
	}
	if !snapshotCommitLabelPattern.MatchString(sha) {
		sha = "unknown"
	}
	if st.spec.NamedDatabases() {
		// The first database step of a deploy: refuse a target change of a
		// running app before any snapshot, migration, delivery or job.
		if st.deploymentID != "" {
			if err := p.guardDeployTargets(ctx, st); err != nil {
				return err
			}
		}
		// Every named database that declares snapshots gets its own
		// pre-deploy safety snapshot in its own target namespace.
		for _, requirement := range st.spec.Databases {
			if !containsCapability(requirement, "snapshot") {
				continue
			}
			target := bound.named[requirement.Name]
			if target == nil {
				return &DatabaseTargetError{Reason: fmt.Sprintf("database %q was not bound at acceptance", requirement.Name)}
			}
			if err := p.snapshotTarget(ctx, st, sg, target.resolved.Target.Database, target, sha); err != nil {
				return err
			}
		}
		return nil
	}

	db, err := postgresDatabase(st.spec)
	if err != nil {
		return err
	}
	var target *boundDatabase
	if bound != nil {
		target = bound.legacy
		if err := target.requireCapabilities(dbSnapshot); err != nil {
			return err
		}
		db = target.resolved.Target.Database
	}
	return p.snapshotTarget(ctx, st, sg, db, target, sha)
}

func (p *Pipeline) snapshotTarget(ctx context.Context, st *state, sg *saga.Saga, db string, target *boundDatabase, sha string) error {
	if target != nil {
		if err := target.requireCapabilities(dbSnapshot); err != nil {
			return err
		}
	}
	location, err := p.prepareSnapshotLocation(db, target)
	if err != nil {
		return err
	}
	created, err := createDataSnapshot(ctx, location, sha)
	if err != nil {
		return err
	}
	filename := filepath.Join(location.dir, created.Filename)
	metadata := map[string]string{
		"database":  db,
		"snapshot":  filename,
		"commitSha": st.commitSHA,
	}
	if target != nil && target.name != "" {
		metadata["logicalDatabase"] = target.name
	}
	_ = sg.Log(ctx, "snapshot.created", fmt.Sprintf("snapshot created: %s", filename), metadata)

	// Auto-export to S3 if configured
	if st.spec.Snapshots != nil && st.spec.Snapshots.ExportBucket != "" && p.Storage != nil {
		exportBucket := st.spec.Snapshots.ExportBucket
		key := snapshotExportKey(st.spec, target, filepath.Base(filename))
		if err := p.Storage.PutObject(ctx, exportBucket, key, filename); err != nil {
			_ = sg.Log(ctx, "snapshot.export_failed", fmt.Sprintf("snapshot export failed: %v", err), map[string]string{
				"bucket": exportBucket,
				"key":    key,
			})
		} else {
			_ = sg.Log(ctx, "snapshot.exported", fmt.Sprintf("snapshot exported to %s/%s", exportBucket, key), map[string]string{
				"bucket": exportBucket,
				"key":    key,
			})
		}
	}
	return nil
}

// snapshotExportKey keeps the v2 key for legacy snapshots; a named target's
// exports live under its logical database, so two targets never share keys.
func snapshotExportKey(spec *model.InfraSpec, target *boundDatabase, filename string) string {
	if target != nil && target.name != "" {
		return "snapshots/" + spec.App + "/databases/" + target.name + "/" + filename
	}
	return "snapshots/" + spec.App + "/" + filename
}

func containsCapability(requirement model.DatabaseRequirement, capability string) bool {
	for _, declared := range requirement.Capabilities {
		if declared == capability {
			return true
		}
	}
	return false
}
