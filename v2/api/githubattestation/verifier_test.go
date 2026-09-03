package githubattestation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/model"
)

func privateCandidate() model.ReleaseCandidate {
	sourceSHA := strings.Repeat("a", 40)
	workflowSHA := strings.Repeat("c", 40)
	signerSHA := strings.Repeat("d", 40)
	digest := "sha256:" + strings.Repeat("b", 64)
	return model.ReleaseCandidate{
		Provider: "github-actions", Repository: "private-org/app", RepositoryID: "11", OwnerID: "22",
		RepositoryVisibility: "private", RunID: "33", RunAttempt: "2", Ref: "refs/heads/main",
		WorkflowRef: "private-org/app/.github/workflows/caller.yml@" + workflowSHA, WorkflowSHA: workflowSHA,
		SignerWorkflowRef: "trusted-org/release/.github/workflows/release.yml@" + signerSHA, SignerWorkflowSHA: signerSHA,
		Attestation: model.ReleaseAttestationIdentity{Mode: "github-private", Issuer: githubOIDCIssuer, SubjectDigest: digest, MaterialSHA: sourceSHA},
	}
}

func verifiedOutput(t *testing.T, predicate string, signerURI string) []byte {
	t.Helper()
	candidate := privateCandidate()
	predicateBody := map[string]any{"spdxVersion": "SPDX-2.3", "SPDXID": "SPDXRef-DOCUMENT", "name": "app", "documentNamespace": "https://example.test/sbom", "creationInfo": map[string]any{"created": "2026-01-01T00:00:00Z"}}
	if predicate == provenancePredicate {
		predicateBody = map[string]any{
			"buildDefinition": map[string]any{
				"resolvedDependencies": []any{map[string]any{"uri": "git+https://github.com/private-org/app@refs/heads/main", "digest": map[string]string{"gitCommit": candidate.Attestation.MaterialSHA}}},
				"internalParameters":   map[string]any{"github": map[string]string{"repository_id": candidate.RepositoryID, "repository_owner_id": candidate.OwnerID, "runner_environment": "github-hosted"}},
			},
			"runDetails": map[string]any{"metadata": map[string]string{"invocationId": "https://github.com/private-org/app/actions/runs/33/attempts/2"}},
		}
	}
	value := []any{map[string]any{"verificationResult": map[string]any{
		"statement": map[string]any{"subject": []any{map[string]any{"digest": map[string]string{"sha256": strings.TrimPrefix(candidate.Attestation.SubjectDigest, "sha256:")}}}, "predicateType": predicate, "predicate": predicateBody},
		"signature": map[string]any{"certificate": map[string]string{
			"issuer": githubOIDCIssuer, "githubWorkflowSHA": candidate.Attestation.MaterialSHA,
			"githubWorkflowRepository": candidate.Repository, "githubWorkflowRef": candidate.Ref,
			"buildSignerURI": signerURI, "buildSignerDigest": candidate.SignerWorkflowSHA,
			"buildConfigURI": "https://github.com/" + candidate.WorkflowRef, "buildConfigDigest": candidate.WorkflowSHA,
			"runnerEnvironment":   "github-hosted",
			"sourceRepositoryURI": "git+https://github.com/private-org/app", "sourceRepositoryDigest": candidate.Attestation.MaterialSHA,
			"sourceRepositoryIdentifier": candidate.RepositoryID, "sourceRepositoryOwnerIdentifier": candidate.OwnerID,
			"sourceRepositoryVisibilityAtSigning": "private", "runInvocationURI": "https://github.com/private-org/app/actions/runs/33/attempts/2",
		}},
		"verifiedTimestamps": []any{map[string]string{"uri": "https://timestamp.example.test"}},
	}}}
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestVerifyOutputBindsCentralReusableWorkflowExactly(t *testing.T) {
	candidate := privateCandidate()
	signer, _, _ := strings.Cut(candidate.SignerWorkflowRef, "@")
	exact := "https://github.com/" + candidate.SignerWorkflowRef
	for _, predicate := range []string{provenancePredicate, spdxPredicate} {
		if err := verifyOutput(verifiedOutput(t, predicate, exact), predicate, candidate.Attestation.SubjectDigest, candidate.Attestation.MaterialSHA, candidate, signer); err != nil {
			t.Fatalf("%s rejected: %v", predicate, err)
		}
	}
	// gh's signer-workflow option is a prefix regex; Norn's parsed certificate
	// binding must reject the adjacent workflow that that option could match.
	prefixConfusion := "https://github.com/" + candidate.SignerWorkflowRef + "-attacker"
	if err := verifyOutput(verifiedOutput(t, provenancePredicate, prefixConfusion), provenancePredicate, candidate.Attestation.SubjectDigest, candidate.Attestation.MaterialSHA, candidate, signer); err == nil {
		t.Fatal("prefix-confused signer identity accepted")
	}
}

func TestValidateConfigRejectsSymlinkAndInsecureRegistryCredential(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	registry := filepath.Join(dir, "registry.json")
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(key, []byte("not parsed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte(`{"auths":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gh, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{AppID: "1", InstallationID: 2, PrivateKeyFile: key, RegistryAuthFile: registry, APIBaseURL: githubAPI, GHPath: gh}
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("group-readable registry credential accepted")
	}
	if err := os.Chmod(registry, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "key-link")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	cfg.PrivateKeyFile = link
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("symlinked private key accepted")
	}
}

func TestInstallationTokenResponseRequiresExactReadOnlyRepositoryGrant(t *testing.T) {
	ok := installationTokenResponse{Permissions: map[string]string{"attestations": "read", "metadata": "read"}, RepositorySelection: "selected"}
	ok.Repositories = append(ok.Repositories, struct {
		ID       json.Number `json:"id"`
		FullName string      `json:"full_name"`
	}{ID: json.Number("11"), FullName: "private-org/app"})
	if !ok.matches("private-org/app", "11") {
		t.Fatal("exact attestation grant rejected")
	}
	for name, response := range map[string]installationTokenResponse{
		"missing permissions": {RepositorySelection: "selected", Repositories: ok.Repositories},
		"extra permission":    {Permissions: map[string]string{"attestations": "read", "contents": "read"}, RepositorySelection: "selected", Repositories: ok.Repositories},
		"all repositories":    {Permissions: ok.Permissions, RepositorySelection: "all", Repositories: ok.Repositories},
		"wrong repository":    {Permissions: ok.Permissions, RepositorySelection: "selected"},
	} {
		t.Run(name, func(t *testing.T) {
			if response.matches("private-org/app", "11") {
				t.Fatal("overbroad or wrong installation token accepted")
			}
		})
	}
}
