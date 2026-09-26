package artifactstore

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
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
	// A second client with a distinct spool must recover the exact retained
	// object from the remote bucket alone.
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

func TestS3RetainedMaterializationAcrossProcesses(t *testing.T) {
	payload := []byte("signed retained MySQL source bytes for process recovery")
	descriptor := descriptorFor(payload)
	if os.Getenv("NORN_S3_MATERIALIZE_CHILD") == "1" {
		certificate, err := os.ReadFile(os.Getenv("NORN_S3_MATERIALIZE_CA"))
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(certificate) {
			t.Fatal("emulator certificate was not trusted")
		}
		transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
		defer transport.CloseIdleConnections()
		config := S3Config{Endpoint: os.Getenv("NORN_S3_MATERIALIZE_ENDPOINT"), Bucket: "norn-artifacts",
			Prefix: "v3/private", Region: "us-east-1", AccessKey: "artifact-writer", SecretKey: "test-secret",
			Transport: transport, SpoolDirectory: os.Getenv("NORN_S3_MATERIALIZE_SPOOL"),
			SpoolCapacity: 32 << 20, RetainFor: 24 * time.Hour}
		reader, err := OpenS3(context.Background(), config)
		if err != nil {
			t.Fatal(err)
		}
		path, err := MaterializePrivate(context.Background(), reader, descriptor, os.Getenv("NORN_S3_MATERIALIZE_PRIVATE"))
		if err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, payload) {
			t.Fatalf("separate process materialization = %q, %v", actual, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private artifact mode = %v, %v", info, err)
		}
		return
	}
	_, server := s3emulator.Start("norn-artifacts", "artifact-writer")
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	writerSpool := t.TempDir()
	if err := os.Chmod(writerSpool, 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenS3(context.Background(), S3Config{Endpoint: endpoint.Host, Bucket: "norn-artifacts",
		Prefix: "v3/private", Region: "us-east-1", AccessKey: "artifact-writer", SecretKey: "test-secret",
		Transport: server.Client().Transport, SpoolDirectory: writerSpool, SpoolCapacity: 32 << 20,
		RetainFor: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Publish(context.Background(), descriptor, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	spool := t.TempDir()
	private := t.TempDir()
	for _, directory := range []string{spool, private} {
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	caPath := filepath.Join(t.TempDir(), "emulator-ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestS3RetainedMaterializationAcrossProcesses$")
	child.Env = append(os.Environ(), "NORN_S3_MATERIALIZE_CHILD=1", "NORN_S3_MATERIALIZE_CA="+caPath,
		"NORN_S3_MATERIALIZE_ENDPOINT="+endpoint.Host, "NORN_S3_MATERIALIZE_SPOOL="+spool,
		"NORN_S3_MATERIALIZE_PRIVATE="+private)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("separate process did not materialize retained object: %v\n%s", err, output)
	}
	if entries, err := os.ReadDir(spool); err != nil || len(entries) > 1 {
		t.Fatalf("reader spool retained unexpected files: %v, %v", entries, err)
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

func TestS3PublicationReconcilesLostCommitAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name string
		size int
	}{
		{name: "single-put", size: 1024},
		{name: "multipart", size: int(s3PartBytes) + 1024},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, emulator := s3TestConfig(t)
			writer, err := OpenS3(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			emulator.Configure(func(e *s3emulator.Emulator) {
				e.LoseCommitAck = true
				e.RequireConditionalComplete = true
			})
			payload := bytes.Repeat([]byte("r"), test.size)
			descriptor := descriptorFor(payload)
			if _, err := writer.Publish(context.Background(), descriptor, bytes.NewReader(payload)); err != nil {
				t.Fatalf("committed object with lost acknowledgement was not reconciled: %v", err)
			}
			var lost int
			emulator.Configure(func(e *s3emulator.Emulator) { lost = e.CommitAcksLost })
			if lost != 1 {
				t.Fatalf("lost commit acknowledgements = %d, want 1", lost)
			}
			if _, err := writer.Publish(context.Background(), descriptor, bytes.NewReader(payload)); err != nil {
				t.Fatalf("same-content retry was not idempotent: %v", err)
			}
			readerConfig := config
			readerConfig.SpoolDirectory = t.TempDir()
			if err := os.Chmod(readerConfig.SpoolDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			reader, err := OpenS3(context.Background(), readerConfig)
			if err != nil {
				t.Fatal(err)
			}
			var actual bytes.Buffer
			if err := reader.Materialize(context.Background(), descriptor, &actual); err != nil || !bytes.Equal(actual.Bytes(), payload) {
				t.Fatalf("retained object after ambiguous response = %d bytes, %v", actual.Len(), err)
			}
		})
	}
}

func TestS3PublicationRefusesUncommittedWriteFailure(t *testing.T) {
	config, emulator := s3TestConfig(t)
	writer, err := OpenS3(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	emulator.Configure(func(e *s3emulator.Emulator) { e.FailWrites = true })
	payload := []byte("uncommitted retained artifact")
	descriptor := descriptorFor(payload)
	if _, err := writer.Publish(context.Background(), descriptor, bytes.NewReader(payload)); err == nil {
		t.Fatal("uncommitted publication was accepted")
	}
	if err := writer.Verify(context.Background(), descriptor); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("failed publication left a visible object: %v", err)
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
