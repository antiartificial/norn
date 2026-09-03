package handler

import (
	"strings"
	"testing"

	"norn/v2/api/config"
)

func TestGitHubOIDCNumericIdentityClaims(t *testing.T) {
	for _, value := range []string{"1", "101", "999999999999999999999999"} {
		if !validGitHubNumericID(value) {
			t.Fatalf("numeric GitHub identity %q rejected", value)
		}
	}
	for _, value := range []string{"", "0", "-1", "repo-101", "1.5"} {
		if validGitHubNumericID(value) {
			t.Fatalf("non-numeric GitHub identity %q accepted", value)
		}
	}
}

func TestGitHubActionsFleetPolicyBindsNumericRepositoryIdentityAndIntent(t *testing.T) {
	h := &Handler{cfg: &config.Config{GitHubActionsAllowedRefs: []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsFleetAllowedRepository: "acme/norn-fleet@101@202", GitHubActionsFleetAllowedEnvironments: []string{"production"}, GitHubActionsFleetAllowedIntents: []string{"apply", "recover"}, GitHubActionsFleetAllowedWorkflowRefs: []string{"acme/norn-fleet/.github/workflows/apply.yml@" + strings.Repeat("a", 40)}}}
	claims := &githubActionsClaims{Repository: "acme/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202", Environment: "production", Ref: "refs/heads/main", EventName: "push", WorkflowRef: "acme/norn-fleet/.github/workflows/apply.yml@refs/heads/main", WorkflowSHA: strings.Repeat("a", 40), SHA: strings.Repeat("b", 40), RefProtected: "true"}
	ci, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "apply"})
	if err != nil || ci.Intent != "apply" {
		t.Fatalf("valid fleet exchange=%v ci=%+v", err, ci)
	}
	claims.WorkflowSHA = strings.Repeat("b", 40)
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "apply"}); err == nil {
		t.Fatal("Fleet workflow SHA outside the pinned configuration was accepted")
	}
	claims.WorkflowSHA = strings.Repeat("a", 40)
	claims.WorkflowRef = "acme/norn-fleet/.github/workflows/other.yml@refs/heads/main"
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "apply"}); err == nil {
		t.Fatal("Fleet workflow path outside the pinned configuration was accepted")
	}
	claims.WorkflowRef = "acme/norn-fleet/.github/workflows/apply.yml@refs/heads/main"
	claims.RepositoryID = "other"
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "apply"}); err == nil {
		t.Fatal("repository rename/reuse identity accepted")
	}
	claims.RepositoryID = "101"
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "plan"}); err == nil {
		t.Fatal("unallowlisted fleet intent accepted")
	}
}

func TestGitHubActionsReleasePolicyRequiresPublicRepository(t *testing.T) {
	workflowSHA := strings.Repeat("a", 40)
	h := &Handler{cfg: &config.Config{GitHubActionsDefaultBranch: "main", GitHubActionsAllowedRefs: []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsReleaseBindings: []string{"widgets=acme/widgets@101@202"}, GitHubActionsAllowedEnvironments: []string{"staging"}, GitHubActionsAllowedWorkflowRefs: []string{"acme/release/.github/workflows/release.yml@" + workflowSHA}}}
	claims := &githubActionsClaims{Repository: "acme/widgets", RepositoryID: "101", RepositoryOwnerID: "202", RepositoryVisibility: "private", Environment: "staging", Ref: "refs/heads/main", EventName: "push", RefProtected: "true", JobWorkflowRef: "acme/release/.github/workflows/release.yml@" + workflowSHA, JobWorkflowSHA: workflowSHA}
	request := githubActionsExchangeRequest{Scope: ScopeReleaseStage, App: "widgets", Environment: "staging", Intent: "stage"}
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err == nil {
		t.Fatal("private release repository accepted without a private attestation verifier")
	}
	claims.RepositoryVisibility = "public"
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err != nil {
		t.Fatalf("public release repository rejected: %v", err)
	}
}

func TestGitHubActionsPrivateAttestationScopeIsNornPolicyBoundAndStagingOnly(t *testing.T) {
	workflowSHA := strings.Repeat("a", 40)
	h := &Handler{cfg: &config.Config{ReleaseAttestationTrustMode: "norn-signed-private", GitHubActionsDefaultBranch: "main", GitHubActionsAllowedRefs: []string{"refs/heads/main", "refs/tags/v*"}, GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsReleaseBindings: []string{"widgets=personal-owner/widgets@101@202"}, GitHubActionsAllowedEnvironments: []string{"staging", "production"}, GitHubActionsAllowedWorkflowRefs: []string{"personal-owner/norn/.github/workflows/norn-app-release.yml@" + workflowSHA}}}
	claims := &githubActionsClaims{Repository: "personal-owner/widgets", RepositoryID: "101", RepositoryOwnerID: "202", RepositoryVisibility: "private", Environment: "staging", Ref: "refs/heads/main", RefType: "branch", EventName: "push", RefProtected: "true", JobWorkflowRef: "personal-owner/norn/.github/workflows/norn-app-release.yml@" + workflowSHA, JobWorkflowSHA: workflowSHA}
	request := githubActionsExchangeRequest{Scope: ScopeReleaseAttest, App: "widgets", Environment: "staging", Intent: "attest"}
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err != nil {
		t.Fatalf("ordinary private repository attestation identity rejected: %v", err)
	}
	if got := releaseAttestationMode(claims.RepositoryVisibility, h.cfg.ReleaseAttestationTrustMode); got != "norn-signed-private" {
		t.Fatalf("server-selected trust mode = %q", got)
	}
	claims.Environment, claims.Ref, claims.RefType = "production", "refs/tags/v1.0.0", "tag"
	request.Environment = "production"
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err == nil {
		t.Fatal("production tag received private attestation signing authority")
	}
}

func TestGitHubActionsReleaseLaneMatrixRejectsCrossLaneIdentity(t *testing.T) {
	workflowSHA := strings.Repeat("a", 40)
	h := &Handler{cfg: &config.Config{GitHubActionsDefaultBranch: "main", GitHubActionsAllowedRefs: []string{"refs/heads/main", "refs/tags/v*"}, GitHubActionsAllowedEvents: []string{"push", "workflow_dispatch"}, GitHubActionsReleaseBindings: []string{"widgets=acme/widgets@101@202"}, GitHubActionsAllowedEnvironments: []string{"staging", "production"}, GitHubActionsAllowedWorkflowRefs: []string{"acme/release/.github/workflows/release.yml@" + workflowSHA}}}
	claims := &githubActionsClaims{Repository: "acme/widgets", RepositoryID: "101", RepositoryOwnerID: "202", RepositoryVisibility: "public", Environment: "production", Ref: "refs/tags/v1.2.3", RefType: "tag", EventName: "push", RefProtected: "true", JobWorkflowRef: "acme/release/.github/workflows/release.yml@" + workflowSHA, JobWorkflowSHA: workflowSHA}
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeReleasePromote, App: "widgets", Environment: "production", Intent: "promote"}); err != nil {
		t.Fatalf("valid promotion lane rejected: %v", err)
	}
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeReleaseStage, App: "widgets", Environment: "production", Intent: "stage"}); err == nil {
		t.Fatal("production tag exchanged as stage lane")
	}
	claims.Environment, claims.Ref, claims.RefType, claims.EventName = "production", "refs/heads/main", "branch", "workflow_dispatch"
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeReleaseRollback, App: "widgets", Environment: "production", Intent: "rollback"}); err != nil {
		t.Fatalf("valid rollback lane rejected: %v", err)
	}
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeReleasePromote, App: "widgets", Environment: "production", Intent: "promote"}); err == nil {
		t.Fatal("rollback workflow exchanged as promotion lane")
	}
}

func TestGitHubActionsReleaseLaneUsesConfiguredDefaultBranch(t *testing.T) {
	workflowSHA := strings.Repeat("a", 40)
	h := &Handler{cfg: &config.Config{GitHubActionsDefaultBranch: "trunk", GitHubActionsAllowedRefs: []string{"refs/heads/trunk", "refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsReleaseBindings: []string{"widgets=acme/widgets@101@202"}, GitHubActionsAllowedEnvironments: []string{"staging"}, GitHubActionsAllowedWorkflowRefs: []string{"acme/release/.github/workflows/release.yml@" + workflowSHA}}}
	claims := &githubActionsClaims{Repository: "acme/widgets", RepositoryID: "101", RepositoryOwnerID: "202", RepositoryVisibility: "public", Environment: "staging", Ref: "refs/heads/trunk", EventName: "push", RefProtected: "true", JobWorkflowRef: "acme/release/.github/workflows/release.yml@" + workflowSHA, JobWorkflowSHA: workflowSHA}
	request := githubActionsExchangeRequest{Scope: ScopeReleaseStage, App: "widgets", Environment: "staging", Intent: "stage"}
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err != nil {
		t.Fatalf("configured protected branch rejected: %v", err)
	}
	claims.Ref = "refs/heads/main"
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err == nil {
		t.Fatal("hard-coded main branch was accepted")
	}
}

func TestGitHubActionsReleaseBindingPreventsAppRepositoryCrossProduct(t *testing.T) {
	sha := strings.Repeat("a", 40)
	h := &Handler{cfg: &config.Config{
		GitHubActionsDefaultBranch: "main", GitHubActionsAllowedRefs: []string{"refs/heads/main"},
		GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsAllowedEnvironments: []string{"staging"},
		GitHubActionsAllowedWorkflowRefs: []string{"acme/release/.github/workflows/release.yml@" + sha},
		GitHubActionsReleaseBindings:     []string{"app-a=acme/repo-a@101@201", "app-b=acme/repo-b@102@202"},
	}}
	claims := &githubActionsClaims{Repository: "acme/repo-a", RepositoryID: "101", RepositoryOwnerID: "201", RepositoryVisibility: "public", Environment: "staging", Ref: "refs/heads/main", EventName: "push", RefProtected: "true", JobWorkflowRef: "acme/release/.github/workflows/release.yml@" + sha, JobWorkflowSHA: sha}
	request := githubActionsExchangeRequest{Scope: ScopeReleaseStage, App: "app-a", Environment: "staging", Intent: "stage"}
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err != nil {
		t.Fatalf("exact app/repository binding rejected: %v", err)
	}
	request.App = "app-b"
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err == nil {
		t.Fatal("repo-a was permitted to mint an app-b token")
	}
	claims.Repository, claims.RepositoryID, claims.RepositoryOwnerID = "acme/repo-b", "102", "202"
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err != nil {
		t.Fatalf("exact app-b/repo-b binding rejected: %v", err)
	}

	// Legacy independent lists are parsed for configuration compatibility, but
	// must not recreate a release app/repository cross-product policy.
	h.cfg.GitHubActionsReleaseBindings = nil
	h.cfg.GitHubActionsAllowedRepositories = []string{"acme/repo-b@102@202"}
	h.cfg.GitHubActionsAllowedApps = []string{"app-b"}
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err == nil {
		t.Fatal("legacy independent release allowlists authorized a release identity")
	}
}
