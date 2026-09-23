package archive

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
)

// Settings selects and configures an archive backend. It is shared by the
// API and the offline recovery CLI so both open archives identically.
type Settings struct {
	// Backend is "local" (the consolidated local/Mini profile) or "object"
	// (the Fleet profile). It is never inferred from partial settings.
	Backend  string
	Dir      string
	MaxBytes int64
	Endpoint string
	Bucket   string
	Prefix   string
	Region   string
	// AccessKeyFile, SecretKeyFile and CAFile are owner-only regular files.
	AccessKeyFile string
	SecretKeyFile string
	CAFile        string
	// ApplicationAccessKey is the application storage key; the archive
	// refuses to reuse it (archive credentials must be independent).
	ApplicationAccessKey string
	// Transport is for tests (emulators); production uses CAFile.
	Transport http.RoundTripper
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// OpenStore opens the configured backend. The returned closer releases
// local resources.
func OpenStore(ctx context.Context, settings Settings) (Store, io.Closer, error) {
	switch settings.Backend {
	case "local":
		if settings.Endpoint != "" || settings.Bucket != "" {
			return nil, nil, fmt.Errorf("the local archive backend does not take object storage settings")
		}
		store, err := OpenLocal(settings.Dir, settings.MaxBytes)
		if err != nil {
			return nil, nil, err
		}
		return store, store, nil
	case "object":
		if settings.Dir != "" {
			return nil, nil, fmt.Errorf("the object archive backend does not take a local directory")
		}
		accessKey, err := readPrivateSetting(settings.AccessKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("archive access key: %w", err)
		}
		secretKey, err := readPrivateSetting(settings.SecretKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("archive secret key: %w", err)
		}
		if settings.ApplicationAccessKey != "" && accessKey == settings.ApplicationAccessKey {
			return nil, nil, fmt.Errorf("the evidence archive must use credentials independent of application object storage")
		}
		transport := settings.Transport
		if settings.CAFile != "" {
			bundle, err := readPrivateSetting(settings.CAFile)
			if err != nil {
				return nil, nil, fmt.Errorf("archive CA bundle: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(bundle)) {
				return nil, nil, fmt.Errorf("archive CA bundle has no certificates")
			}
			base := http.DefaultTransport.(*http.Transport).Clone()
			base.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
			transport = base
		}
		store, err := OpenObjectStore(ctx, ObjectStoreConfig{Endpoint: settings.Endpoint, Bucket: settings.Bucket, Prefix: settings.Prefix, Region: settings.Region,
			AccessKey: accessKey, SecretKey: secretKey, Transport: transport})
		if err != nil {
			return nil, nil, err
		}
		return store, nopCloser{}, nil
	default:
		return nil, nil, fmt.Errorf("archive backend must be local or object")
	}
}

// readPrivateSetting reads a small owner-only regular file without following
// a final symlink or blocking on a FIFO.
func readPrivateSetting(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("a private file path is required")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("private file must be a regular owner-only file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("private file must be a regular owner-only file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return "", fmt.Errorf("private file must be owned by this process")
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(data) > 64<<10 {
		return "", fmt.Errorf("private file is unreadable or too large")
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("private file is empty")
	}
	return value, nil
}
