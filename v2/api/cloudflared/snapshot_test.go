package cloudflared

import (
	"context"
	"os"
	"path/filepath"
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
