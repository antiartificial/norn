package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/startup"
)

func TestTargetTerminalTrustFailsClosed(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	observer, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "signer.pub")
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(keyPath, []byte(base64.RawStdEncoding.EncodeToString(public)), 0600); err != nil {
		t.Fatal(err)
	}
	m := targetTerminalManifest{SchemaVersion: targetTerminalManifestSchema, TargetID: "work-staging",
		EtcdPrefix: "/norn/work-staging", ControlAuthority: "authority-1", SourceCommit: strings.Repeat("a", 40),
		ExpiresAt: now.Add(24 * time.Hour), RunnerSubject: "runner", VerifierSubject: "verifier", ObserverSubject: "observer",
		VerifierDeviceID: "verifier-device", ObserverDeviceID: "observer-device", ObserverKeyID: "observer-key-1",
		ObserverPublicKey: base64.RawStdEncoding.EncodeToString(observer), TrustGeneration: 1}
	write := func(manifest targetTerminalManifest) {
		t.Helper()
		payload, _ := json.Marshal(manifest)
		signed := signedTargetTerminalManifest{Manifest: manifest, SignerKeyID: "signer-1",
			Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, append([]byte(targetTerminalManifestDomain), payload...)))}
		raw, _ := json.Marshal(signed)
		if err := os.Remove(manifestPath); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, raw, 0400); err != nil {
			t.Fatal(err)
		}
	}
	write(m)
	cfg := config.Config{Profile: "production", Environment: "staging", EnvironmentExplicit: true, ControlAuthority: "authority-1",
		FleetTargetTerminalEnabled: true, FleetTargetTerminalTargetID: "work-staging", FleetTargetTerminalManifestFile: manifestPath,
		FleetTargetTerminalSignerKeyID: "signer-1", FleetTargetTerminalSignerPublicKeyFile: keyPath}
	backend := startup.ControlBackendConfig{Backend: startup.BackendEtcd, EtcdPrefix: "/norn/work-staging"}
	loaded, err := loadTargetOfflineTerminalTrustForOwner(&cfg, backend, now, uint32(os.Geteuid()))
	if err != nil || loaded == nil || loaded.transport.observerSubject != "observer" || loaded.observerPublicSHA256() == "" {
		t.Fatalf("valid target trust rejected: %v", err)
	}
	if os.Geteuid() != 0 {
		if _, err := loadTargetOfflineTerminalTrust(&cfg, backend, now); err == nil {
			t.Fatal("runtime accepted service-owned signer key")
		}
	}
	if _, err := readTargetTrustFile(keyPath, 256, false, uint32(os.Geteuid()), true); err == nil {
		t.Fatal("accepted signer key beneath non-root-owned parent")
	}
	if _, err := loadTargetOfflineTerminalTrustForOwner(&cfg, backend, now, uint32(os.Geteuid()+1)); err == nil {
		t.Fatal("accepted manifest owned by another service UID")
	}
	for _, test := range []struct {
		name   string
		change func(*config.Config, *startup.ControlBackendConfig)
	}{
		{"disabled", func(c *config.Config, _ *startup.ControlBackendConfig) { c.FleetTargetTerminalEnabled = false }},
		{"management", func(c *config.Config, _ *startup.ControlBackendConfig) { c.FleetAuthorityOnly = true }},
		{"wrong profile", func(c *config.Config, _ *startup.ControlBackendConfig) { c.Profile = "development" }},
		{"wrong lane", func(c *config.Config, _ *startup.ControlBackendConfig) { c.Environment = "production" }},
		{"implicit lane", func(c *config.Config, _ *startup.ControlBackendConfig) { c.EnvironmentExplicit = false }},
		{"postgres", func(_ *config.Config, b *startup.ControlBackendConfig) { b.Backend = startup.BackendPostgres }},
		{"source validation", func(_ *config.Config, b *startup.ControlBackendConfig) { b.SourceValidation = true }},
		{"wrong prefix", func(_ *config.Config, b *startup.ControlBackendConfig) { b.EtcdPrefix = "/other" }},
		{"wrong authority", func(c *config.Config, _ *startup.ControlBackendConfig) { c.ControlAuthority = "other" }},
		{"wrong target", func(c *config.Config, _ *startup.ControlBackendConfig) { c.FleetTargetTerminalTargetID = "other" }},
		{"missing manifest", func(c *config.Config, _ *startup.ControlBackendConfig) { c.FleetTargetTerminalManifestFile = "" }},
		{"missing signer", func(c *config.Config, _ *startup.ControlBackendConfig) { c.FleetTargetTerminalSignerPublicKeyFile = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, b := cfg, backend
			test.change(&c, &b)
			if _, err := loadTargetOfflineTerminalTrustForOwner(&c, b, now, uint32(os.Geteuid())); err == nil {
				t.Fatal("accepted invalid target trust")
			}
		})
	}
	for _, test := range []struct {
		name   string
		change func(*targetTerminalManifest)
	}{
		{"expired", func(v *targetTerminalManifest) { v.ExpiresAt = now }},
		{"too distant", func(v *targetTerminalManifest) { v.ExpiresAt = now.Add(31 * 24 * time.Hour) }},
		{"wrong prefix", func(v *targetTerminalManifest) { v.EtcdPrefix = "/other" }},
		{"wrong authority", func(v *targetTerminalManifest) { v.ControlAuthority = "other" }},
		{"bad source", func(v *targetTerminalManifest) { v.SourceCommit = "main" }},
		{"same device", func(v *targetTerminalManifest) { v.ObserverDeviceID = v.VerifierDeviceID }},
		{"same subject", func(v *targetTerminalManifest) { v.ObserverSubject = v.VerifierSubject }},
		{"bad key", func(v *targetTerminalManifest) { v.ObserverPublicKey = "garbage" }},
		{"zero generation", func(v *targetTerminalManifest) { v.TrustGeneration = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			v := m
			test.change(&v)
			write(v)
			if _, err := loadTargetOfflineTerminalTrustForOwner(&cfg, backend, now, uint32(os.Geteuid())); err == nil {
				t.Fatal("accepted invalid signed manifest")
			}
		})
	}
	write(m)
	t.Run("wrong pinned signer", func(t *testing.T) {
		wrong, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, []byte(base64.RawStdEncoding.EncodeToString(wrong)), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTargetOfflineTerminalTrustForOwner(&cfg, backend, now, uint32(os.Geteuid())); err == nil {
			t.Fatal("accepted signer mismatch")
		}
		if err := os.WriteFile(keyPath, []byte(base64.RawStdEncoding.EncodeToString(public)), 0600); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tampered signature", func(t *testing.T) {
		raw, _ := os.ReadFile(manifestPath)
		raw[len(raw)-3] = 'X'
		_ = os.Chmod(manifestPath, 0600)
		_ = os.WriteFile(manifestPath, raw, 0600)
		if _, err := loadTargetOfflineTerminalTrustForOwner(&cfg, backend, now, uint32(os.Geteuid())); err == nil {
			t.Fatal("accepted tamper")
		}
	})
	write(m)
	t.Run("writable manifest", func(t *testing.T) {
		_ = os.Chmod(manifestPath, 0644)
		if _, err := loadTargetOfflineTerminalTrustForOwner(&cfg, backend, now, uint32(os.Geteuid())); err == nil {
			t.Fatal("accepted readable manifest")
		}
		_ = os.Chmod(manifestPath, 0400)
	})
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(dir, "link")
		if err := os.Symlink(manifestPath, link); err != nil {
			t.Fatal(err)
		}
		c := cfg
		c.FleetTargetTerminalManifestFile = link
		if _, err := loadTargetOfflineTerminalTrustForOwner(&c, backend, now, uint32(os.Geteuid())); err == nil {
			t.Fatal("accepted symlink")
		}
	})
	t.Run("oversize", func(t *testing.T) {
		_ = os.Chmod(manifestPath, 0600)
		_ = os.WriteFile(manifestPath, []byte(strings.Repeat("x", 4097)), 0600)
		if _, err := loadTargetOfflineTerminalTrustForOwner(&cfg, backend, now, uint32(os.Geteuid())); err == nil {
			t.Fatal("accepted oversized manifest")
		}
	})
	write(m)
	t.Run("noncanonical", func(t *testing.T) {
		raw, _ := os.ReadFile(manifestPath)
		_ = os.Chmod(manifestPath, 0600)
		_ = os.WriteFile(manifestPath, append(raw, '\n'), 0600)
		if _, err := loadTargetOfflineTerminalTrustForOwner(&cfg, backend, now, uint32(os.Geteuid())); err == nil {
			t.Fatal("accepted noncanonical manifest")
		}
	})
}

type terminalTrustStoreFixture struct {
	verified int
	err      error
}

func (s *terminalTrustStoreFixture) VerifyOfflineTerminalObserverTrust(_ context.Context, generation uint64, subject, keyID string, public ed25519.PublicKey) error {
	s.verified++
	if generation != 2 || subject != "observer" || keyID != "key-2" || len(public) != ed25519.PublicKeySize {
		return os.ErrInvalid
	}
	return s.err
}
func TestTargetTerminalTrustReconcilesBeforeService(t *testing.T) {
	trust := &targetTerminalTrust{generation: 2, observerKeyID: "key-2", observerPublic: make(ed25519.PublicKey, ed25519.PublicKeySize),
		transport: offlineTerminalTransportTrust{observerSubject: "observer", targetID: "target", sourceCommit: strings.Repeat("a", 40)}}
	store := &terminalTrustStoreFixture{}
	if err := trust.reconcile(context.Background(), store); err != nil || store.verified != 1 {
		t.Fatalf("reconcile: %v %#v", err, store)
	}
	if err := trust.reconcile(context.Background(), nil); err == nil {
		t.Fatal("accepted missing store")
	}
	store.err = os.ErrPermission
	if err := trust.reconcile(context.Background(), store); err == nil {
		t.Fatal("accepted trust conflict")
	}
}
