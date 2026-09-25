package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"norn/v2/api/store"
)

type snapshotExportJournal struct {
	db    *store.DB
	claim store.OperationClaim
}

func (j snapshotExportJournal) intent(bucket, key, digest string, size int64, manifest []byte) store.SnapshotExportIntent {
	sum := sha256.Sum256(manifest)
	return store.SnapshotExportIntent{OperationID: j.claim.OperationID(), Bucket: bucket, ObjectKey: key,
		DumpSHA256: digest, DumpSize: size, ManifestSHA256: hex.EncodeToString(sum[:])}
}

func (j snapshotExportJournal) prepare(ctx context.Context, intent store.SnapshotExportIntent) error {
	if j.db == nil {
		return fmt.Errorf("claimed snapshot export requires a durable journal")
	}
	return j.db.PrepareSnapshotExportIntent(ctx, j.claim, intent)
}

func (j snapshotExportJournal) complete(ctx context.Context, intent store.SnapshotExportIntent) error {
	return j.db.RecordSnapshotExportReceipt(ctx, intent)
}
