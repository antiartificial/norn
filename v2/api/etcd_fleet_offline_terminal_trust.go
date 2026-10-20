package main

// Target Fleet terminal trust is deliberately independent of the PostgreSQL
// management authority's owner approval key and provider credentials. Loading
// it validates configuration only; it does not register any mutation route.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/startup"
)

const targetTerminalManifestSchema = "norn.fleet-target-terminal-trust/v1"
const targetTerminalManifestDomain = targetTerminalManifestSchema + "\x00"

var targetTerminalSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type targetTerminalManifest struct {
	SchemaVersion     string    `json:"schemaVersion"`
	TargetID          string    `json:"targetId"`
	EtcdPrefix        string    `json:"etcdPrefix"`
	ControlAuthority  string    `json:"controlAuthority"`
	SourceCommit      string    `json:"sourceCommit"`
	ExpiresAt         time.Time `json:"expiresAt"`
	RunnerSubject     string    `json:"runnerSubject"`
	VerifierSubject   string    `json:"verifierSubject"`
	ObserverSubject   string    `json:"observerSubject"`
	VerifierDeviceID  string    `json:"verifierDeviceId"`
	ObserverDeviceID  string    `json:"observerDeviceId"`
	ObserverKeyID     string    `json:"observerKeyId"`
	ObserverPublicKey string    `json:"observerPublicKey"`
	TrustGeneration   uint64    `json:"trustGeneration"`
}

type signedTargetTerminalManifest struct {
	Manifest    targetTerminalManifest `json:"manifest"`
	SignerKeyID string                 `json:"signerKeyId"`
	Signature   string                 `json:"signature"`
}

type targetTerminalTrust struct {
	transport      offlineTerminalTransportTrust
	observerKeyID  string
	observerPublic ed25519.PublicKey
	generation     uint64
	targetID       string
	sourceCommit   string
	expiresAt      time.Time
}

func targetTerminalAtom(s string) bool {
	return s != "" && len(s) <= 256 && !strings.ContainsAny(s, " \t\r\n\x00")
}

// readTargetTrustFile rejects symlinks, writes by other users, oversized and
// non-owner-only manifests. The manifest belongs to the Norn service user;
// the separately pinned signer key is root-owned on the production runtime.
func readTargetTrustFile(path string, max int64, ownerOnly bool, requiredUID uint32, secureParents bool) ([]byte, error) {
	if !filepath.IsAbs(path) || max <= 0 {
		return nil, fmt.Errorf("target terminal trust file path must be absolute")
	}
	if secureParents {
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			info, err := os.Lstat(dir)
			if err != nil {
				return nil, fmt.Errorf("stat target terminal signer parent: %w", err)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
				return nil, fmt.Errorf("target terminal signer key requires root-owned non-writable parent directories")
			}
			if dir == string(filepath.Separator) {
				break
			}
		}
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open target terminal trust file: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat target terminal trust file: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Size() > max || info.Size() == 0 ||
		stat.Uid != requiredUID ||
		info.Mode().Perm()&0022 != 0 || (ownerOnly && info.Mode().Perm() != 0400) {
		return nil, fmt.Errorf("target terminal trust file must be bounded, owned, regular and non-writable by others")
	}
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(raw)) != info.Size() || int64(len(raw)) > max {
		return nil, fmt.Errorf("read bounded target terminal trust file")
	}
	return raw, nil
}

func loadTargetOfflineTerminalTrust(cfg *config.Config, backend startup.ControlBackendConfig, now time.Time) (*targetTerminalTrust, error) {
	return loadTargetOfflineTerminalTrustForOwners(cfg, backend, now, uint32(os.Geteuid()), 0, true)
}

func loadTargetOfflineTerminalTrustForOwner(cfg *config.Config, backend startup.ControlBackendConfig, now time.Time, requiredUID uint32) (*targetTerminalTrust, error) {
	return loadTargetOfflineTerminalTrustForOwners(cfg, backend, now, requiredUID, requiredUID, false)
}

func loadTargetOfflineTerminalTrustForOwners(cfg *config.Config, backend startup.ControlBackendConfig, now time.Time, manifestUID, signerUID uint32, secureSignerParents bool) (*targetTerminalTrust, error) {
	if cfg == nil {
		return nil, nil
	}
	manifestPath, signerPath, signerID := cfg.FleetTargetTerminalManifestFile,
		cfg.FleetTargetTerminalSignerPublicKeyFile, cfg.FleetTargetTerminalSignerKeyID
	if !cfg.FleetTargetTerminalEnabled {
		if manifestPath != "" || signerPath != "" || signerID != "" || cfg.FleetTargetTerminalTargetID != "" {
			return nil, fmt.Errorf("target terminal trust fields require explicit enable")
		}
		return nil, nil
	}
	if cfg.FleetAuthorityOnly || cfg.ProfileID() != "production" || cfg.EnvironmentID() != "staging" || !cfg.EnvironmentExplicit ||
		backend.Backend != startup.BackendEtcd || backend.SourceValidation || cfg.ControlAuthority == "" || backend.EtcdPrefix == "" ||
		manifestPath == "" || signerPath == "" || !targetTerminalAtom(signerID) || !targetTerminalAtom(cfg.FleetTargetTerminalTargetID) {
		return nil, fmt.Errorf("target terminal trust requires explicit production/staging etcd target and complete separate trust settings")
	}
	keyRaw, err := readTargetTrustFile(signerPath, 256, false, signerUID, secureSignerParents)
	if err != nil {
		return nil, err
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(strings.TrimSpace(string(keyRaw)))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("target terminal signer key must be raw-base64 Ed25519")
	}
	raw, err := readTargetTrustFile(manifestPath, 4096, true, manifestUID, false)
	if err != nil {
		return nil, err
	}
	var signed signedTargetTerminalManifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&signed) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("target terminal manifest is malformed")
	}
	canonical, err := json.Marshal(signed)
	if err != nil || !bytes.Equal(raw, canonical) || signed.SignerKeyID != signerID {
		return nil, fmt.Errorf("target terminal manifest is not canonical or signer differs")
	}
	m := signed.Manifest
	message, err := json.Marshal(m)
	sig, sigErr := base64.RawStdEncoding.Strict().DecodeString(signed.Signature)
	if err != nil || sigErr != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, append([]byte(targetTerminalManifestDomain), message...), sig) {
		return nil, fmt.Errorf("target terminal manifest signature is invalid")
	}
	observer, err := base64.RawStdEncoding.Strict().DecodeString(m.ObserverPublicKey)
	transport := offlineTerminalTransportTrust{runnerSubject: m.RunnerSubject, verifierSubject: m.VerifierSubject,
		observerSubject: m.ObserverSubject, verifierDeviceID: m.VerifierDeviceID, observerDeviceID: m.ObserverDeviceID}
	if err != nil || len(observer) != ed25519.PublicKeySize || m.SchemaVersion != targetTerminalManifestSchema ||
		!transport.valid() || m.TargetID != cfg.FleetTargetTerminalTargetID || !targetTerminalAtom(m.ObserverKeyID) ||
		m.EtcdPrefix != backend.EtcdPrefix || m.ControlAuthority != cfg.ControlAuthority ||
		!targetTerminalSHA.MatchString(m.SourceCommit) || m.TrustGeneration == 0 ||
		m.ExpiresAt.IsZero() || !now.Before(m.ExpiresAt) || now.Add(30*24*time.Hour).Before(m.ExpiresAt) {
		return nil, fmt.Errorf("target terminal manifest identity, lane or expiry differs")
	}
	transport.targetID, transport.sourceCommit, transport.expiresAt = m.TargetID, m.SourceCommit, m.ExpiresAt
	return &targetTerminalTrust{transport: transport, observerKeyID: m.ObserverKeyID,
		observerPublic: ed25519.PublicKey(observer), generation: m.TrustGeneration,
		targetID: m.TargetID, sourceCommit: m.SourceCommit, expiresAt: m.ExpiresAt}, nil
}

type targetTerminalTrustStore interface {
	VerifyOfflineTerminalObserverTrust(context.Context, uint64, string, string, ed25519.PublicKey) error
}

func (t *targetTerminalTrust) reconcile(ctx context.Context, store targetTerminalTrustStore) error {
	if t == nil {
		return nil
	}
	if store == nil {
		return fmt.Errorf("target terminal trust requires an etcd store")
	}
	if err := store.VerifyOfflineTerminalObserverTrust(ctx, t.generation, t.transport.observerSubject, t.observerKeyID, t.observerPublic); err != nil {
		return fmt.Errorf("verify target terminal observer trust: %w", err)
	}
	return nil
}

func (t targetTerminalTrust) observerPublicSHA256() string {
	sum := sha256.Sum256(t.observerPublic)
	return hex.EncodeToString(sum[:])
}
