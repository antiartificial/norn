package cloudflared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadConfigSnapshotBindsUnknownFieldBytes(t *testing.T) {
	prior := configPath
	path := filepath.Join(t.TempDir(), "config.yml")
	SetConfigPath(path)
	t.Cleanup(func() { SetConfigPath(prior) })
	first := []byte("tunnel: demo\ningress:\n  - service: http_status:404\nunknown-option: first\n")
	if err := os.WriteFile(path, first, 0600); err != nil {
		t.Fatal(err)
	}
	_, before, err := ReadConfigSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second := []byte("tunnel: demo\ningress:\n  - service: http_status:404\nunknown-option: second\n")
	if err := os.WriteFile(path, second, 0600); err != nil {
		t.Fatal(err)
	}
	_, after, err := ReadConfigSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("unknown config field edit did not change worker precondition")
	}
}

func TestIngressEditPreservesUnknownYAMLAndMatchesPublishedDigest(t *testing.T) {
	prior := configPath
	path := filepath.Join(t.TempDir(), "config.yml")
	SetConfigPath(path)
	t.Cleanup(func() { SetConfigPath(prior) })
	original := "# Mini tunnel\ntunnel: demo\nunknown-option: keep-me # owner note\ningress:\n  - hostname: existing.example.com # existing host\n    service: http://127.0.0.1:8080\n    originRequest:\n      noTLSVerify: true\n  - service: http_status:404 # fallback\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := ReadConfigSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !AddIngress(cfg, "https://existing.example.com", "http://127.0.0.1:8181") {
		t.Fatal("existing ingress did not update service")
	}
	if !AddIngress(cfg, "https://new.example.com", "http://127.0.0.1:9090") {
		t.Fatal("new ingress did not change config")
	}
	wantDigest, err := ConfigDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	published, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := sha256.Sum256(published)
	if hex.EncodeToString(got[:]) != wantDigest {
		t.Fatal("accepted config digest differs from published file")
	}
	for _, retained := range []string{"# Mini tunnel", "unknown-option: keep-me", "# owner note", "# existing host", "originRequest:", "noTLSVerify: true", "# fallback", "http://127.0.0.1:8181", "new.example.com"} {
		if !strings.Contains(string(published), retained) {
			t.Fatalf("published config lost %q: %s", retained, published)
		}
	}
}
