package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/model"
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

func TestBuildPreservesBoundReleaseArtifact(t *testing.T) {
	digest := "registry.example.test/app@sha256:" + strings.Repeat("a", 64)
	st := &state{artifactBound: true, imageTag: digest, spec: &model.InfraSpec{App: "app"}}
	if err := (&Pipeline{}).build(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	if st.imageTag != digest {
		t.Fatalf("bound artifact changed to %q", st.imageTag)
	}
}
