package storage

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"norn/v2/api/internal/s3emulator"
	"norn/v2/api/model"
)

func TestPutObjectIfAbsentAcrossSingleAndMultipart(t *testing.T) {
	emulator, server := s3emulator.Start("snapshot-review", "writer")
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := minio.New(endpoint.Host, &minio.Options{Creds: credentials.NewStaticV4("writer", "test-secret", ""), Secure: true,
		Region: "us-east-1", BucketLookup: minio.BucketLookupPath, Transport: server.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{mc: mc}
	emulator.Configure(func(e *s3emulator.Emulator) { e.RequireConditionalComplete = true })
	for _, size := range []int{16, 9 << 20} {
		path := filepath.Join(t.TempDir(), "snapshot.dump")
		payload := bytes.Repeat([]byte("s"), size)
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("snapshots/demo/%d.dump", size)
		if err := client.PutObjectIfAbsent(context.Background(), "snapshot-review", key, path); err != nil {
			t.Fatalf("first publication of %d bytes: %v", size, err)
		}
		if err := client.PutObjectIfAbsent(context.Background(), "snapshot-review", key, path); err == nil {
			t.Fatalf("duplicate publication of %d bytes replaced the object", size)
		}
		download := filepath.Join(t.TempDir(), "download.dump")
		if err := client.GetObject(context.Background(), "snapshot-review", key, download); err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(download)
		if err != nil || !bytes.Equal(actual, payload) {
			t.Fatalf("published %d-byte object differs: %v", size, err)
		}
	}
}

func TestApplyBucketEnvSetsDefaultAndNamedBuckets(t *testing.T) {
	env := map[string]string{}

	applyBucketEnv(env, model.ObjectStorageBucket{
		Name:   "omniphore-media",
		Prefix: "prod/",
		Env:    "MEDIA",
	}, 0)
	applyBucketEnv(env, model.ObjectStorageBucket{
		Name: "omniphore-snapshots",
	}, 1)

	assertEnv(t, env, "S3_BUCKET", "omniphore-media")
	assertEnv(t, env, "S3_PREFIX", "prod/")
	assertEnv(t, env, "S3_BUCKET_MEDIA", "omniphore-media")
	assertEnv(t, env, "S3_PREFIX_MEDIA", "prod/")
	assertEnv(t, env, "S3_BUCKET_OMNIPHORE_SNAPSHOTS", "omniphore-snapshots")
}

func assertEnv(t *testing.T, env map[string]string, key, want string) {
	t.Helper()
	if got := env[key]; got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}
