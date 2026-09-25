package artifactstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config uses a dedicated, narrowly scoped bucket credential. The bucket
// must already exist with versioning and object lock enabled. Norn never
// creates, deletes, or changes the bucket or its retention policy. A shared
// SpoolDirectory has one total SpoolCapacity across its publisher processes.
type S3Config struct {
	Endpoint       string
	Bucket         string
	Prefix         string
	Region         string
	AccessKey      string
	SecretKey      string
	Insecure       bool
	Transport      http.RoundTripper
	SpoolDirectory string
	SpoolCapacity  int64
	RetainFor      time.Duration
}

type S3Store struct {
	client *minio.Client
	bucket string
	prefix string
	spool  string
	limit  int64
	retain time.Duration
}

const s3SHA256Metadata = "Norn-Sha256"
const s3PartBytes int64 = 8 << 20

// OpenS3 refuses a bucket that cannot retain immutable versions. The
// conditional-create probe also catches S3-compatible services that ignore
// If-None-Match. This does not replace a bucket policy that denies delete and
// retention-bypass privileges to the Norn artifact credential.
func OpenS3(ctx context.Context, config S3Config) (*S3Store, error) {
	if config.Endpoint == "" || config.Bucket == "" || config.Region == "" || config.AccessKey == "" || config.SecretKey == "" ||
		config.SpoolCapacity <= 0 || config.RetainFor < 24*time.Hour || !filepath.IsAbs(config.SpoolDirectory) {
		return nil, fmt.Errorf("S3 artifact store requires bucket, credentials, private spool, capacity and retention")
	}
	if config.Insecure {
		host, _, err := net.SplitHostPort(config.Endpoint)
		address := net.ParseIP(host)
		if err != nil || address == nil || !address.IsLoopback() {
			return nil, fmt.Errorf("S3 artifact HTTP requires a numeric loopback endpoint")
		}
	}
	if err := checkPrivateDirectory(config.SpoolDirectory); err != nil {
		return nil, err
	}
	prefix := strings.Trim(config.Prefix, "/")
	if prefix != "" && (path.Clean(prefix) != prefix || strings.HasPrefix(prefix, ".") || strings.ContainsAny(prefix, "\\\x00")) {
		return nil, fmt.Errorf("S3 artifact prefix is invalid")
	}
	if prefix != "" {
		prefix += "/"
	}
	client, err := minio.New(config.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(config.AccessKey, config.SecretKey, ""), Secure: !config.Insecure,
		Region: config.Region, BucketLookup: minio.BucketLookupPath, Transport: config.Transport, MaxRetries: 3,
	})
	if err != nil {
		return nil, fmt.Errorf("S3 artifact client: %w", err)
	}
	exists, err := client.BucketExists(ctx, config.Bucket)
	if err != nil {
		return nil, fmt.Errorf("S3 artifact bucket check: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("S3 artifact bucket does not exist")
	}
	versioning, err := client.GetBucketVersioning(ctx, config.Bucket)
	if err != nil {
		return nil, fmt.Errorf("S3 artifact versioning check: %w", err)
	}
	if versioning.Status != "Enabled" {
		return nil, fmt.Errorf("S3 artifact bucket must have versioning enabled")
	}
	lock, _, _, _, err := client.GetObjectLockConfig(ctx, config.Bucket)
	if err != nil {
		return nil, fmt.Errorf("S3 artifact object lock check: %w", err)
	}
	if lock != "Enabled" {
		return nil, fmt.Errorf("S3 artifact bucket must have object lock enabled")
	}
	store := &S3Store{client: client, bucket: config.Bucket, prefix: prefix, spool: config.SpoolDirectory, limit: config.SpoolCapacity, retain: config.RetainFor}
	if err := store.probeConditionalCreate(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func checkPrivateDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("artifact spool must be an owner-only directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("artifact spool must be owned by this process")
	}
	return nil
}

func (s *S3Store) name(expected Descriptor) string { return s.prefix + expected.Key }

func (s *S3Store) putOptions(until time.Time) minio.PutObjectOptions {
	options := minio.PutObjectOptions{ContentType: "application/octet-stream", UserMetadata: map[string]string{s3SHA256Metadata: ""},
		Mode: minio.Compliance, RetainUntilDate: until, PartSize: uint64(s3PartBytes), NumThreads: 1, SendContentMd5: true}
	options.SetMatchETagExcept("*")
	return options
}

func (s *S3Store) probeConditionalCreate(ctx context.Context) error {
	first := []byte("norn artifact conditional-create probe v1")
	second := []byte("norn artifact conditional-create probe v2")
	name := s.prefix + "norn-artifact-probe/conditional-create"
	options := s.putOptions(time.Now().Add(s.retain))
	_, firstErr := s.client.PutObject(ctx, s.bucket, name, strings.NewReader(string(first)), int64(len(first)), options)
	if firstErr != nil && !isS3PreconditionFailed(firstErr) {
		return fmt.Errorf("S3 artifact probe initial publication: %w", firstErr)
	}
	before, err := s.client.GetObject(ctx, s.bucket, name, minio.GetObjectOptions{})
	if err != nil {
		return err
	}
	previous, readErr := io.ReadAll(io.LimitReader(before, 1024))
	closeErr := before.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("S3 artifact probe read: %v %v", readErr, closeErr)
	}
	_, replaceErr := s.client.PutObject(ctx, s.bucket, name, strings.NewReader(string(second)), int64(len(second)), options)
	if replaceErr == nil {
		return fmt.Errorf("S3 artifact store replaced an existing object")
	}
	after, err := s.client.GetObject(ctx, s.bucket, name, minio.GetObjectOptions{})
	if err != nil {
		return err
	}
	current, readErr := io.ReadAll(io.LimitReader(after, 1024))
	closeErr = after.Close()
	if readErr != nil || closeErr != nil || string(current) != string(previous) {
		return fmt.Errorf("S3 artifact conditional-create probe changed an existing object")
	}
	return nil
}

func isS3PreconditionFailed(err error) bool {
	response := minio.ToErrorResponse(err)
	return err != nil && (response.StatusCode == http.StatusPreconditionFailed || response.Code == "PreconditionFailed")
}

func s3ArtifactError(err error) error {
	if err == nil {
		return nil
	}
	response := minio.ToErrorResponse(err)
	switch {
	case response.StatusCode == http.StatusNotFound || response.Code == "NoSuchKey":
		return ErrArtifactNotFound
	case response.StatusCode == http.StatusInsufficientStorage || response.Code == "XMinioStorageFull" || response.Code == "QuotaExceeded":
		return errors.Join(ErrArtifactFull, err)
	}
	return err
}

// Publish verifies the source into a bounded private spool before the remote
// upload. A bad source can therefore never create a content-keyed object.
func (s *S3Store) Publish(ctx context.Context, expected Descriptor, source io.Reader) (Descriptor, error) {
	if err := expected.Validate(); err != nil {
		return Descriptor{}, err
	}
	if expected.Size > s.limit {
		return Descriptor{}, ErrArtifactFull
	}
	if err := ctx.Err(); err != nil {
		return Descriptor{}, err
	}
	unlock, err := s.lockSpool(ctx, expected.Size)
	if err != nil {
		return Descriptor{}, err
	}
	defer unlock()
	file, err := os.CreateTemp(s.spool, ".norn-upload-*")
	if err != nil {
		return Descriptor{}, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := copyVerified(ctx, file, source, expected); err != nil {
		return Descriptor{}, err
	}
	if err := file.Sync(); err != nil {
		return Descriptor{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Descriptor{}, err
	}
	minimumUntil := time.Now().Add(s.retain)
	options := s.putOptions(minimumUntil)
	options.UserMetadata[s3SHA256Metadata] = expected.SHA256
	var putErr error
	if expected.Size > s3PartBytes {
		putErr = s.publishMultipart(ctx, expected, file, options)
	} else {
		_, putErr = s.client.PutObject(ctx, s.bucket, s.name(expected), file, expected.Size, options)
	}
	if putErr != nil && !isS3PreconditionFailed(putErr) {
		if verifyErr := s.Verify(ctx, expected); verifyErr != nil {
			return Descriptor{}, errors.Join(s3ArtifactError(putErr), verifyErr)
		}
	} else if err := s.Verify(ctx, expected); err != nil {
		return Descriptor{}, err
	}
	mode, until, err := s.client.GetObjectRetention(ctx, s.bucket, s.name(expected), "")
	if err != nil || mode == nil || *mode != minio.Compliance || until == nil || until.Before(minimumUntil.Add(-time.Minute)) {
		return Descriptor{}, errors.Join(ErrArtifactUnverified, err)
	}
	return expected, nil
}

// A spool is shared by every store process using its directory. Hold the
// filesystem lock through remote verification so simultaneous publishers
// cannot each consume the full configured capacity. Abandoned upload files
// retain their capacity charge until an operator inspects and removes them.
func (s *S3Store) lockSpool(ctx context.Context, size int64) (func(), error) {
	lock, err := os.OpenFile(filepath.Join(s.spool, ".norn-upload.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = lock.Close()
			return nil, err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = lock.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = lock.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	unlock := func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}
	entries, err := os.ReadDir(s.spool)
	if err != nil {
		unlock()
		return nil, err
	}
	var used int64
	for _, entry := range entries {
		if entry.Name() == ".norn-upload.lock" {
			continue
		}
		if !strings.HasPrefix(entry.Name(), ".norn-upload-") || !entry.Type().IsRegular() {
			unlock()
			return nil, fmt.Errorf("S3 artifact spool contains an unexpected entry: %s", entry.Name())
		}
		info, err := entry.Info()
		if err != nil || info.Size() < 0 || info.Size() > s.limit-used {
			unlock()
			return nil, ErrArtifactFull
		}
		used += info.Size()
	}
	if used > s.limit-size {
		unlock()
		return nil, ErrArtifactFull
	}
	return unlock, nil
}

// The pinned MinIO high-level PutObject implementation drops custom headers
// before CompleteMultipartUpload. Use Core so If-None-Match reaches the final
// commit, which is the only point where a multipart upload can replace a key.
func (s *S3Store) publishMultipart(ctx context.Context, expected Descriptor, file *os.File, options minio.PutObjectOptions) (runErr error) {
	core := minio.Core{Client: s.client}
	name := s.name(expected)
	uploadID, err := core.NewMultipartUpload(ctx, s.bucket, name, options)
	if err != nil {
		return fmt.Errorf("start multipart artifact publication: %w", err)
	}
	defer func() {
		if runErr != nil {
			abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = core.AbortMultipartUpload(abortCtx, s.bucket, name, uploadID)
		}
	}()
	parts := make([]minio.CompletePart, 0, (expected.Size+s3PartBytes-1)/s3PartBytes)
	for offset, number := int64(0), 1; offset < expected.Size; offset, number = offset+s3PartBytes, number+1 {
		if err := ctx.Err(); err != nil {
			return err
		}
		length := min(s3PartBytes, expected.Size-offset)
		part, err := core.PutObjectPart(ctx, s.bucket, name, uploadID, number, io.NewSectionReader(file, offset, length), length, minio.PutObjectPartOptions{})
		if err != nil {
			return fmt.Errorf("upload multipart artifact part %d: %w", number, err)
		}
		parts = append(parts, minio.CompletePart{ETag: part.ETag, PartNumber: number})
	}
	// Retention, metadata and content type belong to the initiation request.
	// Repeating them at completion is rejected by real S3 implementations;
	// keep only the no-replace precondition on the commit request.
	completeOptions := minio.PutObjectOptions{}
	completeOptions.SetMatchETagExcept("*")
	_, err = core.CompleteMultipartUpload(ctx, s.bucket, name, uploadID, parts, completeOptions)
	if err != nil {
		return fmt.Errorf("complete multipart artifact publication: %w", err)
	}
	return nil
}

func (s *S3Store) Open(ctx context.Context, expected Descriptor) (io.ReadCloser, error) {
	if err := expected.Validate(); err != nil {
		return nil, err
	}
	stat, err := s.client.StatObject(ctx, s.bucket, s.name(expected), minio.StatObjectOptions{})
	if err != nil {
		return nil, s3ArtifactError(err)
	}
	if stat.Size != expected.Size || stat.Size > MaxArtifactBytes {
		return nil, ErrArtifactCorrupt
	}
	for key, value := range stat.UserMetadata {
		if strings.EqualFold(key, s3SHA256Metadata) && value != expected.SHA256 {
			return nil, ErrArtifactCorrupt
		}
	}
	mode, until, err := s.client.GetObjectRetention(ctx, s.bucket, s.name(expected), stat.VersionID)
	if err != nil || mode == nil || *mode != minio.Compliance || until == nil || !until.After(time.Now()) {
		return nil, errors.Join(ErrArtifactUnverified, err)
	}
	options := minio.GetObjectOptions{}
	if err := options.SetMatchETag(stat.ETag); err != nil {
		return nil, err
	}
	if stat.VersionID != "" {
		options.VersionID = stat.VersionID
	}
	object, err := s.client.GetObject(ctx, s.bucket, s.name(expected), options)
	if err != nil {
		return nil, s3ArtifactError(err)
	}
	return &verifyingReadCloser{reader: contextReader{ctx: ctx, reader: object}, closer: object, expected: expected, hash: sha256.New()}, nil
}

func (s *S3Store) Materialize(ctx context.Context, expected Descriptor, destination io.Writer) error {
	reader, err := s.Open(ctx, expected)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyBuffer(destination, reader, make([]byte, streamBufferBytes))
	closeErr := reader.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (s *S3Store) Verify(ctx context.Context, expected Descriptor) error {
	return s.Materialize(ctx, expected, io.Discard)
}

var _ Store = (*S3Store)(nil)
