package pipeline

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"norn/v2/api/model"
	"norn/v2/api/saga"
)

// admission is intentionally a deploy stage after source resolution. A
// production profile must verify the actual source provenance, not only the
// requested ref or repository declaration.
func (p *Pipeline) admission(_ context.Context, st *state, _ *saga.Saga) error {
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
	} else if !st.artifactBound && !model.IsContentAddressedImage(st.spec.Build.Image) {
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
	if !p.Production || (st.preflight && !st.artifactBound) {
		return nil
	}
	if !model.IsContentAddressedImage(st.imageTag) {
		return fmt.Errorf("production artifact admission blocked: image must be pinned by sha256 OCI digest")
	}
	if st.artifactBound && st.spec != nil {
		expected := authorizedArtifactRepository(p.RegistryURL, st.spec)
		actual := contentAddressedRepository(st.imageTag)
		if expected == "" || actual != expected {
			return fmt.Errorf("production artifact admission blocked: bound artifact repository %q does not match authorized repository %q", actual, expected)
		}
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

// VerifyReleaseArtifact reruns production admission for an already-published
// immutable rollback target. It performs no build or push.
func (p *Pipeline) VerifyReleaseArtifact(ctx context.Context, sourceSHA, artifact string, candidate model.ReleaseCandidate) error {
	return p.artifactAdmission(ctx, &state{commitSHA: sourceSHA, imageTag: artifact, artifactBound: true, candidate: candidate}, nil)
}

func authorizedArtifactRepository(registryURL string, spec *model.InfraSpec) string {
	if spec == nil {
		return ""
	}
	if spec.Build != nil {
		if repository := contentAddressedRepository(strings.TrimSpace(spec.Build.Image)); repository != "" {
			return repository
		}
	}
	registryURL = strings.TrimRight(strings.TrimSpace(registryURL), "/")
	if registryURL == "" || strings.TrimSpace(spec.App) == "" {
		return ""
	}
	return registryURL + "/" + strings.TrimSpace(spec.App)
}

func contentAddressedRepository(ref string) string {
	const marker = "@sha256:"
	ref = strings.TrimSpace(ref)
	if !model.IsContentAddressedImage(ref) {
		return ""
	}
	return ref[:strings.LastIndex(ref, marker)]
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
	if p.ReleaseAdmissionMode == "keyless" {
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
		command = "cosign"
	}
	args := []string{"verify", "--key", key}
	if commit := strings.TrimSpace(st.commitSHA); commit != "" {
		args = append(args, "--annotations", "norn.git.sha="+commit)
	}
	args = append(args, st.imageTag)
	return runArtifactPolicyCommand(ctx, command, args...)
}

// verifyKeylessAttestations requires a workload-identity signature plus both
// provenance and SBOM attestations for the submitted digest. Cosign verifies
// the Fulcio issuer/certificate identity; Norn then decodes the verified DSSE
// statements and binds their structured subjects/materials to this request.
func (p *Pipeline) verifyKeylessAttestations(ctx context.Context, st *state) error {
	digest, ok := imageDigest(st.imageTag)
	if !ok || st.candidate.Attestation.SubjectDigest != digest || st.candidate.Attestation.MaterialSHA != st.commitSHA || st.candidate.Repository == "" || st.candidate.WorkflowRef == "" || st.candidate.SignerWorkflowRef == "" || !strings.HasSuffix(st.candidate.SignerWorkflowRef, "@"+st.candidate.SignerWorkflowSHA) || st.candidate.Attestation.Issuer != p.ReleaseAttestationIssuer || st.candidate.Attestation.ProvenanceURI == "" || st.candidate.Attestation.SBOMURI == "" {
		return fmt.Errorf("keyless admission requires a candidate bound to this digest and source SHA")
	}
	if p.ReleaseAttestationIssuer == "" || !containsExact(p.ReleaseAttestationRepositories, st.candidate.Repository) || !containsExact(p.ReleaseAttestationWorkflowRefs, st.candidate.SignerWorkflowRef) || !p.ReleaseRequireSBOM {
		return fmt.Errorf("keyless release attestation policy is incomplete")
	}
	if p.VerifyKeylessAttestations != nil {
		return p.VerifyKeylessAttestations(ctx, st.imageTag, st.commitSHA, st.candidate.SignerWorkflowRef, st.candidate)
	}
	command := strings.TrimSpace(p.CosignPath)
	if command == "" {
		command = "cosign"
	}
	identity := "https://github.com/" + st.candidate.SignerWorkflowRef
	baseArgs := []string{"--certificate-oidc-issuer", p.ReleaseAttestationIssuer, "--certificate-identity", identity}
	if output, err := p.runArtifactCommand(ctx, command, append(append([]string{"verify"}, baseArgs...), "--annotations", "norn.git.sha="+st.commitSHA, st.imageTag)...); err != nil {
		return artifactPolicyError(err, output)
	}
	outputs := map[string][]byte{}
	for _, typ := range []string{"https://slsa.dev/provenance/v1", "https://spdx.dev/Document/v2.3"} {
		output, err := p.runArtifactCommand(ctx, command, append(append([]string{"verify-attestation", "--type", typ}, baseArgs...), st.imageTag)...)
		if err != nil {
			return artifactPolicyError(err, output)
		}
		outputs[typ] = output
	}
	if err := verifyProvenanceEnvelope(outputs["https://slsa.dev/provenance/v1"], digest, st.commitSHA, st.candidate.Repository, st.candidate.Ref); err != nil {
		return fmt.Errorf("SLSA provenance rejected: %w", err)
	}
	if err := verifySPDXEnvelope(outputs["https://spdx.dev/Document/v2.3"], digest); err != nil {
		return fmt.Errorf("SPDX SBOM rejected: %w", err)
	}
	return nil
}

func (p *Pipeline) runArtifactCommand(ctx context.Context, command string, args ...string) ([]byte, error) {
	if p.RunArtifactCommand != nil {
		return p.RunArtifactCommand(ctx, command, args...)
	}
	return exec.CommandContext(ctx, command, args...).CombinedOutput()
}

func imageDigest(ref string) (string, bool) {
	marker := "@sha256:"
	index := strings.LastIndex(strings.TrimSpace(ref), marker)
	if index < 0 || index+1 >= len(strings.TrimSpace(ref)) {
		return "", false
	}
	digest := strings.TrimSpace(ref)[index+1:]
	return digest, len(digest) == len("sha256:")+64 && fleetDigestRe.MatchString(digest)
}

var fleetDigestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type inTotoStatement struct {
	Subject []struct {
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
}

type dsseEnvelope struct {
	Payload string `json:"payload"`
}

func verifiedStatements(output []byte) ([]inTotoStatement, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	var results []inTotoStatement
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("cosign output is not JSON: %w", err)
		}
		var envelopes []dsseEnvelope
		if len(raw) > 0 && raw[0] == '[' {
			if err := json.Unmarshal(raw, &envelopes); err != nil {
				return nil, err
			}
		} else {
			var one dsseEnvelope
			if err := json.Unmarshal(raw, &one); err != nil {
				return nil, err
			}
			envelopes = []dsseEnvelope{one}
		}
		for _, envelope := range envelopes {
			payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
			if err != nil {
				payload, err = base64.RawStdEncoding.DecodeString(envelope.Payload)
			}
			if err != nil {
				return nil, fmt.Errorf("DSSE payload is not base64: %w", err)
			}
			var statement inTotoStatement
			if err := json.Unmarshal(payload, &statement); err != nil {
				return nil, fmt.Errorf("DSSE payload is not an in-toto statement: %w", err)
			}
			results = append(results, statement)
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("cosign returned no DSSE statements")
	}
	return results, nil
}

func statementHasDigest(statement inTotoStatement, digest string) bool {
	for _, subject := range statement.Subject {
		if subject.Digest["sha256"] == strings.TrimPrefix(digest, "sha256:") {
			return true
		}
	}
	return false
}

func verifyProvenanceEnvelope(output []byte, digest, sourceSHA, repository, ref string) error {
	statements, err := verifiedStatements(output)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if statement.PredicateType != "https://slsa.dev/provenance/v1" && statement.PredicateType != "https://slsa.dev/provenance/v0.2" {
			continue
		}
		if !statementHasDigest(statement, digest) {
			continue
		}
		var predicate struct {
			BuildDefinition struct {
				ResolvedDependencies []struct {
					URI    string            `json:"uri"`
					Digest map[string]string `json:"digest"`
				} `json:"resolvedDependencies"`
			} `json:"buildDefinition"`
			Materials []struct {
				URI    string            `json:"uri"`
				Digest map[string]string `json:"digest"`
			} `json:"materials"`
		}
		if err := json.Unmarshal(statement.Predicate, &predicate); err != nil {
			continue
		}
		materials := predicate.Materials
		materials = append(materials, predicate.BuildDefinition.ResolvedDependencies...)
		for _, material := range materials {
			if repositoryMatches(material.URI, repository, ref) && materialMatchesSHA(material.Digest, sourceSHA) {
				return nil
			}
		}
	}
	return fmt.Errorf("no statement binds subject %s to source %s in repository %s", digest, sourceSHA, repository)
}

func verifySPDXEnvelope(output []byte, digest string) error {
	statements, err := verifiedStatements(output)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if statement.PredicateType != "https://spdx.dev/Document/v2.3" {
			continue
		}
		if !statementHasDigest(statement, digest) {
			continue
		}
		var spdx struct {
			SPDXVersion string `json:"spdxVersion"`
			SPDXID      string `json:"SPDXID"`
		}
		if json.Unmarshal(statement.Predicate, &spdx) == nil && strings.HasPrefix(spdx.SPDXVersion, "SPDX-") && spdx.SPDXID != "" {
			return nil
		}
	}
	return fmt.Errorf("no SPDX statement binds subject %s", digest)
}

func materialMatchesSHA(digest map[string]string, sourceSHA string) bool {
	for _, key := range []string{"sha1", "gitCommit", "sha256"} {
		if digest[key] == sourceSHA {
			return true
		}
	}
	return false
}

func repositoryMatches(uri, repository, ref string) bool {
	repository = strings.TrimSuffix(strings.TrimSpace(repository), ".git")
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	uri = strings.TrimSpace(uri)
	if strings.HasPrefix(uri, "git+https://") {
		uri = strings.TrimPrefix(uri, "git+")
	}
	if strings.HasPrefix(uri, "git@github.com:") {
		uri = "https://github.com/" + strings.TrimPrefix(uri, "git@github.com:")
	}
	parsed, err := url.Parse(uri)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return false
	}
	path := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	actualRef := ""
	if before, after, ok := strings.Cut(path, "@"); ok {
		path, actualRef = before, after
	}
	return path == repository && (actualRef == "" || actualRef == ref)
}

func containsExact(values []string, expected string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == expected {
			return true
		}
	}
	return false
}
func artifactPolicyError(err error, output []byte) error {
	message := strings.TrimSpace(string(output))
	if len(message) > 4096 {
		message = message[:4096]
	}
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("%s", message)
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
