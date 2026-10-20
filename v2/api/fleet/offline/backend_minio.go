package offline

import (
	"context"
	"errors"
	"fmt"
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

func (r MinIOLatestBackendReader) VersionSize(ctx context.Context, bucket, key, version string) (int64, error) {
	if r.Client == nil || version == "" || version == "null" {
		return 0, ErrLatestBackendMismatch
	}
	stat, err := r.Client.StatObject(ctx, bucket, key, minio.StatObjectOptions{VersionID: version})
	if err != nil || stat.VersionID != version || stat.Size <= 0 || stat.Size > maxEffectStateBytes {
		return 0, ErrLatestBackendMismatch
	}
	return stat.Size, nil
}

// ForeignLockAbsent checks the native OpenTofu S3 lockfile. A successful HEAD
// or an ambiguous provider error fails closed; only authenticated not-found is
// accepted as absence.
func (r MinIOLatestBackendReader) ForeignLockAbsent(ctx context.Context, bucket, key string) error {
	if r.Client == nil || bucket == "" || key == "" {
		return ErrLatestBackendMismatch
	}
	_, err := r.Client.StatObject(ctx, bucket, key+".tflock", minio.StatObjectOptions{})
	if err == nil {
		return fmt.Errorf("%w: OpenTofu lock exists", ErrLatestBackendMismatch)
	}
	response := minio.ToErrorResponse(err)
	if response.StatusCode != 404 || (response.Code != "NoSuchKey" && response.Code != "NoSuchObject" && response.Code != "NotFound") {
		return ErrLatestBackendMismatch
	}
	return nil
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
