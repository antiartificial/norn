package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"norn/v2/api/model"
	"norn/v2/api/saga"
)

var errPredeploySnapshotClaimLost = errors.New("predeploy snapshot claim lost")

type claimGuardedSnapshotObjects struct {
	snapshotCreateOnlyObjectStore
	check func(context.Context) error
}

func (s claimGuardedSnapshotObjects) PutObjectIfAbsent(ctx context.Context, bucket, key, path string) error {
	if err := s.check(ctx); err != nil {
		return fmt.Errorf("%w: %w", errPredeploySnapshotClaimLost, err)
	}
	return s.snapshotCreateOnlyObjectStore.PutObjectIfAbsent(ctx, bucket, key, path)
}

func (s claimGuardedSnapshotObjects) PutObject(ctx context.Context, bucket, key, path string) error {
	if err := s.check(ctx); err != nil {
		return fmt.Errorf("%w: %w", errPredeploySnapshotClaimLost, err)
	}
	return s.snapshotCreateOnlyObjectStore.PutObject(ctx, bucket, key, path)
}

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
	if st.claim.OperationID() != "" && p.DB != nil {
		if err := p.DB.CheckOperationClaim(ctx, st.claim); err != nil {
			return fmt.Errorf("predeploy snapshot claim is no longer current: %w", err)
		}
	}
	if target != nil {
		if err := target.requireCapabilities(dbSnapshot); err != nil {
			return err
		}
	}
	location, err := p.prepareSnapshotLocation(db, target)
	if err != nil {
		return err
	}
	var created *dataSnapshot
	if st.claim.OperationID() != "" {
		if st.operationStartedAt.IsZero() {
			return fmt.Errorf("predeploy snapshot operation start time is unavailable")
		}
		// The accepted operation owns one stable safety snapshot name.
		label := "effect-" + st.claim.OperationID()
		if target != nil {
			created, err = createPinnedDataSnapshotAt(ctx, location, label, st.operationStartedAt)
		} else {
			created, err = createPinnedLegacySnapshotAt(ctx, location, label, st.operationStartedAt)
		}
	} else {
		created, err = createDataSnapshot(ctx, location, sha)
	}
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

	if st.spec.Snapshots != nil && st.spec.Snapshots.ExportBucket != "" {
		objects, ok := p.SnapshotObjects.(snapshotCreateOnlyObjectStore)
		if !ok || st.claim.OperationID() == "" {
			return fmt.Errorf("predeploy snapshot export requires a claimed operation and create-only object storage")
		}
		// pg_dump may outlive a lease even when it was valid before the dump.
		// Recheck before starting the remote publication; the durable export
		// effect reservation remains a separate recovery gate.
		if p.DB == nil {
			return fmt.Errorf("predeploy snapshot export requires a claim store")
		}
		if err := p.DB.CheckOperationClaim(ctx, st.claim); err != nil {
			return fmt.Errorf("predeploy snapshot export claim is no longer current: %w", err)
		}
		objects = claimGuardedSnapshotObjects{snapshotCreateOnlyObjectStore: objects, check: func(ctx context.Context) error {
			return p.DB.CheckOperationClaim(ctx, st.claim)
		}}
		exportBucket := st.spec.Snapshots.ExportBucket
		var key string
		if target != nil {
			_, key, err = p.ExportTargetSnapshotReserved(ctx, st.spec, target.name, created.Filename, objects, exportBucket, st.claim)
		} else {
			key, err = exportLegacySnapshotClaimed(ctx, objects, exportBucket, st.spec.App, db, created.Filename, location.dir, st.claim.OperationID(), st.operationStartedAt, &snapshotExportJournal{db: p.DB, claim: st.claim})
		}
		if err != nil {
			_ = sg.Log(ctx, "snapshot.export_failed", fmt.Sprintf("snapshot export failed: %v", err), map[string]string{"bucket": exportBucket, "snapshot": created.Filename})
			return fmt.Errorf("predeploy snapshot export: %w", err)
		}
		// The remote copy can outlive the operation lease. A verified export
		// must not let a stale deploy advance to migration or job submission.
		if err := p.DB.CheckOperationClaim(ctx, st.claim); err != nil {
			return fmt.Errorf("predeploy snapshot export claim is no longer current: %w", err)
		}
		_ = sg.Log(ctx, "snapshot.exported", fmt.Sprintf("snapshot exported to %s/%s", exportBucket, key), map[string]string{
			"bucket": exportBucket, "key": key,
		})
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
