package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeylessVerifierParsesOnlyVerifiedStdout(t *testing.T) {
	script := filepath.Join(t.TempDir(), "cosign")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"payload\":\"verified\"}'\nprintf '%s\\n' 'Verification for image digest --' >&2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := (&Pipeline{}).runArtifactCommand(context.Background(), script, "verify-attestation")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "{\"payload\":\"verified\"}\n" {
		t.Fatalf("verifier output includes diagnostics: %q", output)
	}
}

func TestKeylessVerifierPreservesFailureDiagnostic(t *testing.T) {
	script := filepath.Join(t.TempDir(), "cosign")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' 'missing matching attestation' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := (&Pipeline{}).runArtifactCommand(context.Background(), script, "verify-attestation")
	if err == nil || !strings.Contains(err.Error(), "missing matching attestation") {
		t.Fatalf("failure diagnostic = %v", err)
	}
}
