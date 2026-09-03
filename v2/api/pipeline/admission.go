package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"norn/v2/api/model"
	"norn/v2/api/saga"
)

// admission is intentionally a deploy stage after source resolution. A
// production profile must verify the actual source provenance, not only the
// requested ref or repository declaration.
func (p *Pipeline) admission(_ context.Context, st *state, _ *saga.Saga) error {
	// Connector admission belongs before builds, snapshots, migrations, or any
	// scheduler mutation. In particular, the local Apple connector rejects
	// unsupported regional, scheduled, canary, and endpoint-scaling shapes here
	// instead of discovering the mismatch after a database migration.
	if workloads := p.workloadConnector(); workloads != nil {
		if err := workloads.Validate(st.spec, p.Production); err != nil {
			return fmt.Errorf("workload connector admission blocked: %w", err)
		}
	}
	if !p.Production {
		return nil
	}
	var blockers []string
	if st.sourceKind != "git_clone" {
		blockers = append(blockers, "source must be a git clone")
	}
	if st.sourceDirty {
		blockers = append(blockers, "source checkout is dirty")
	}
	if st.spec.Build == nil && !st.artifactBound {
		blockers = append(blockers, "build configuration is required to avoid mutable latest images")
	} else if st.spec.Build != nil && !st.artifactBound && !model.IsContentAddressedImage(st.spec.Build.Image) {
		blockers = append(blockers, "build.image: production requires an externally published image pinned by sha256 OCI digest")
	}
	if strings.TrimSpace(p.RegistryURL) == "" {
		blockers = append(blockers, "NORN_REGISTRY_URL is required for immutable registry-backed images")
	}
	validation := model.ValidateSpecWithOptions(st.spec, model.ValidationOptions{NetworkMode: p.NetworkMode, StrictSecrets: true})
	for _, finding := range validation.Findings {
		if finding.Severity == "error" {
			blockers = append(blockers, finding.Field+": "+finding.Message)
		}
	}
	for name, process := range st.spec.Processes {
		if process.Port > 0 && process.Health == nil {
			blockers = append(blockers, "processes."+name+".health: production services require a health check")
		}
		if len(st.spec.Endpoints) > 0 && process.Scaling != nil && process.Scaling.Min > 1 && strings.TrimSpace(p.IngressURL) == "" {
			blockers = append(blockers, "processes."+name+".scaling: NORN_INGRESS_URL is required for endpoint-backed multiple allocations")
		}
	}
	if len(blockers) == 0 {
		return nil
	}
	sort.Strings(blockers)
	return fmt.Errorf("production admission blocked: %s", strings.Join(blockers, "; "))
}

// artifactAdmission runs after the build. A legacy production preflight does
// not push an image, so it has no registry artifact to admit. An artifact-bound
// release preflight is different: it rehearses deployment of an already-pushed
// digest and must run the same read-only registry, evidence, and vulnerability
// checks as a deploy.
func (p *Pipeline) artifactAdmission(ctx context.Context, st *state, _ *saga.Saga) error {
	if !p.Production || (st.preflight && !st.artifactBound) {
		return nil
	}
	return p.verifyReleaseArtifactAdmission(ctx, st)
}

// verifyReleaseArtifactAdmission performs every immutable artifact check even
// when called outside the normal deploy path. Rollback workers use this at the
// final scheduler-mutation boundary to close the queue-time TOCTOU window.
func (p *Pipeline) verifyReleaseArtifactAdmission(ctx context.Context, st *state) error {
	if st == nil {
		return fmt.Errorf("release artifact admission requires state")
	}
	if !model.IsContentAddressedImage(st.imageTag) {
		return fmt.Errorf("production artifact admission blocked: image must be pinned by sha256 OCI digest")
	}
	if err := p.verifyRegistryArtifact(ctx, st.imageTag); err != nil {
		return fmt.Errorf("production artifact admission blocked: registry digest verification failed: %w", err)
	}
	if err := p.verifyArtifactSignature(ctx, st); err != nil {
		return fmt.Errorf("production artifact admission blocked: publisher signature verification failed: %w", err)
	}
	if err := p.scanArtifactVulnerabilities(ctx, st.imageTag); err != nil {
		return fmt.Errorf("production artifact admission blocked: vulnerability policy failed: %w", err)
	}
	return nil
}

func (p *Pipeline) verifyRegistryArtifact(ctx context.Context, imageRef string) error {
	if p.VerifyArtifact != nil {
		return p.VerifyArtifact(ctx, imageRef)
	}
	// buildx persists instance state below its config directory even for a
	// read-only manifest inspection. Keep that incidental state in a private,
	// disposable directory so admission continues to work when DOCKER_CONFIG
	// contains read-only registry credentials under a hardened service unit.
	buildxConfig, err := os.MkdirTemp("", "norn-buildx-")
	if err != nil {
		return fmt.Errorf("create temporary buildx config: %w", err)
	}
	defer os.RemoveAll(buildxConfig)
	env, cleanup, err := p.registryCommandEnv(buildxConfig)
	if err != nil {
		return err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "inspect", imageRef)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := boundedPolicyOutput(output, 4096)
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("%s", message)
	}
	return nil
}

func (p *Pipeline) verifyArtifactSignature(ctx context.Context, st *state) error {
	if p.ReleaseAdmissionMode == "keyless" || p.ReleaseAdmissionMode == "attested" {
		return p.verifyKeylessAttestations(ctx, st)
	}
	if p.VerifySignature != nil {
		return p.VerifySignature(ctx, st.imageTag)
	}
	key := strings.TrimSpace(p.ArtifactSigningPublicKey)
	if key == "" {
		return fmt.Errorf("NORN_ARTIFACT_SIGNING_PUBLIC_KEY is required")
	}
	command := strings.TrimSpace(p.CosignPath)
	if command == "" {
		if p.productionReleaseLane() {
			return fmt.Errorf("production release verification requires an absolute NORN_COSIGN_PATH")
		}
		command = "cosign"
	}
	if p.productionReleaseLane() && !filepath.IsAbs(command) {
		return fmt.Errorf("production release verification requires an absolute NORN_COSIGN_PATH")
	}
	args := []string{"verify", "--key", key}
	if commit := strings.TrimSpace(st.commitSHA); commit != "" {
		args = append(args, "--annotations", "norn.git.sha="+commit)
	}
	args = append(args, st.imageTag)
	return runArtifactPolicyCommand(ctx, command, args...)
}

func (p *Pipeline) scanArtifactVulnerabilities(ctx context.Context, imageRef string) error {
	if p.ScanArtifact != nil {
		return p.ScanArtifact(ctx, imageRef)
	}
	severities := normalizedArtifactSeverities(p.ArtifactDenySeverities)
	if len(severities) == 0 {
		return fmt.Errorf("NORN_ARTIFACT_DENY_SEVERITIES is required")
	}
	command := strings.TrimSpace(p.TrivyPath)
	if command == "" {
		if p.productionReleaseLane() {
			return fmt.Errorf("production release scanning requires an absolute NORN_TRIVY_PATH")
		}
		command = "trivy"
	}
	if p.productionReleaseLane() && !filepath.IsAbs(command) {
		return fmt.Errorf("production release scanning requires an absolute NORN_TRIVY_PATH")
	}
	if p.ReleaseAttestationTrustMode != "github-private" && p.ReleaseAttestationTrustMode != "norn-signed-private" {
		return runArtifactPolicyCommand(ctx, command, "image", "--quiet", "--scanners", "vuln", "--exit-code", "1", "--severity", strings.Join(severities, ","), "--ignore-unfixed", imageRef)
	}
	env, cleanup, err := p.registryCommandEnv("")
	if err != nil {
		return err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, command, "image", "--quiet", "--scanners", "vuln", "--exit-code", "1", "--severity", strings.Join(severities, ","), "--ignore-unfixed", imageRef)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", boundedPolicyOutput(output, 4096))
	}
	return nil
}

// registryCommandEnv gives registry consumers an isolated Docker config. It
// intentionally never inherits GH_TOKEN, the GitHub App key, or an ambient
// HOME credential store.
func (p *Pipeline) registryCommandEnv(buildxConfig string) ([]string, func(), error) {
	if p.ReleaseAttestationTrustMode != "github-private" && p.ReleaseAttestationTrustMode != "norn-signed-private" {
		return append(os.Environ(), "BUILDX_CONFIG="+buildxConfig), func() {}, nil
	}
	if err := secureOwnerOnlyRegularFile(p.ReleaseRegistryAuthFile); err != nil {
		return nil, nil, fmt.Errorf("private registry auth file is unavailable")
	}
	raw, err := os.ReadFile(p.ReleaseRegistryAuthFile)
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return nil, nil, fmt.Errorf("private registry auth file is unavailable")
	}
	home, err := os.MkdirTemp("", "norn-release-registry-")
	if err != nil {
		return nil, nil, err
	}
	if err := os.Mkdir(filepath.Join(home, ".docker"), 0o700); err != nil {
		_ = os.RemoveAll(home)
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(home, ".docker", "config.json"), raw, 0o600); err != nil {
		_ = os.RemoveAll(home)
		return nil, nil, err
	}
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "DOCKER_CONFIG=" + filepath.Join(home, ".docker"), "BUILDX_CONFIG=" + buildxConfig, "NO_COLOR=1"}, func() { _ = os.RemoveAll(home) }, nil
}

func secureOwnerOnlyRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("not an owner-only regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("not owned by current user")
	}
	return nil
}

func normalizedArtifactSeverities(values []string) []string {
	allowed := map[string]bool{"UNKNOWN": true, "LOW": true, "MEDIUM": true, "HIGH": true, "CRITICAL": true}
	seen := map[string]bool{}
	result := []string{}
	for _, raw := range values {
		value := strings.ToUpper(strings.TrimSpace(raw))
		if !allowed[value] || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func runArtifactPolicyCommand(ctx context.Context, command string, args ...string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	message := boundedPolicyOutput(output, 4096)
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("%s", message)
}

func boundedPolicyOutput(output []byte, limit int) string {
	message := strings.ToValidUTF8(strings.TrimSpace(string(output)), "\uFFFD")
	if limit <= 0 || len(message) <= limit {
		return message
	}
	message = message[:limit]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}
