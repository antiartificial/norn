package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"norn/v2/api/saga"
)

func (p *Pipeline) snapshot(ctx context.Context, st *state, sg *saga.Saga) error {
	if st.spec.Infrastructure == nil || st.spec.Infrastructure.Postgres == nil {
		return nil // skip
	}

	db := st.spec.Infrastructure.Postgres.Database
	sha := st.commitSHA
	if len(sha) > 12 {
		sha = sha[:12]
	}
	filename := fmt.Sprintf("snapshots/%s_%s_%s.dump", db, sha, time.Now().Format("20060102T150405"))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return fmt.Errorf("create snapshots dir: %w", err)
	}
	cmd := exec.CommandContext(ctx, "pg_dump", "-Fc", "-d", db, "-f", filename)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_dump: %s", string(out))
	}
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
