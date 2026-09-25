package artifactstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"norn/v2/api/internal/s3emulator"
)

func s3TestConfig(t *testing.T) (S3Config, *s3emulator.Emulator) {
	t.Helper()
	emulator, server := s3emulator.Start("norn-artifacts", "artifact-writer")
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	spool := t.TempDir()
	if err := os.Chmod(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	return S3Config{Endpoint: endpoint.Host, Bucket: "norn-artifacts", Prefix: "v3/private", Region: "us-east-1",
		AccessKey: "artifact-writer", SecretKey: "test-secret", Transport: server.Client().Transport,
		SpoolDirectory: spool, SpoolCapacity: 32 << 20, RetainFor: 24 * time.Hour}, emulator
}

func TestS3StoreConformanceAndCrossClientRead(t *testing.T) {
	config, emulator := s3TestConfig(t)
	writer, err := OpenS3(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	runStoreConformance(t, writer, "")
	// A second process has no access to the writer's spool. It must recover
	// the exact retained object from the remote bucket alone.
	readerConfig := config
	readerConfig.SpoolDirectory = t.TempDir()
	if err := os.Chmod(readerConfig.SpoolDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenS3(context.Background(), readerConfig)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("cross-node-retained-source")
	descriptor := descriptorFor(payload)
	if _, err := writer.Publish(context.Background(), descriptor, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	var restored bytes.Buffer
	if err := reader.Materialize(context.Background(), descriptor, &restored); err != nil || !bytes.Equal(restored.Bytes(), payload) {
		t.Fatalf("cross-client materialization = %q, %v", restored.Bytes(), err)
	}
	entries, err := os.ReadDir(readerConfig.SpoolDirectory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("reader spool was used: %v, %v", entries, err)
	}
	emulator.Tamper("v3/private/"+descriptor.Key, []byte("cross-node-retained-sourcX"))
	if err := reader.Verify(context.Background(), descriptor); !errors.Is(err, ErrArtifactCorrupt) {
		t.Fatalf("remote tampering = %v", err)
	}
}

func TestS3StoreRejectsMissingImmutabilityAndUnsafeSpool(t *testing.T) {
	config, emulator := s3TestConfig(t)
	for _, defect := range []struct {
		name string
		set  func(*s3emulator.Emulator)
	}{
		{"versioning", func(e *s3emulator.Emulator) { e.DisableVersioning = true }},
		{"object lock", func(e *s3emulator.Emulator) { e.DisableObjectLock = true }},
	} {
		emulator.Configure(defect.set)
		if _, err := OpenS3(context.Background(), config); err == nil {
			t.Fatalf("%s absence accepted", defect.name)
		}
		emulator.Configure(func(e *s3emulator.Emulator) { e.DisableVersioning, e.DisableObjectLock = false, false })
	}
	unsafe := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	config.SpoolDirectory = unsafe
	if _, err := OpenS3(context.Background(), config); err == nil {
		t.Fatal("public spool accepted")
	}
}

func TestS3StoreRejectsRemoteHTTP(t *testing.T) {
	config, _ := s3TestConfig(t)
	config.Insecure = true
	for _, endpoint := range []string{"example.com:9000", "localhost:9000", "10.0.0.1:9000", "[::ffff:10.0.0.1]:9000"} {
		config.Endpoint = endpoint
		if _, err := OpenS3(context.Background(), config); err == nil {
			t.Fatalf("insecure endpoint %q accepted", endpoint)
		}
	}
}

func TestS3StoreConditionalMultipartPublication(t *testing.T) {
	config, emulator := s3TestConfig(t)
	emulator.Configure(func(e *s3emulator.Emulator) { e.RequireConditionalComplete = true })
	store, err := OpenS3(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("large retained SQL artifact\n"), 400000) // above the 8 MiB part size
	expected := descriptorFor(payload)
	if _, err := store.Publish(context.Background(), expected, &sizedReader{reader: bytes.NewReader(payload), maxRead: streamBufferBytes}); err != nil {
		t.Fatalf("conditional multipart publication: %v", err)
	}
	if err := store.Verify(context.Background(), expected); err != nil {
		t.Fatalf("multipart verification: %v", err)
	}
	if _, err := store.Publish(context.Background(), expected, bytes.NewReader(payload)); err != nil {
		t.Fatalf("idempotent multipart publication: %v", err)
	}
	emulator.Configure(func(e *s3emulator.Emulator) { e.CorruptReads = true })
	if err := store.Verify(context.Background(), expected); !errors.Is(err, ErrArtifactCorrupt) {
		t.Fatalf("multipart read corruption: %v", err)
	}
}

type pausedS3Source struct {
	reader  io.Reader
	started chan struct{}
	release chan struct{}
}

func (r *pausedS3Source) Read(p []byte) (int, error) {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	<-r.release
	return r.reader.Read(p)
}

func TestS3SpoolCapacityIsSharedAcrossStoreInstances(t *testing.T) {
	config, _ := s3TestConfig(t)
	config.SpoolCapacity = 64
	first, err := OpenS3(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenS3(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("a"), 40)
	descriptor := descriptorFor(payload)
	paused := &pausedS3Source{reader: bytes.NewReader(payload), started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := first.Publish(context.Background(), descriptor, paused)
		done <- err
	}()
	select {
	case <-paused.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first publisher did not reach its spool")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := second.Publish(waitCtx, descriptor, bytes.NewReader(payload)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second publisher bypassed held spool lock: %v", err)
	}
	close(paused.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	abandoned, err := os.CreateTemp(config.SpoolDirectory, ".norn-upload-abandoned-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := abandoned.Truncate(30); err != nil {
		t.Fatal(err)
	}
	if err := abandoned.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Publish(context.Background(), descriptor, bytes.NewReader(payload)); !errors.Is(err, ErrArtifactFull) {
		t.Fatalf("abandoned spool bytes did not reduce capacity: %v", err)
	}
	if err := os.Remove(abandoned.Name()); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Publish(context.Background(), descriptor, bytes.NewReader(payload)); err != nil {
		t.Fatalf("spool did not reopen after abandoned bytes were removed: %v", err)
	}
}
