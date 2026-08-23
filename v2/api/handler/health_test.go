package handler

import (
	"path/filepath"
	"testing"
)

func TestSOPSAgeKeyFileHonorsExplicitConfiguration(t *testing.T) {
	want := filepath.Join(t.TempDir(), "age-key.txt")
	t.Setenv("SOPS_AGE_KEY_FILE", "  "+want+"  ")
	if got := sopsAgeKeyFile(); got != want {
		t.Fatalf("sopsAgeKeyFile() = %q, want %q", got, want)
	}
}
