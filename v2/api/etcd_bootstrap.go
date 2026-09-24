package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/startup"
	"norn/v2/api/store"
)

const (
	etcdManagedCredentialBootstrapArgument = "--norn-etcd-bootstrap"
	etcdBootstrapRecordVersion             = 3
	minimumBootstrapTTL                    = time.Hour
	minimumBootstrapRemaining              = 30 * time.Minute
)

type etcdBootstrapRecord struct {
	Version        int               `json:"version"`
	State          string            `json:"state"`
	Token          store.AccessToken `json:"token"`
	TTLNanoseconds int64             `json:"ttlNanoseconds"`
	OutputPathHash string            `json:"outputPathSHA256"`
	SigningKeyHash string            `json:"signingKeySHA256"`
}

const etcdBootstrapAccepted = "accepted"

type etcdBootstrapRequest struct {
	Output  string
	Subject string
	Scopes  []string
	TTL     time.Duration
}

// runEtcdManagedCredentialBootstrap accepts the initial managed credential
// atomically, then writes its opaque bearer to an exclusive owner-only file.
// The versioned record has no bearer material and makes publication retryable.
func runEtcdManagedCredentialBootstrap(args []string, cfg *config.Config, backend startup.ControlBackendConfig) (bool, error) {
	if len(args) != 1 || args[0] != etcdManagedCredentialBootstrapArgument {
		return false, nil
	}
	if cfg == nil || backend.Backend != startup.BackendEtcd || backend.SourceValidation {
		return true, fmt.Errorf("%s requires normal NORN_CONTROL_BACKEND=etcd", etcdManagedCredentialBootstrapArgument)
	}
	req, err := parseEtcdBootstrapRequest(os.Getenv)
	if err != nil {
		return true, err
	}
	if !cfg.RequireExplicitAuth || len(cfg.APIToken) < 32 {
		return true, fmt.Errorf("bootstrap requires NORN_REQUIRE_EXPLICIT_AUTH=true and a 32-byte NORN_API_TOKEN")
	}
	client, err := newEtcdClient(backend)
	if err != nil {
		return true, err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := bootstrapEtcdManagedCredential(ctx, client, backend.EtcdPrefix, cfg.APIToken, req, publishBootstrapTokenFile); err != nil {
		return true, err
	}
	return true, nil
}

func parseEtcdBootstrapRequest(getenv func(string) string) (etcdBootstrapRequest, error) {
	req := etcdBootstrapRequest{Output: strings.TrimSpace(getenv(startup.EtcdBootstrapTokenFileEnv)), Subject: strings.TrimSpace(getenv(startup.EtcdBootstrapSubjectEnv)), Scopes: splitBootstrapScopes(getenv(startup.EtcdBootstrapScopesEnv))}
	var err error
	req.TTL, err = time.ParseDuration(strings.TrimSpace(getenv(startup.EtcdBootstrapTTLEnv)))
	if req.Output == "" || !filepath.IsAbs(req.Output) {
		return etcdBootstrapRequest{}, fmt.Errorf("%s must be an absolute, new token file path", startup.EtcdBootstrapTokenFileEnv)
	}
	if req.Subject == "" || len(req.Scopes) == 0 || err != nil || req.TTL < minimumBootstrapTTL || req.TTL > 72*time.Hour {
		return etcdBootstrapRequest{}, fmt.Errorf("%s, %s, and a 1h..72h %s are required", startup.EtcdBootstrapSubjectEnv, startup.EtcdBootstrapScopesEnv, startup.EtcdBootstrapTTLEnv)
	}
	return req, nil
}

func bootstrapEtcdManagedCredential(ctx context.Context, kv clientv3.KV, prefix, secret string, req etcdBootstrapRequest, publish func(string, string) error) error {
	record, err := acceptInitialEtcdManagedCredential(ctx, kv, prefix, secret, req)
	if err != nil {
		return err
	}
	if err := requireUsableInitialEtcdCredential(record, time.Now()); err != nil {
		return err
	}
	token, err := handler.SignManagedAccessToken(secret, &record.Token)
	if err != nil {
		return fmt.Errorf("recreate accepted bootstrap credential: %w", err)
	}
	if err := publish(req.Output, token); err != nil {
		return fmt.Errorf("publish bootstrap token: %w", err)
	}
	return nil
}

func acceptInitialEtcdManagedCredential(ctx context.Context, kv clientv3.KV, prefix, secret string, req etcdBootstrapRequest) (*etcdBootstrapRecord, error) {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		return nil, errors.New("etcd bootstrap prefix is required")
	}
	markerKey := etcdBootstrapMarkerKey(prefix)
	marker, err := loadEtcdBootstrapRecord(ctx, kv, markerKey)
	if err != nil {
		return nil, err
	}
	if marker != nil {
		if err := marker.matches(req, secret); err != nil {
			return nil, fmt.Errorf("refusing bootstrap retry: %w", err)
		}
		if err := verifyEtcdBootstrapToken(ctx, kv, prefix, marker); err != nil {
			return nil, err
		}
		return marker, nil
	}

	// etcd range comparisons apply atomically at transaction commit. Version=0
	// across the whole prefix is therefore an actual empty-prefix predicate,
	// unlike a prior Get followed by per-key comparisons.
	record, err := newEtcdBootstrapRecord(req, secret, time.Now())
	if err != nil {
		return nil, err
	}
	markerValue, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode bootstrap marker: %w", err)
	}
	tokenValue, err := json.Marshal(&record.Token)
	if err != nil {
		return nil, fmt.Errorf("encode bootstrap token record: %w", err)
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.Version(prefix+"/"), "=", 0).WithPrefix(),
		clientv3.Compare(clientv3.CreateRevision(markerKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(etcdBootstrapTokenKey(prefix, record.Token.JTI)), "=", 0),
	}
	accepted, err := kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(markerKey, string(markerValue)), clientv3.OpPut(etcdBootstrapTokenKey(prefix, record.Token.JTI), string(tokenValue))).Commit()
	if err != nil {
		return nil, fmt.Errorf("accept initial managed credential: %w", err)
	}
	if accepted.Succeeded {
		return record, nil
	}
	marker, err = loadEtcdBootstrapRecord(ctx, kv, markerKey)
	if err != nil {
		return nil, err
	}
	if marker == nil {
		return nil, fmt.Errorf("refusing bootstrap: %s is not empty", startup.EtcdPrefixEnv)
	}
	if err := marker.matches(req, secret); err != nil {
		return nil, fmt.Errorf("refusing bootstrap retry: %w", err)
	}
	if err := verifyEtcdBootstrapToken(ctx, kv, prefix, marker); err != nil {
		return nil, err
	}
	return marker, nil
}

func newEtcdBootstrapRecord(req etcdBootstrapRequest, secret string, now time.Time) (*etcdBootstrapRecord, error) {
	token, err := handler.NewManagedAccessTokenRecord(req.Subject, req.Scopes, req.TTL, now)
	if err != nil {
		return nil, fmt.Errorf("prepare initial managed credential: %w", err)
	}
	return &etcdBootstrapRecord{Version: etcdBootstrapRecordVersion, State: etcdBootstrapAccepted, Token: *token, TTLNanoseconds: int64(req.TTL), OutputPathHash: hashBootstrapOutputPath(req.Output), SigningKeyHash: hashBootstrapSigningKey(secret)}, nil
}

func (record *etcdBootstrapRecord) matches(req etcdBootstrapRequest, secret string) error {
	if record == nil || record.Version != etcdBootstrapRecordVersion || record.State != etcdBootstrapAccepted || record.Token.JTI == "" || record.TTLNanoseconds <= 0 || record.OutputPathHash == "" || record.SigningKeyHash == "" {
		return errors.New("bootstrap marker has an unsupported or incomplete record")
	}
	if record.Token.Subject != req.Subject || record.TTLNanoseconds != int64(req.TTL) || record.OutputPathHash != hashBootstrapOutputPath(req.Output) || record.SigningKeyHash != hashBootstrapSigningKey(secret) {
		return errors.New("accepted bootstrap parameters differ")
	}
	expected, err := handler.NewManagedAccessTokenRecord(req.Subject, req.Scopes, req.TTL, record.Token.IssuedAt)
	if err != nil {
		return err
	}
	if !sameBootstrapScopes(record.Token.Scopes, expected.Scopes) || record.Token.RevokedAt != nil || record.Token.RotatedFrom != "" || !record.Token.ExpiresAt.Equal(record.Token.IssuedAt.Add(req.TTL)) {
		return errors.New("accepted bootstrap token metadata differs")
	}
	return nil
}

func loadEtcdBootstrapRecord(ctx context.Context, kv clientv3.KV, key string) (*etcdBootstrapRecord, error) {
	response, err := kv.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("load bootstrap marker: %w", err)
	}
	if len(response.Kvs) == 0 {
		return nil, nil
	}
	var record etcdBootstrapRecord
	if err := json.Unmarshal(response.Kvs[0].Value, &record); err != nil {
		return nil, fmt.Errorf("decode bootstrap marker: %w", err)
	}
	return &record, nil
}

func verifyEtcdBootstrapToken(ctx context.Context, kv clientv3.KV, prefix string, record *etcdBootstrapRecord) error {
	response, err := kv.Get(ctx, etcdBootstrapTokenKey(prefix, record.Token.JTI))
	if err != nil {
		return fmt.Errorf("load accepted bootstrap token: %w", err)
	}
	if len(response.Kvs) != 1 {
		return errors.New("accepted bootstrap marker has no token registry entry")
	}
	var token store.AccessToken
	if err := json.Unmarshal(response.Kvs[0].Value, &token); err != nil {
		return fmt.Errorf("decode accepted bootstrap token: %w", err)
	}
	if !sameBootstrapToken(&record.Token, &token) {
		return errors.New("accepted bootstrap marker and token registry disagree")
	}
	return nil
}

// requireInitialEtcdBootstrap prevents the normal Fleet router from becoming
// the first Norn writer in a fresh prefix. The bootstrap command is the only
// supported first-write path because it prepares and verifies the initial
// credential and its recovery record before accepting the marker.
func requireInitialEtcdBootstrap(ctx context.Context, kv clientv3.KV, prefix, signingKey string) error {
	prefix = strings.TrimRight(prefix, "/")
	record, err := loadEtcdBootstrapRecord(ctx, kv, etcdBootstrapMarkerKey(prefix))
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("initial managed credential bootstrap is required before normal etcd Fleet runtime")
	}
	if record.Version != etcdBootstrapRecordVersion || record.State != etcdBootstrapAccepted || record.Token.JTI == "" || record.TTLNanoseconds <= 0 || record.OutputPathHash == "" || record.SigningKeyHash != hashBootstrapSigningKey(signingKey) {
		return errors.New("initial managed credential bootstrap record is invalid")
	}
	if err := verifyEtcdBootstrapToken(ctx, kv, prefix, record); err != nil {
		return err
	}
	return requireUsableInitialEtcdCredential(record, time.Now())
}

func requireUsableInitialEtcdCredential(record *etcdBootstrapRecord, now time.Time) error {
	if record == nil || record.Token.RevokedAt != nil {
		return errors.New("initial managed credential is revoked")
	}
	if !record.Token.ExpiresAt.After(now.UTC().Add(minimumBootstrapRemaining)) {
		return fmt.Errorf("initial managed credential must remain valid for at least %s", minimumBootstrapRemaining)
	}
	return nil
}

func sameBootstrapToken(a, b *store.AccessToken) bool {
	if a == nil || b == nil || a.JTI != b.JTI || a.DeviceID != b.DeviceID || a.Subject != b.Subject || !a.IssuedAt.Equal(b.IssuedAt) || !a.ExpiresAt.Equal(b.ExpiresAt) || a.RotatedFrom != b.RotatedFrom || !sameBootstrapScopes(a.Scopes, b.Scopes) {
		return false
	}
	return (a.RevokedAt == nil && b.RevokedAt == nil) || (a.RevokedAt != nil && b.RevokedAt != nil && a.RevokedAt.Equal(*b.RevokedAt))
}

func sameBootstrapScopes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func etcdBootstrapMarkerKey(prefix string) string {
	return strings.TrimRight(prefix, "/") + "/v3/bootstrap/initial-managed-credential"
}
func etcdBootstrapTokenKey(prefix, jti string) string {
	return strings.TrimRight(prefix, "/") + "/auth/token/" + jti
}

func hashBootstrapOutputPath(output string) string {
	sum := sha256.Sum256([]byte(output))
	return hex.EncodeToString(sum[:])
}

func hashBootstrapSigningKey(signingKey string) string {
	sum := sha256.Sum256([]byte(signingKey))
	return hex.EncodeToString(sum[:])
}

// publishBootstrapTokenFile makes both file data and the directory entry
// durable. It never replaces an existing path, including an uncertain file
// from a failed attempt.
func publishBootstrapTokenFile(output, token string) (err error) {
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return resumeBootstrapTokenFile(output, token)
		}
		return fmt.Errorf("create %s: %w", startup.EtcdBootstrapTokenFileEnv, err)
	}
	closed := false
	removable := true
	defer func() {
		if !closed {
			if closeErr := file.Close(); err == nil && closeErr != nil {
				err = fmt.Errorf("close bootstrap token: %w", closeErr)
			}
		}
		if err != nil && removable {
			_ = os.Remove(output)
		}
	}()
	if _, err = file.WriteString(token + "\n"); err != nil {
		return fmt.Errorf("write bootstrap token: %w", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Errorf("sync bootstrap token: %w", err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("close bootstrap token: %w", err)
	}
	closed = true
	// After a successful file fsync and close, retain the path even if syncing
	// its directory reports an error. The file may already be visible after a
	// crash, and a retry must never overwrite that uncertain publication.
	removable = false
	directory, err := os.Open(filepath.Dir(output))
	if err != nil {
		return fmt.Errorf("open bootstrap token directory: %w", err)
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return fmt.Errorf("sync bootstrap token directory: %w", err)
	}
	return nil
}

// resumeBootstrapTokenFile only completes fsync for a prior exact publication.
// It never changes bytes or permissions, and refuses a different file so a
// retried bootstrap cannot replace an uncertain bearer after a crash.
func resumeBootstrapTokenFile(output, token string) error {
	entry, err := os.Lstat(output)
	if err != nil {
		return fmt.Errorf("inspect existing %s: %w", startup.EtcdBootstrapTokenFileEnv, err)
	}
	if !entry.Mode().IsRegular() || entry.Mode().Perm() != 0o600 {
		return fmt.Errorf("existing %s is not an owner-only regular token file", startup.EtcdBootstrapTokenFileEnv)
	}
	file, err := os.OpenFile(output, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open existing %s: %w", startup.EtcdBootstrapTokenFileEnv, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat existing %s: %w", startup.EtcdBootstrapTokenFileEnv, err)
	}
	if !os.SameFile(entry, opened) || opened.Mode().Perm() != 0o600 {
		return fmt.Errorf("existing %s changed while resuming publication", startup.EtcdBootstrapTokenFileEnv)
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read existing %s: %w", startup.EtcdBootstrapTokenFileEnv, err)
	}
	if string(contents) != token+"\n" {
		return fmt.Errorf("existing %s does not contain the accepted bootstrap token", startup.EtcdBootstrapTokenFileEnv)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync existing bootstrap token: %w", err)
	}
	directory, err := os.Open(filepath.Dir(output))
	if err != nil {
		return fmt.Errorf("open bootstrap token directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync bootstrap token directory: %w", err)
	}
	return nil
}

func splitBootstrapScopes(raw string) []string {
	var scopes []string
	for _, scope := range strings.Split(raw, ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}
