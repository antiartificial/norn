package pipeline

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

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
	if st.spec.Build == nil {
		blockers = append(blockers, "build configuration is required to avoid mutable latest images")
	} else if !model.IsContentAddressedImage(st.spec.Build.Image) {
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

// artifactAdmission runs after the build. Production preflights intentionally
// do not push an image, while a mutating deploy must submit the exact manifest
// digest reported by the registry-backed build.
func (p *Pipeline) artifactAdmission(ctx context.Context, st *state, _ *saga.Saga) error {
	if !p.Production || st.preflight {
		return nil
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
	cmd := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "inspect", imageRef)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 4096 {
			message = message[:4096]
		}
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("%s", message)
	}
	return nil
}

func (p *Pipeline) verifyArtifactSignature(ctx context.Context, st *state) error {
	if p.VerifySignature != nil {
		return p.VerifySignature(ctx, st.imageTag)
	}
	key := strings.TrimSpace(p.ArtifactSigningPublicKey)
	if key == "" {
		return fmt.Errorf("NORN_ARTIFACT_SIGNING_PUBLIC_KEY is required")
	}
	command := strings.TrimSpace(p.CosignPath)
	if command == "" {
		command = "cosign"
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
		command = "trivy"
	}
	return runArtifactPolicyCommand(ctx, command, "image", "--quiet", "--scanners", "vuln", "--exit-code", "1", "--severity", strings.Join(severities, ","), "--ignore-unfixed", imageRef)
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
	message := strings.TrimSpace(string(output))
	if len(message) > 4096 {
		message = message[:4096]
	}
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("%s", message)
}
