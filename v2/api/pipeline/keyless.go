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
	"strings"
)

const (
	keylessProvenancePredicate = "https://slsa.dev/provenance/v1"
	keylessSPDXPredicate       = "https://spdx.dev/Document/v2.3"
)

func (p *Pipeline) verifyKeylessAttestations(ctx context.Context, st *state) error {
	if st == nil {
		return fmt.Errorf("keyless admission requires release state")
	}
	digest, ok := keylessImageDigest(st.imageTag)
	c := st.candidate
	trustMode := p.ReleaseAttestationTrustMode
	if trustMode == "" {
		trustMode = "github-public"
	}
	bootstrapSigner := strings.TrimSpace(p.ExternalFleetBootstrapSignerRef) != "" && c.SignerWorkflowRef == p.ExternalFleetBootstrapSignerRef
	if !ok || c.Attestation.Mode != trustMode || c.Attestation.SubjectDigest != digest || c.Attestation.MaterialSHA != st.commitSHA || c.Repository == "" || c.SignerWorkflowRef == "" || !strings.HasSuffix(c.SignerWorkflowRef, "@"+c.SignerWorkflowSHA) || c.Attestation.Issuer != p.ReleaseAttestationIssuer || !containsExact(p.ReleaseAttestationRepositories, c.Repository) || (!containsExact(p.ReleaseAttestationWorkflowRefs, c.SignerWorkflowRef) && !bootstrapSigner) || !p.ReleaseRequireSBOM {
		return fmt.Errorf("keyless admission requires candidate, policy, digest, and source bindings")
	}
	private := c.RepositoryVisibility == "private" || c.RepositoryVisibility == "internal"
	if private {
		switch trustMode {
		case "github-private":
			if c.Attestation.Mode != "github-private" || p.VerifyPrivateKeylessAttestations == nil {
				return fmt.Errorf("private GitHub release attestations are not configured")
			}
			return p.VerifyPrivateKeylessAttestations(ctx, st.imageTag, st.commitSHA, c.SignerWorkflowRef, c)
		case "norn-signed-private":
			if c.Attestation.Mode != "norn-signed-private" || p.VerifyNornPrivateAttestations == nil || st.spec == nil {
				return fmt.Errorf("Norn-signed private release attestations are not configured")
			}
			return p.VerifyNornPrivateAttestations(ctx, st.imageTag, st.commitSHA, st.spec.App, c)
		default:
			return fmt.Errorf("private release attestations are not permitted by the configured trust mode")
		}
	}
	if c.RepositoryVisibility != "" && c.RepositoryVisibility != "public" {
		return fmt.Errorf("release candidate has unsupported repository visibility")
	}
	if trustMode != "github-public" || c.Attestation.Mode != "github-public" {
		return fmt.Errorf("public GitHub release attestations are not permitted by private trust mode")
	}
	if p.VerifyKeylessAttestations != nil {
		return p.VerifyKeylessAttestations(ctx, st.imageTag, st.commitSHA, c.SignerWorkflowRef, c)
	}
	identity := "https://github.com/" + c.SignerWorkflowRef
	base := []string{"--certificate-oidc-issuer", p.ReleaseAttestationIssuer, "--certificate-identity", identity}
	if _, err := p.runArtifactCommand(ctx, p.CosignPath, append(append([]string{"verify"}, base...), "--annotations", "norn.git.sha="+st.commitSHA, st.imageTag)...); err != nil {
		return fmt.Errorf("keyless signature verification failed: %w", err)
	}
	provenance, err := p.runArtifactCommand(ctx, p.CosignPath, append(append([]string{"verify-attestation", "--type", keylessProvenancePredicate}, base...), st.imageTag)...)
	if err != nil {
		return fmt.Errorf("keyless provenance verification failed: %w", err)
	}
	sbom, err := p.runArtifactCommand(ctx, p.CosignPath, append(append([]string{"verify-attestation", "--type", keylessSPDXPredicate}, base...), st.imageTag)...)
	if err != nil {
		return fmt.Errorf("keyless SBOM verification failed: %w", err)
	}
	if err := verifyProvenanceEnvelope(provenance, digest, st.commitSHA, c.Repository, c.Ref); err != nil {
		return fmt.Errorf("SLSA provenance rejected: %w", err)
	}
	return verifySPDXEnvelope(sbom, digest)
}

func (p *Pipeline) runArtifactCommand(ctx context.Context, command string, args ...string) ([]byte, error) {
	if p.RunArtifactCommand != nil {
		return p.RunArtifactCommand(ctx, command, args...)
	}
	command = strings.TrimSpace(command)
	if command == "" {
		command = "cosign"
	}
	output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if len(output) > 4<<20 {
		return nil, fmt.Errorf("keyless verifier output is too large")
	}
	return output, err
}

func keylessImageDigest(image string) (string, bool) {
	i := strings.LastIndex(strings.TrimSpace(image), "@sha256:")
	if i < 0 {
		return "", false
	}
	d := strings.TrimSpace(image)[i+1:]
	return d, len(d) == 71 && strings.HasPrefix(d, "sha256:")
}

type keylessStatement struct {
	Subject []struct {
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
}

func verifiedStatements(output []byte) ([]keylessStatement, error) {
	if len(output) == 0 || len(output) > 4<<20 {
		return nil, errors.New("cosign returned no DSSE statements")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	var raw []json.RawMessage
	for {
		var value json.RawMessage
		err := decoder.Decode(&value)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("cosign output is not a valid JSON stream")
		}
		if len(value) > 0 && value[0] == '[' {
			var array []json.RawMessage
			if json.Unmarshal(value, &array) != nil {
				return nil, errors.New("cosign output array is invalid")
			}
			raw = append(raw, array...)
		} else {
			raw = append(raw, value)
		}
	}
	if len(raw) == 0 {
		return nil, errors.New("cosign returned no DSSE statements")
	}
	out := make([]keylessStatement, 0, len(raw))
	for _, value := range raw {
		var envelope struct {
			Payload string `json:"payload"`
		}
		if json.Unmarshal(value, &envelope) != nil {
			return nil, errors.New("cosign envelope is invalid")
		}
		payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
		if err != nil {
			return nil, errors.New("DSSE payload is not base64")
		}
		var statement keylessStatement
		if json.Unmarshal(payload, &statement) != nil {
			return nil, errors.New("DSSE payload is not an in-toto statement")
		}
		out = append(out, statement)
	}
	return out, nil
}

func verifyProvenanceEnvelope(output []byte, digest, sourceSHA, repository, ref string) error {
	statements, err := verifiedStatements(output)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if statement.PredicateType != keylessProvenancePredicate || !statementHasDigest(statement, digest) {
			continue
		}
		var predicate struct {
			BuildDefinition struct {
				ResolvedDependencies []material `json:"resolvedDependencies"`
			} `json:"buildDefinition"`
			Materials []material `json:"materials"`
		}
		if json.Unmarshal(statement.Predicate, &predicate) != nil {
			continue
		}
		for _, item := range append(predicate.Materials, predicate.BuildDefinition.ResolvedDependencies...) {
			if repositoryMatches(item.URI, repository, ref) && item.Digest["gitCommit"] == sourceSHA {
				return nil
			}
		}
	}
	return errors.New("no matching source material")
}

type material struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest"`
}

func verifySPDXEnvelope(output []byte, digest string) error {
	statements, err := verifiedStatements(output)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if statement.PredicateType != keylessSPDXPredicate || !statementHasDigest(statement, digest) {
			continue
		}
		var body struct {
			SPDXVersion string `json:"spdxVersion"`
			SPDXID      string `json:"SPDXID"`
		}
		if json.Unmarshal(statement.Predicate, &body) == nil && body.SPDXVersion == "SPDX-2.3" && body.SPDXID == "SPDXRef-DOCUMENT" {
			return nil
		}
	}
	return errors.New("no matching SPDX statement")
}

func statementHasDigest(statement keylessStatement, digest string) bool {
	for _, subject := range statement.Subject {
		if subject.Digest["sha256"] == strings.TrimPrefix(digest, "sha256:") {
			return true
		}
	}
	return false
}

func repositoryMatches(raw, repository, ref string) bool {
	raw = strings.TrimSuffix(strings.TrimSuffix(raw, "@"+ref), ".git")
	raw = strings.TrimPrefix(raw, "git+")
	raw = strings.TrimPrefix(raw, "git@github.com:")
	if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() == "github.com" {
		return strings.TrimPrefix(parsed.Path, "/") == repository
	}
	return strings.TrimPrefix(raw, "https://github.com/") == repository
}

func containsExact(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
