package pipeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"norn/v2/api/model"
)

func TestKeylessAdmissionRunsSignatureAndParsesBoundAttestations(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	sha := strings.Repeat("b", 40)
	provenance := envelope(t, map[string]any{
		"subject":       []any{map[string]any{"digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}},
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate":     map[string]any{"buildDefinition": map[string]any{"resolvedDependencies": []any{map[string]any{"uri": "git+https://github.com/acme/widgets@refs/heads/main", "digest": map[string]string{"gitCommit": sha}}}}},
	})
	spdx := envelope(t, map[string]any{"subject": []any{map[string]any{"digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}}, "predicateType": "https://spdx.dev/Document/v2.3", "predicate": map[string]any{"spdxVersion": "SPDX-2.3", "SPDXID": "SPDXRef-DOCUMENT"}})
	var calls [][]string
	p := &Pipeline{ReleaseAdmissionMode: "keyless", ReleaseAttestationIssuer: "https://token.actions.githubusercontent.com", ReleaseAttestationRepositories: []string{"acme/widgets"}, ReleaseAttestationWorkflowRefs: []string{"acme/widgets/.github/workflows/release.yml@" + sha}, ReleaseRequireSBOM: true,
		RunArtifactCommand: func(_ context.Context, command string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{command}, args...))
			if args[0] == "verify" {
				return []byte("[]"), nil
			}
			if args[2] == "https://slsa.dev/provenance/v1" {
				return provenance, nil
			}
			return spdx, nil
		},
	}
	st := &state{imageTag: "registry.example.test/acme/widgets@" + digest, commitSHA: sha, candidate: model.ReleaseCandidate{Repository: "acme/widgets", Ref: "refs/heads/main", WorkflowRef: "acme/widgets/.github/workflows/release.yml@refs/heads/main", SignerWorkflowRef: "acme/widgets/.github/workflows/release.yml@" + sha, SignerWorkflowSHA: sha, Attestation: model.ReleaseAttestationIdentity{Issuer: "https://token.actions.githubusercontent.com", ProvenanceURI: "https://example.test/provenance", SBOMURI: "https://example.test/sbom", SubjectDigest: digest, MaterialSHA: sha}}}
	if err := p.verifyKeylessAttestations(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[0][1] != "verify" || !containsArg(calls[0], "norn.git.sha="+sha) || calls[1][1] != "verify-attestation" || calls[2][3] != "https://spdx.dev/Document/v2.3" {
		t.Fatalf("unexpected cosign calls: %#v", calls)
	}
}

func TestKeylessAttestationRejectsWrongSubjectOrMaterial(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	sha := strings.Repeat("b", 40)
	wrongSubject := envelope(t, map[string]any{"subject": []any{map[string]any{"digest": map[string]string{"sha256": strings.Repeat("c", 64)}}}, "predicateType": "https://spdx.dev/Document/v2.3", "predicate": map[string]any{"spdxVersion": "SPDX-2.3", "SPDXID": "SPDXRef-DOCUMENT"}})
	if err := verifySPDXEnvelope(wrongSubject, digest); err == nil {
		t.Fatal("wrong subject accepted")
	}
	wrongMaterial := envelope(t, map[string]any{"subject": []any{map[string]any{"digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}}, "predicateType": "https://slsa.dev/provenance/v1", "predicate": map[string]any{"materials": []any{map[string]any{"uri": "https://github.com/acme/widgets", "digest": map[string]string{"gitCommit": strings.Repeat("c", 40)}}}}})
	if err := verifyProvenanceEnvelope(wrongMaterial, digest, sha, "acme/widgets", "refs/heads/main"); err == nil {
		t.Fatal("wrong material accepted")
	}
}

func TestVerifiedStatementsAcceptsCosignJSONStream(t *testing.T) {
	statement := map[string]any{"subject": []any{map[string]any{"digest": map[string]string{"sha256": strings.Repeat("a", 64)}}}, "predicateType": "https://spdx.dev/Document/v2.3", "predicate": map[string]any{"spdxVersion": "SPDX-2.3", "SPDXID": "SPDXRef-DOCUMENT"}}
	array := envelope(t, statement)
	var values []map[string]string
	if err := json.Unmarshal(array, &values); err != nil {
		t.Fatal(err)
	}
	stream, err := json.Marshal(values[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifiedStatements(append(append(stream, '\n'), stream...)); err != nil {
		t.Fatalf("Cosign JSON object stream rejected: %v", err)
	}
}

func TestRepositoryMatchesOnlyExactGitHubOwnerAndRepository(t *testing.T) {
	if !repositoryMatches("git@github.com:acme/widgets.git", "acme/widgets", "refs/heads/main") {
		t.Fatal("expected canonical GitHub URL to match")
	}
	if repositoryMatches("https://evil.example/github.com/acme/widgets", "acme/widgets", "refs/heads/main") || repositoryMatches("https://github.com/acme/widgets-extra", "acme/widgets", "refs/heads/main") {
		t.Fatal("deceptive repository URI matched")
	}
}

func envelope(t *testing.T, statement map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal([]map[string]string{{"payload": base64.StdEncoding.EncodeToString(payload)}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}
