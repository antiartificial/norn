package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageReferenceFromMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	digest := strings.Repeat("b", 64)
	if err := os.WriteFile(path, []byte(`{"containerimage.digest":"sha256:`+digest+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := imageReferenceFromMetadata("registry.example.test/norn/demo", path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "registry.example.test/norn/demo@sha256:" + digest; got != want {
		t.Fatalf("image reference = %q, want %q", got, want)
	}
}

func TestImageReferenceFromMetadataRejectsMissingDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := imageReferenceFromMetadata("registry.example.test/norn/demo", path); err == nil {
		t.Fatal("expected missing digest to fail")
	}
}
