package pipeline

import (
	"context"
	"fmt"
	"path/filepath"

	"norn/v2/api/saga"
)

func (p *Pipeline) snapshot(ctx context.Context, st *state, sg *saga.Saga) error {
	if st.spec.Infrastructure == nil || st.spec.Infrastructure.Postgres == nil {
		return nil // skip
	}

	db, err := postgresDatabase(st.spec)
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
	created, err := createDataSnapshot(ctx, db, sha)
	if err != nil {
		return err
	}
	filename := filepath.Join("snapshots", created.Filename)
	_ = sg.Log(ctx, "snapshot.created", fmt.Sprintf("snapshot created: %s", filename), map[string]string{
		"database":  db,
		"snapshot":  filename,
		"commitSha": st.commitSHA,
	})

	// Auto-export to S3 if configured
	if st.spec.Snapshots != nil && st.spec.Snapshots.ExportBucket != "" && p.Storage != nil {
		exportBucket := st.spec.Snapshots.ExportBucket
		key := "snapshots/" + st.spec.App + "/" + filepath.Base(filename)
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
