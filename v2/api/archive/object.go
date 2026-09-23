package archive

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ObjectStoreConfig configures the Fleet adapter: an S3-compatible bucket
// reached with credentials dedicated to the evidence archive (never the
// application provisioning or Garage administration keys).
type ObjectStoreConfig struct {
	Endpoint  string // host[:port]
	Bucket    string
	Prefix    string // optional key namespace within the bucket, e.g. "norn/prod"
	Region    string
	AccessKey string
	SecretKey string
	// Insecure permits plain HTTP (local emulators only).
	Insecure bool
	// Transport overrides the HTTP transport (custom CA bundles, tests).
	Transport http.RoundTripper
}

// ObjectStore is the Fleet archive adapter. Publication is a conditional
// create (If-None-Match: *) with a Content-MD5 integrity check and the
// object's SHA-256 recorded in its metadata; an existing object is verified
// byte-for-byte as a duplicate or refused as a conflict. Reads are bounded
// (stat first, then a size-limited read bound to the stat'ed ETag) and
// hashed. OpenObjectStore refuses a store that does not enforce conditional
// creation, since it could silently replace published evidence. The adapter
// never deletes or overwrites objects.
type ObjectStore struct {
	client *minio.Client
	bucket string
	prefix string
}

const (
	shaMetadata    = "Norn-Sha256"
	probeObjectKey = "norn-archive-probe/conditional-create"
)

// OpenObjectStore connects, requires the bucket, and proves the store
// enforces conditional creation.
func OpenObjectStore(ctx context.Context, config ObjectStoreConfig) (*ObjectStore, error) {
	if config.Endpoint == "" || config.Bucket == "" || config.Region == "" || config.AccessKey == "" || config.SecretKey == "" {
		return nil, fmt.Errorf("object archive needs an endpoint, bucket, region and dedicated credentials")
	}
	prefix := strings.Trim(config.Prefix, "/")
	if prefix != "" && !ValidKey(prefix) {
		return nil, fmt.Errorf("object archive prefix is not a clean key prefix")
	}
	if prefix != "" {
		prefix += "/"
	}
	client, err := minio.New(config.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(config.AccessKey, config.SecretKey, ""), Secure: !config.Insecure,
		Region: config.Region, BucketLookup: minio.BucketLookupPath, Transport: config.Transport,
		// Bounded retries: an outage keeps evidence pending for the next
		// archive pass rather than stalling this one.
		MaxRetries: 3,
	})
	if err != nil {
		return nil, fmt.Errorf("object archive client: %w", err)
	}
	store := &ObjectStore{client: client, bucket: config.Bucket, prefix: prefix}
	exists, err := client.BucketExists(ctx, config.Bucket)
	if err != nil {
		return nil, fmt.Errorf("object archive bucket check: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("object archive bucket %s does not exist; it is provisioned outside Norn", config.Bucket)
	}
	if err := store.probeConditionalCreate(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// probeConditionalCreate writes a probe object once (or finds it) and then
// attempts a conditional create of different bytes, which must be refused.
func (s *ObjectStore) probeConditionalCreate(ctx context.Context) error {
	for attempt := 0; attempt < 2; attempt++ {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		err := s.conditionalPut(ctx, s.prefix+probeObjectKey, []byte(hex.EncodeToString(nonce)))
		switch {
		case isPreconditionFailed(err):
			return nil
		case err != nil:
			return fmt.Errorf("object archive conditional-create probe: %w", err)
		}
		// First attempt may simply have created the probe object; the
		// second must be refused.
	}
	return fmt.Errorf("object archive store replaced an existing object despite If-None-Match: immutable publication cannot be guaranteed")
}

func (s *ObjectStore) conditionalPut(ctx context.Context, name string, data []byte) error {
	sum := sha256.Sum256(data)
	options := minio.PutObjectOptions{ContentType: "application/octet-stream", SendContentMd5: true, DisableMultipart: true,
		UserMetadata: map[string]string{shaMetadata: hex.EncodeToString(sum[:])}}
	options.SetMatchETagExcept("*")
	_, err := s.client.PutObject(ctx, s.bucket, name, bytes.NewReader(data), int64(len(data)), options)
	return err
}

func isPreconditionFailed(err error) bool {
	response := minio.ToErrorResponse(err)
	return err != nil && (response.StatusCode == http.StatusPreconditionFailed || response.Code == "PreconditionFailed")
}

func translateObjectError(err error) error {
	if err == nil {
		return nil
	}
	response := minio.ToErrorResponse(err)
	switch {
	case response.StatusCode == http.StatusNotFound || response.Code == "NoSuchKey":
		return ErrObjectNotFound
	case response.StatusCode == http.StatusInsufficientStorage || response.Code == "XMinioStorageFull" || response.Code == "QuotaExceeded":
		return fmt.Errorf("%w: %s", ErrArchiveFull, response.Code)
	}
	return err
}

func (s *ObjectStore) PutImmutable(ctx context.Context, key string, data []byte) (ObjectInfo, error) {
	if !ValidKey(key) {
		return ObjectInfo{}, fmt.Errorf("invalid archive key")
	}
	sum := sha256.Sum256(data)
	info := ObjectInfo{Key: key, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	err := s.conditionalPut(ctx, s.prefix+key, data)
	if isPreconditionFailed(err) {
		existing, _, getErr := s.read(ctx, key, info.Size)
		if errors.Is(getErr, ErrObjectTooLarge) {
			return ObjectInfo{}, ErrImmutableConflict
		}
		if getErr != nil {
			return ObjectInfo{}, getErr
		}
		return info, verifyDuplicate(existing, info)
	}
	if err != nil {
		return ObjectInfo{}, translateObjectError(err)
	}
	return info, nil
}

// read stats the object, refuses it above maxBytes, then reads at most
// maxBytes+1 bytes of exactly the stat'ed version (If-Match on its ETag) and
// hashes them. A recorded SHA-256 metadata value that differs from the bytes
// read is corruption.
func (s *ObjectStore) read(ctx context.Context, key string, maxBytes int64) (ObjectInfo, []byte, error) {
	name := s.prefix + key
	stat, err := s.client.StatObject(ctx, s.bucket, name, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, nil, translateObjectError(err)
	}
	if stat.Size > maxBytes {
		return ObjectInfo{}, nil, ErrObjectTooLarge
	}
	options := minio.GetObjectOptions{}
	if err := options.SetMatchETag(stat.ETag); err != nil {
		return ObjectInfo{}, nil, err
	}
	object, err := s.client.GetObject(ctx, s.bucket, name, options)
	if err != nil {
		return ObjectInfo{}, nil, translateObjectError(err)
	}
	defer object.Close()
	data, err := io.ReadAll(io.LimitReader(object, maxBytes+1))
	if err != nil {
		return ObjectInfo{}, nil, translateObjectError(err)
	}
	if int64(len(data)) > maxBytes {
		return ObjectInfo{}, nil, ErrObjectTooLarge
	}
	if int64(len(data)) != stat.Size {
		return ObjectInfo{}, nil, ErrObjectCorrupt
	}
	sum := sha256.Sum256(data)
	info := ObjectInfo{Key: key, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	if recorded := stat.UserMetadata[shaMetadata]; recorded != "" && recorded != info.SHA256 {
		return ObjectInfo{}, nil, ErrObjectCorrupt
	}
	return info, data, nil
}

func (s *ObjectStore) Get(ctx context.Context, key string, maxBytes int64) ([]byte, ObjectInfo, error) {
	if !ValidKey(key) {
		return nil, ObjectInfo{}, fmt.Errorf("invalid archive key")
	}
	info, data, err := s.read(ctx, key, maxBytes)
	return data, info, err
}

func (s *ObjectStore) Verify(ctx context.Context, expected ObjectInfo) error {
	if !ValidKey(expected.Key) {
		return fmt.Errorf("invalid archive key")
	}
	actual, _, err := s.read(ctx, expected.Key, expected.Size)
	if errors.Is(err, ErrObjectTooLarge) {
		return ErrObjectCorrupt
	}
	if err != nil {
		return err
	}
	if actual.SHA256 != expected.SHA256 || actual.Size != expected.Size {
		return ErrObjectCorrupt
	}
	return nil
}

func (s *ObjectStore) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.prefix + prefix, Recursive: true}) {
		if object.Err != nil {
			return nil, translateObjectError(object.Err)
		}
		key := strings.TrimPrefix(object.Key, s.prefix)
		if strings.HasPrefix(key, "norn-archive-probe/") || !ValidKey(key) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}
