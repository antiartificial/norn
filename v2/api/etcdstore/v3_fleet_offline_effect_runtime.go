package etcdstore

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"norn/v2/api/fleet/offline"
)

// OfflineEffectRuntimeConfig is an authority-owned staging manifest. Credential
// values live only in owner-only files, never in runner requests or receipts.
type OfflineEffectRuntimeConfig struct {
	AccountUUID, ProjectID, VPCUUID                            string
	RequiredTags                                               []string
	DOTokenFile                                                string
	S3Endpoint, S3Region, S3AccessKeyFile, S3SecretKeyFile     string
	NomadURL, ConsulURL, NomadServerName, ConsulServerName     string
	NomadTokenFile, ConsulTokenFile, NomadCAFile, ConsulCAFile string
	NomadPolicyName, NomadPolicySHA256                         string
}

func readOfflineEffectFile(path string, max int64) ([]byte, error) {
	if !filepath.IsAbs(path) || max < 1 {
		return nil, offline.ErrEffectProofMissing
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, offline.ErrEffectProofMissing
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > max || info.Mode().Perm()&0077 != 0 {
		return nil, offline.ErrEffectProofMissing
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, offline.ErrEffectProofMissing
	}
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > max {
		return nil, offline.ErrEffectProofMissing
	}
	return raw, nil
}

func offlineEffectSecret(path string) (string, error) {
	raw, err := readOfflineEffectFile(path, 4096)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", offline.ErrEffectProofMissing
	}
	return value, nil
}

func offlineEffectRoots(path string) (*x509.CertPool, error) {
	raw, err := readOfflineEffectFile(path, 64<<10)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, offline.ErrEffectProofMissing
	}
	return roots, nil
}

// NewOfflineEffectAuthorityFromFile composes the reviewed observer only when
// every independent read credential and fixed work-staging topology is pinned.
// It makes no provider mutation and performs no observation until Issue.
func NewOfflineEffectAuthorityFromFile(store *V3OperationStore, path string, origin interface {
	VerifyOfflineEffectSource(context.Context, string) error
}) (*OfflineEffectAuthority, error) {
	if store == nil || origin == nil {
		return nil, offline.ErrEffectProofMissing
	}
	raw, err := readOfflineEffectFile(path, 16<<10)
	if err != nil {
		return nil, err
	}
	var cfg OfflineEffectRuntimeConfig
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, offline.ErrEffectProofMissing
	}
	if cfg.AccountUUID != offlineWorkAccountUUID || cfg.ProjectID == "" || cfg.VPCUUID == "" || len(cfg.RequiredTags) == 0 ||
		cfg.NomadPolicyName == "" || !offlineEvidenceDigest.MatchString(cfg.NomadPolicySHA256) ||
		!strings.HasPrefix(cfg.NomadURL, "https://") || !strings.HasPrefix(cfg.ConsulURL, "https://") ||
		cfg.NomadServerName == "" || cfg.ConsulServerName == "" || cfg.S3Region == "" || cfg.S3Endpoint == "" ||
		strings.Contains(cfg.S3Endpoint, "://") {
		return nil, offline.ErrEffectProofMissing
	}
	doToken, err := offlineEffectSecret(cfg.DOTokenFile)
	if err != nil {
		return nil, err
	}
	s3Access, err := offlineEffectSecret(cfg.S3AccessKeyFile)
	if err != nil {
		return nil, err
	}
	s3Secret, err := offlineEffectSecret(cfg.S3SecretKeyFile)
	if err != nil {
		return nil, err
	}
	nomadToken, err := offlineEffectSecret(cfg.NomadTokenFile)
	if err != nil {
		return nil, err
	}
	consulToken, err := offlineEffectSecret(cfg.ConsulTokenFile)
	if err != nil {
		return nil, err
	}
	nomadRoots, err := offlineEffectRoots(cfg.NomadCAFile)
	if err != nil {
		return nil, err
	}
	consulRoots, err := offlineEffectRoots(cfg.ConsulCAFile)
	if err != nil {
		return nil, err
	}
	backend, err := minio.New(cfg.S3Endpoint, &minio.Options{Creds: credentials.NewStaticV4(s3Access, s3Secret, ""), Secure: true, Region: cfg.S3Region, BucketLookup: minio.BucketLookupPath})
	if err != nil {
		return nil, fmt.Errorf("offline effect backend: %w", errors.Join(offline.ErrEffectProofMissing, err))
	}
	return NewOfflineEffectAuthority(store, OfflineEffectReaders{
		Origin:   origin,
		Provider: offlineDOReader{client: &http.Client{Timeout: 20 * time.Second}, token: func(context.Context) (string, error) { return doToken, nil }, projectID: cfg.ProjectID},
		Backend:  offline.MinIOLatestBackendReader{Client: backend},
		Workload: offline.NomadConsulSource{NomadURL: cfg.NomadURL, ConsulURL: cfg.ConsulURL, NomadToken: nomadToken, ConsulToken: consulToken, NomadServerName: cfg.NomadServerName, ConsulServerName: cfg.ConsulServerName, NomadRoots: nomadRoots, ConsulRoots: consulRoots, PolicyName: cfg.NomadPolicyName, PolicySHA256: cfg.NomadPolicySHA256, Now: time.Now},
		Epoch:    offlineEffectStoreEpoch{store},
		Topology: OfflineProviderTopology{AccountUUID: cfg.AccountUUID, ProjectID: cfg.ProjectID, VPCUUID: cfg.VPCUUID, RequiredTags: cfg.RequiredTags}, Clock: time.Now,
	})
}

type offlineEffectStoreEpoch struct{ *V3OperationStore }

func (r offlineEffectStoreEpoch) ReadOfflineEffectEpoch(ctx context.Context) (int64, error) {
	epoch, _, err := r.fleetAuthorityEpochWithRevision(ctx)
	return epoch, err
}
