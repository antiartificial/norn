package pipeline

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"norn/v2/api/model"
)

// This test intentionally uses a real configured registry digest. CI or a
// recovery-drill runner can set NORN_TEST_REGISTRY_REF to exercise registry
// authentication, manifest availability, and rollback-time digest resolution.
func TestRealRegistryDigestAvailability(t *testing.T) {
	ref := os.Getenv("NORN_TEST_REGISTRY_REF")
	if ref == "" {
		t.Skip("NORN_TEST_REGISTRY_REF is not configured")
	}
	if !model.IsContentAddressedImage(ref) {
		t.Fatal("NORN_TEST_REGISTRY_REF must use image@sha256:... form")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := (&Pipeline{}).verifyRegistryArtifact(ctx, ref); err != nil {
		t.Fatalf("real registry digest verification failed: %v", err)
	}
}

// This test exercises the actual Cosign and Trivy admission commands against a
// configured registry digest. The image must already be signed by the supplied
// public key and satisfy the configured deny severities.
func TestRealRegistryArtifactPolicy(t *testing.T) {
	ref := os.Getenv("NORN_TEST_REGISTRY_REF")
	publicKey := os.Getenv("NORN_TEST_COSIGN_PUBLIC_KEY")
	if ref == "" || publicKey == "" {
		t.Skip("NORN_TEST_REGISTRY_REF and NORN_TEST_COSIGN_PUBLIC_KEY are not configured")
	}
	if !model.IsContentAddressedImage(ref) {
		t.Fatal("NORN_TEST_REGISTRY_REF must use image@sha256:... form")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	p := &Pipeline{
		Production:               true,
		ArtifactSigningPublicKey: publicKey,
		ArtifactDenySeverities:   []string{"HIGH", "CRITICAL"},
		CosignPath:               envOrTest("NORN_TEST_COSIGN_PATH", "cosign"),
		TrivyPath:                envOrTest("NORN_TEST_TRIVY_PATH", "trivy"),
		VerifyArtifact:           func(context.Context, string) error { return nil },
	}
	if err := p.artifactAdmission(ctx, &state{imageTag: ref, commitSHA: os.Getenv("NORN_TEST_GIT_SHA")}, nil); err != nil {
		t.Fatalf("real registry artifact policy failed: %v", err)
	}
}

func envOrTest(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
