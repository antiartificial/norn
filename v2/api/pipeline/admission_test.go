package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

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

func TestProductionAdmissionAcceptsCallerBoundArtifactWithDockerfileBuild(t *testing.T) {
	p := &Pipeline{Production: true, NetworkMode: "tailnet", RegistryURL: "registry.example.test/norn"}
	st := &state{sourceKind: "git_clone", artifactBound: true, imageTag: "registry.example.test/norn/demo@sha256:" + strings.Repeat("a", 64), spec: &model.InfraSpec{
		App: "demo", Repo: &model.RepoSpec{URL: "https://example.test/demo.git"}, Build: &model.BuildSpec{Dockerfile: "Dockerfile"},
		Processes: map[string]model.Process{"web": {Port: 8080, Health: &model.HealthSpec{Path: "/health"}}},
	}}
	if err := p.admission(context.Background(), st, nil); err != nil {
		t.Fatalf("bound release artifact should satisfy immutable image admission: %v", err)
	}
}

func TestProductionArtifactAdmissionBindsRepositoryToAppPolicy(t *testing.T) {
	p := &Pipeline{
		Production: true, RegistryURL: "registry.example.test/norn",
		VerifyArtifact: func(context.Context, string) error { return nil }, VerifySignature: func(context.Context, string) error { return nil }, ScanArtifact: func(context.Context, string) error { return nil },
	}
	st := &state{
		artifactBound: true,
		imageTag:      "registry.example.test/another/demo@sha256:" + strings.Repeat("a", 64),
		spec:          &model.InfraSpec{App: "demo", Build: &model.BuildSpec{Dockerfile: "Dockerfile"}},
	}
	if err := p.artifactAdmission(context.Background(), st, nil); err == nil || !strings.Contains(err.Error(), "authorized repository") {
		t.Fatalf("cross-repository artifact error=%v", err)
	}
	st.imageTag = "registry.example.test/norn/demo@sha256:" + strings.Repeat("a", 64)
	if err := p.artifactAdmission(context.Background(), st, nil); err != nil {
		t.Fatalf("authorized app artifact rejected: %v", err)
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

func TestQualifiedWordPressPrebuiltKeepsRegistryAndVulnerabilityGates(t *testing.T) {
	ref := model.QualifiedWordPressVerifiedTLSImage
	registryCalls, signatureCalls, scanCalls := 0, 0, 0
	p := &Pipeline{Production: true,
		VerifyArtifact: func(_ context.Context, image string) error {
			registryCalls++
			if image != ref {
				t.Fatalf("registry image=%q", image)
			}
			return nil
		},
		VerifySignature: func(context.Context, string) error { signatureCalls++; return errors.New("unsigned upstream image") },
		ScanArtifact: func(_ context.Context, image string) error {
			scanCalls++
			if image != ref {
				t.Fatalf("scan image=%q", image)
			}
			return nil
		},
	}
	st := &state{spec: qualifiedWordPressRuntimeSpec(), imageTag: ref}
	if err := p.artifactAdmission(context.Background(), st, nil); err != nil {
		t.Fatalf("exact qualified upstream image rejected: %v", err)
	}
	if registryCalls != 1 || signatureCalls != 0 || scanCalls != 1 {
		t.Fatalf("artifact gates registry=%d signature=%d scan=%d", registryCalls, signatureCalls, scanCalls)
	}
	p.VerifyArtifact = func(context.Context, string) error { return errors.New("missing manifest") }
	if err := p.artifactAdmission(context.Background(), st, nil); err == nil || !strings.Contains(err.Error(), "missing manifest") {
		t.Fatalf("missing qualified manifest accepted: %v", err)
	}
	p.VerifyArtifact = func(context.Context, string) error { return nil }
	p.ScanArtifact = func(context.Context, string) error { return errors.New("vulnerability denied") }
	if err := p.artifactAdmission(context.Background(), st, nil); err == nil || !strings.Contains(err.Error(), "vulnerability denied") {
		t.Fatalf("vulnerable qualified artifact accepted: %v", err)
	}
}

func TestProductionAdmissionAcceptsQualifiedWordPressPrebuilt(t *testing.T) {
	spec := qualifiedWordPressRuntimeSpec()
	spec.Repo = &model.RepoSpec{URL: "https://example.test/wordpress-config.git"}
	web := spec.Processes["web"]
	web.Health = &model.HealthSpec{Path: "/"}
	spec.Processes["web"] = web
	p := &Pipeline{Production: true, NetworkMode: "tailnet", RegistryURL: "registry.example.test/norn"}
	st := &state{sourceKind: "git_clone", spec: spec, imageTag: model.QualifiedWordPressVerifiedTLSImage}
	if err := p.admission(context.Background(), st, nil); err != nil {
		t.Fatalf("qualified prebuilt production source admission failed: %v", err)
	}
}

func TestQualifiedWordPressPrebuiltExceptionIsExact(t *testing.T) {
	ref := model.QualifiedWordPressVerifiedTLSImage
	for name, mutate := range map[string]func(*state){
		"missing spec":        func(s *state) { s.spec = nil },
		"generic consumer":    func(s *state) { s.spec.StartupAdapter = "" },
		"different image":     func(s *state) { s.imageTag = "docker.io/library/wordpress@sha256:" + strings.Repeat("a", 64) },
		"spec image mismatch": func(s *state) { s.spec.Build.Image = "docker.io/library/wordpress@sha256:" + strings.Repeat("a", 64) },
		"mutable spec image":  func(s *state) { s.spec.Build.Image = "wordpress:6.8.2-php8.3-apache" },
		"missing content":     func(s *state) { s.spec.Volumes = nil },
		"custom command":      func(s *state) { s.spec.Processes["web"] = model.Process{Command: "apache2-foreground"} },
		"wrong runtime":       func(s *state) { s.spec.Databases[0].Runtime.Components.Host = "OTHER_DB_HOST" },
		"missing ca":          func(s *state) { s.spec.Databases[0].Runtime.TLS = nil },
		"client certificate":  func(s *state) { s.spec.Databases[0].Runtime.TLS.ClientCertFileEnv = "MYSQL_SSL_CERT" },
		"extra process":       func(s *state) { s.spec.Processes["worker"] = model.Process{Command: "true"} },
		"bound release":       func(s *state) { s.artifactBound = true },
	} {
		t.Run(name, func(t *testing.T) {
			signatureCalls := 0
			p := &Pipeline{Production: true, VerifyArtifact: func(context.Context, string) error { return nil },
				VerifySignature: func(context.Context, string) error { signatureCalls++; return errors.New("unsigned upstream image") },
				ScanArtifact:    func(context.Context, string) error { return nil }}
			st := &state{spec: qualifiedWordPressRuntimeSpec(), imageTag: ref}
			mutate(st)
			if err := p.artifactAdmission(context.Background(), st, nil); err == nil {
				t.Fatal("near-miss artifact passed production admission")
			}
			if signatureCalls != 1 {
				t.Fatalf("near-miss signature calls=%d", signatureCalls)
			}
		})
	}
}
