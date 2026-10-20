package etcdstore

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/sys/unix"
	"norn/v2/api/fleet/offline"
)

type offlineTerminalRuntimeConfig struct {
	AccountUUID, ProjectID, VPCUUID                            string
	RequiredTags                                               []string
	DOTokenFile                                                string
	S3Endpoint, S3Region, S3AccessKeyFile, S3SecretKeyFile     string
	BackendBucket, BackendKey                                  string
	NomadURL, ConsulURL, NomadServerName, ConsulServerName     string
	NomadTokenFile, ConsulTokenFile, NomadCAFile, ConsulCAFile string
	NomadPolicyName, NomadPolicySHA256                         string
	TerminalJournalDir                                         string
}

// newOfflineTerminalObserverFromFile is an inert, internal constructor for a
// separate management-host observer process. It is deliberately not connected
// to V3OperationStore or HTTP: target Fleet Norn remains the sole etcd writer,
// and no provider credential belongs on its control nodes. A future private
// transport must carry a signed nonce/binding/epoch/fence-bound attestation.
func newOfflineTerminalObserverFromFile(path string) (OfflineTerminalObserver, error) {
	raw, err := readOfflineEffectFile(path, 16<<10)
	if err != nil {
		return nil, offline.ErrTerminalProof
	}
	var cfg offlineTerminalRuntimeConfig
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF || !validOfflineTerminalRuntimeConfig(cfg) {
		return nil, offline.ErrTerminalProof
	}
	doToken, err := offlineEffectSecret(cfg.DOTokenFile)
	if err != nil {
		return nil, offline.ErrTerminalProof
	}
	s3Access, err := offlineEffectSecret(cfg.S3AccessKeyFile)
	if err != nil {
		return nil, offline.ErrTerminalProof
	}
	s3Secret, err := offlineEffectSecret(cfg.S3SecretKeyFile)
	if err != nil {
		return nil, offline.ErrTerminalProof
	}
	nomadToken, err := offlineEffectSecret(cfg.NomadTokenFile)
	if err != nil {
		return nil, offline.ErrTerminalProof
	}
	consulToken, err := offlineEffectSecret(cfg.ConsulTokenFile)
	if err != nil {
		return nil, offline.ErrTerminalProof
	}
	nomadRoots, err := offlineEffectRoots(cfg.NomadCAFile)
	if err != nil {
		return nil, offline.ErrTerminalProof
	}
	consulRoots, err := offlineEffectRoots(cfg.ConsulCAFile)
	if err != nil {
		return nil, offline.ErrTerminalProof
	}
	backendTransport := http.DefaultTransport.(*http.Transport).Clone()
	backendTransport.Proxy = nil
	backendTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	backend, err := minio.New(cfg.S3Endpoint, &minio.Options{Creds: credentials.NewStaticV4(s3Access, s3Secret, ""), Secure: true,
		Region: cfg.S3Region, BucketLookup: minio.BucketLookupPath, Transport: rejectRedirectTransport{backendTransport}})
	if err != nil {
		return nil, offline.ErrTerminalProof
	}
	workload := offline.NomadConsulSource{NomadURL: cfg.NomadURL, ConsulURL: cfg.ConsulURL, NomadToken: nomadToken, ConsulToken: consulToken,
		NomadServerName: cfg.NomadServerName, ConsulServerName: cfg.ConsulServerName, NomadRoots: nomadRoots, ConsulRoots: consulRoots,
		PolicyName: cfg.NomadPolicyName, PolicySHA256: cfg.NomadPolicySHA256, Now: time.Now}
	composer := offlineTerminalObservationComposer{
		provider: newOfflineDOAbsenceSource(func(context.Context) (string, error) { return doToken, nil }, cfg.ProjectID, cfg.VPCUUID, cfg.RequiredTags),
		backend: offlineBoundTerminalBackend{reader: offline.MinIOLatestBackendReader{Client: backend}, projectID: cfg.ProjectID,
			bucket: cfg.BackendBucket, key: cfg.BackendKey},
		workload: offlineBoundTerminalWorkload{source: workload}, journal: offlineTerminalFileJournal{root: cfg.TerminalJournalDir}, clock: time.Now,
	}
	return composer, nil
}

type rejectRedirectTransport struct{ next http.RoundTripper }

func (t rejectRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(req)
	if err == nil && response != nil && response.StatusCode >= 300 && response.StatusCode < 400 {
		response.Body.Close()
		return nil, errors.New("offline terminal backend redirect rejected")
	}
	return response, err
}

func validOfflineTerminalRuntimeConfig(cfg offlineTerminalRuntimeConfig) bool {
	if cfg.AccountUUID != offlineWorkAccountUUID || cfg.ProjectID == "" || cfg.VPCUUID != offline.WorkVPCUUID || len(cfg.RequiredTags) != 1 || cfg.RequiredTags[0] != "norn-work-staging" ||
		cfg.NomadPolicyName == "" || !offlineEvidenceDigest.MatchString(cfg.NomadPolicySHA256) || cfg.S3Region == "" || cfg.S3Endpoint == "" || strings.Contains(cfg.S3Endpoint, "://") || cfg.BackendBucket == "" || cfg.BackendKey == "" ||
		!strings.HasPrefix(cfg.NomadURL, "https://") || !strings.HasPrefix(cfg.ConsulURL, "https://") || cfg.NomadServerName == "" || cfg.ConsulServerName == "" ||
		cfg.DOTokenFile == "" || cfg.S3AccessKeyFile == "" || cfg.S3SecretKeyFile == "" || cfg.NomadTokenFile == "" || cfg.ConsulTokenFile == "" || cfg.NomadCAFile == "" || cfg.ConsulCAFile == "" || cfg.TerminalJournalDir == "" {
		return false
	}
	return exactHTTPSOrigin(cfg.NomadURL) && exactHTTPSOrigin(cfg.ConsulURL) && validOfflineTerminalJournalRoot(cfg.TerminalJournalDir)
}

func exactHTTPSOrigin(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func validOfflineTerminalJournalRoot(root string) bool {
	abs, err := filepath.Abs(root)
	if err != nil || abs != root {
		return false
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	return unix.Fstat(fd, &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&0077 == 0 && stat.Uid == uint32(unix.Geteuid())
}
