package artifactstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
)

// Run only with a dedicated object-locked provider bucket. The objects are
// intentionally retained and cannot be deleted during the compliance period.
func TestS3RealProviderQualification(t *testing.T) {
	configPath := os.Getenv("NORN_TEST_S3_CONFIG_FILE")
	if configPath == "" {
		t.Skip("NORN_TEST_S3_CONFIG_FILE is not set")
	}
	file, err := os.OpenFile(configPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal("cannot open provider configuration")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		t.Fatal("provider configuration must be an owner-only regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		t.Fatal("provider configuration must be owned by this process")
	}
	var input struct {
		Endpoint  string `json:"endpoint"`
		Bucket    string `json:"bucket"`
		Region    string `json:"region"`
		AccessKey string `json:"accessKey"`
		SecretKey string `json:"secretKey"`
		Prefix    string `json:"prefix"`
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		t.Fatal("invalid provider configuration")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatal("provider configuration has trailing JSON")
	}
	if input.Endpoint == "" || input.Bucket == "" || input.Region == "" || input.AccessKey == "" || input.SecretKey == "" ||
		!strings.HasPrefix(input.Prefix, "norn-v3-disposable/") {
		t.Fatal("provider configuration needs a dedicated norn-v3-disposable/ prefix and complete credentials")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	prefix := strings.TrimSuffix(input.Prefix, "/") + "/" + uuid.NewString()
	spool := func() string {
		directory := t.TempDir()
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		return directory
	}
	config := S3Config{Endpoint: input.Endpoint, Bucket: input.Bucket, Prefix: prefix, Region: input.Region,
		AccessKey: input.AccessKey, SecretKey: input.SecretKey, SpoolDirectory: spool(), SpoolCapacity: 32 << 20,
		RetainFor: 24 * time.Hour}
	writer, err := OpenS3(ctx, config)
	if err != nil {
		t.Fatalf("provider rejected required bucket or conditional-create capability: %v", err)
	}
	readerConfig := config
	readerConfig.SpoolDirectory = spool()
	reader, err := OpenS3(ctx, readerConfig)
	if err != nil {
		t.Fatalf("independent provider client could not open bucket: %v", err)
	}
	payload := bytes.Repeat([]byte("norn retained MySQL artifact qualification\n"), 250000) // multipart
	expected := descriptorFor(payload)
	if _, err := writer.Publish(ctx, expected, bytes.NewReader(payload)); err != nil {
		t.Fatalf("immutable multipart publication failed: %v", err)
	}
	before, err := writer.client.StatObject(ctx, writer.bucket, writer.name(expected), minio.StatObjectOptions{})
	if err != nil || before.VersionID == "" {
		t.Fatalf("provider did not expose a versioned object: %v", err)
	}
	if _, err := writer.Publish(ctx, expected, bytes.NewReader(payload)); err != nil {
		t.Fatalf("idempotent multipart publication failed: %v", err)
	}
	after, err := writer.client.StatObject(ctx, writer.bucket, writer.name(expected), minio.StatObjectOptions{})
	if err != nil || after.VersionID != before.VersionID {
		t.Fatalf("duplicate publication replaced the retained object: %v", err)
	}
	var recovered bytes.Buffer
	if err := reader.Materialize(ctx, expected, &recovered); err != nil || !bytes.Equal(recovered.Bytes(), payload) {
		t.Fatalf("independent client could not verify retained bytes: %v", err)
	}
	for _, directory := range []string{config.SpoolDirectory, readerConfig.SpoolDirectory} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() != ".norn-upload.lock" {
				t.Fatalf("provider qualification left spool entry %s in %s", entry.Name(), filepath.Base(directory))
			}
		}
	}
	t.Logf("provider qualification retained bucket=%s prefix=%s version=%s", input.Bucket, prefix, before.VersionID)
}
