package offline

import (
	"context"
	"errors"
	"io"

	"github.com/minio/minio-go/v7"
)

// MinIOLatestBackendReader independently reads the S3-compatible state
// bucket. Its client must have GetObject/HeadObject only; no write capability
// is needed by this observer.
type MinIOLatestBackendReader struct{ Client *minio.Client }

func (r MinIOLatestBackendReader) VersioningEnabled(ctx context.Context, bucket, key string) (bool, error) {
	if r.Client == nil || bucket == "" || key == "" {
		return false, ErrLatestBackendMismatch
	}
	config, err := r.Client.GetBucketVersioning(ctx, bucket)
	if err != nil || !config.Enabled() || config.ExcludeFolders || len(config.ExcludedPrefixes) != 0 {
		return false, ErrLatestBackendMismatch
	}
	return true, nil
}

func (r MinIOLatestBackendReader) Latest(ctx context.Context, bucket, key string) (string, int64, error) {
	if r.Client == nil {
		return "", 0, ErrLatestBackendMismatch
	}
	stat, err := r.Client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil || stat.VersionID == "" || stat.VersionID == "null" {
		return "", 0, ErrLatestBackendMismatch
	}
	return stat.VersionID, stat.Size, nil
}

func (r MinIOLatestBackendReader) Version(ctx context.Context, bucket, key, version string, size int64) ([]byte, error) {
	if r.Client == nil || version == "" || version == "null" || size <= 0 || size > maxEffectStateBytes {
		return nil, ErrLatestBackendMismatch
	}
	options := minio.GetObjectOptions{VersionID: version}
	stat, err := r.Client.StatObject(ctx, bucket, key, options)
	if err != nil || stat.VersionID != version || stat.Size != size {
		return nil, ErrLatestBackendMismatch
	}
	if err := options.SetMatchETag(stat.ETag); err != nil {
		return nil, ErrLatestBackendMismatch
	}
	object, err := r.Client.GetObject(ctx, bucket, key, options)
	if err != nil {
		return nil, ErrLatestBackendMismatch
	}
	defer object.Close()
	raw, err := io.ReadAll(io.LimitReader(object, size+1))
	if err != nil || int64(len(raw)) != size {
		return nil, errors.Join(ErrLatestBackendMismatch, err)
	}
	return raw, nil
}
