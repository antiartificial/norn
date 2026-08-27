package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"norn/v2/api/connector"
	"norn/v2/api/engine"
	"norn/v2/api/model"
)

func TestProductionAdmissionRequiresImmutableHealthySource(t *testing.T) {
	p := &Pipeline{Production: true, NetworkMode: "tailnet"}
	st := &state{
		sourceKind:  "local_copy",
		sourceDirty: true,
		spec: &model.InfraSpec{
			App: "demo",
			Processes: map[string]model.Process{
				"web": {Port: 8080},
			},
		},
	}
	err := p.admission(context.Background(), st, nil)
	if err == nil {
		t.Fatal("expected production admission to fail")
	}
	for _, expected := range []string{"git clone", "dirty", "NORN_REGISTRY_URL", "health check", "build configuration"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("admission error missing %q: %v", expected, err)
		}
	}
}

func TestProductionArtifactAdmissionFailsWhenRegistryDigestDisappears(t *testing.T) {
	p := &Pipeline{Production: true, VerifyArtifact: func(context.Context, string) error { return errors.New("manifest unknown") }}
	st := &state{imageTag: "registry.example.test/norn/demo@sha256:" + strings.Repeat("a", 64)}
	if err := p.artifactAdmission(context.Background(), st, nil); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("registry verification error=%v", err)
	}
}

func TestRegistryArtifactInspectionUsesDisposableWritableBuildxConfig(t *testing.T) {
	binDir := t.TempDir()
	recordPath := filepath.Join(t.TempDir(), "buildx-config")
	dockerPath := filepath.Join(binDir, "docker")
	script := `#!/bin/sh
set -eu
test "$1 $2 $3" = "buildx imagetools inspect"
test -n "${BUILDX_CONFIG:-}"
test -d "${BUILDX_CONFIG}"
touch "${BUILDX_CONFIG}/probe"
printf '%s' "${BUILDX_CONFIG}" >"${NORN_TEST_BUILDX_RECORD}"
`
	if err := os.WriteFile(dockerPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_CONFIG", "/read-only/registry-config")
	t.Setenv("NORN_TEST_BUILDX_RECORD", recordPath)
	ref := "registry.example.test/norn/demo@sha256:" + strings.Repeat("a", 64)
	if err := (&Pipeline{}).verifyRegistryArtifact(context.Background(), ref); err != nil {
		t.Fatalf("registry inspection failed: %v", err)
	}
	used, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(used)); !os.IsNotExist(err) {
		t.Fatalf("temporary buildx config was not removed: %q err=%v", used, err)
	}
}

func TestBoundedPolicyOutputPreservesUTF8AtByteLimit(t *testing.T) {
	output := append(bytes.Repeat([]byte("界"), 2000), 0xff)
	message := boundedPolicyOutput(output, 4096)
	if len(message) > 4096 {
		t.Fatalf("bounded message has %d bytes", len(message))
	}
	if !utf8.ValidString(message) {
		t.Fatal("bounded message is not valid UTF-8")
	}
	if !strings.Contains(message, "界") {
		t.Fatal("bounded message lost valid policy output")
	}
}

func TestProductionAdmissionAcceptsCleanRegistrySource(t *testing.T) {
	p := &Pipeline{Production: true, NetworkMode: "tailnet", RegistryURL: "registry.example.test/norn"}
	st := &state{
		sourceKind: "git_clone",
		spec: &model.InfraSpec{
			App:   "demo",
			Repo:  &model.RepoSpec{URL: "ssh://git.example.test/demo"},
			Build: &model.BuildSpec{Image: "registry.example.test/norn/demo@sha256:" + strings.Repeat("a", 64)},
			Processes: map[string]model.Process{
				"web": {Port: 8080, Health: &model.HealthSpec{Path: "/health"}, Resources: &model.Resources{CPU: 100, Memory: 128}},
			},
		},
	}
	if err := p.admission(context.Background(), st, nil); err != nil {
		t.Fatalf("production admission failed: %v", err)
	}
}

func TestDevelopmentAdmissionPreservesCompatibility(t *testing.T) {
	p := &Pipeline{}
	if err := p.admission(context.Background(), &state{}, nil); err != nil {
		t.Fatalf("development admission failed: %v", err)
	}
}

func TestConnectorAdmissionRunsBeforeMutableDeployStages(t *testing.T) {
	p := &Pipeline{Workloads: connector.NewApple(&engine.Engine{})}
	st := &state{spec: &model.InfraSpec{App: "sample", Processes: map[string]model.Process{
		"web": {Port: 8080, Scaling: &model.Scaling{Min: 2}},
	}}}
	err := p.admission(context.Background(), st, nil)
	if err == nil || !strings.Contains(err.Error(), "workload connector admission blocked") || !strings.Contains(err.Error(), "multiple local allocations") {
		t.Fatalf("connector admission error = %v", err)
	}
}

func TestProductionAdmissionRejectsBuildWithoutPrepublishedDigest(t *testing.T) {
	p := &Pipeline{Production: true, NetworkMode: "tailnet", RegistryURL: "registry.example.test/norn"}
	st := &state{sourceKind: "git_clone", spec: &model.InfraSpec{
		App: "demo", Repo: &model.RepoSpec{URL: "https://example.test/demo.git"}, Build: &model.BuildSpec{Dockerfile: "Dockerfile"},
		Processes: map[string]model.Process{"web": {Port: 8080, Health: &model.HealthSpec{Path: "/health"}}},
	}}
	if err := p.admission(context.Background(), st, nil); err == nil || !strings.Contains(err.Error(), "externally published") {
		t.Fatalf("admission error=%v", err)
	}
}

func TestProductionArtifactAdmissionRequiresDigest(t *testing.T) {
	verified := ""
	p := &Pipeline{
		Production:      true,
		VerifyArtifact:  func(_ context.Context, ref string) error { verified = ref; return nil },
		VerifySignature: func(context.Context, string) error { return nil },
		ScanArtifact:    func(context.Context, string) error { return nil },
	}
	st := &state{imageTag: "registry.example.test/norn/demo:deadbeef"}
	if err := p.artifactAdmission(context.Background(), st, nil); err == nil {
		t.Fatal("expected mutable production image tag to be rejected")
	}
	st.imageTag = "registry.example.test/norn/demo@sha256:" + strings.Repeat("a", 64)
	if err := p.artifactAdmission(context.Background(), st, nil); err != nil {
		t.Fatalf("content-addressed production image was rejected: %v", err)
	}
	if verified != st.imageTag {
		t.Fatalf("verified ref = %q, want %q", verified, st.imageTag)
	}
	st.preflight = true
	st.imageTag = "demo:local"
	if err := p.artifactAdmission(context.Background(), st, nil); err != nil {
		t.Fatalf("read-only production preflight should not require a pushed digest: %v", err)
	}
}

func TestProductionArtifactAdmissionEnforcesSignatureAndVulnerabilityPolicy(t *testing.T) {
	ref := "registry.example.test/norn/demo@sha256:" + strings.Repeat("a", 64)
	p := &Pipeline{
		Production:      true,
		VerifyArtifact:  func(context.Context, string) error { return nil },
		VerifySignature: func(context.Context, string) error { return errors.New("signature missing") },
		ScanArtifact:    func(context.Context, string) error { return nil },
	}
	if err := p.artifactAdmission(context.Background(), &state{imageTag: ref}, nil); err == nil || !strings.Contains(err.Error(), "signature missing") {
		t.Fatalf("signature policy error=%v", err)
	}
	p.VerifySignature = func(context.Context, string) error { return nil }
	p.ScanArtifact = func(context.Context, string) error { return errors.New("critical vulnerability") }
	if err := p.artifactAdmission(context.Background(), &state{imageTag: ref}, nil); err == nil || !strings.Contains(err.Error(), "critical vulnerability") {
		t.Fatalf("vulnerability policy error=%v", err)
	}
}
