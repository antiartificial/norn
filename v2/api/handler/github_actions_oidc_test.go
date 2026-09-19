package handler

import (
	"strings"
	"testing"

	"norn/v2/api/config"
)

func TestGitHubActionsFleetPolicyBindsNumericRepositoryIdentityAndIntent(t *testing.T) {
	h := &Handler{cfg: &config.Config{GitHubActionsAllowedRefs: []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsFleetAllowedRepository: "acme/norn-fleet@101@202", GitHubActionsFleetAllowedEnvironments: []string{"production"}, GitHubActionsFleetAllowedIntents: []string{"apply", "recover"}, GitHubActionsFleetAllowedWorkflowRefs: []string{"acme/norn-fleet/.github/workflows/apply.yml@" + strings.Repeat("a", 40)}}}
	claims := &githubActionsClaims{Repository: "acme/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202", Environment: "production", Ref: "refs/heads/main", EventName: "push", WorkflowRef: "acme/norn-fleet/.github/workflows/apply.yml@" + strings.Repeat("a", 40), WorkflowSHA: strings.Repeat("a", 40), SHA: strings.Repeat("b", 40), RefProtected: "true"}
	ci, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "apply"})
	if err != nil || ci.Intent != "apply" {
		t.Fatalf("valid fleet exchange=%v ci=%+v", err, ci)
	}
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
	h := &Handler{cfg: &config.Config{GitHubActionsDefaultBranch: "main", GitHubActionsAllowedRefs: []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsAllowedRepositories: []string{"acme/widgets@101@202"}, GitHubActionsAllowedApps: []string{"widgets"}, GitHubActionsAllowedEnvironments: []string{"staging"}, GitHubActionsAllowedWorkflowRefs: []string{"acme/release/.github/workflows/release.yml@" + workflowSHA}}}
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

func TestGitHubActionsReleaseLaneMatrixRejectsCrossLaneIdentity(t *testing.T) {
	workflowSHA := strings.Repeat("a", 40)
	h := &Handler{cfg: &config.Config{GitHubActionsDefaultBranch: "main", GitHubActionsAllowedRefs: []string{"refs/heads/main", "refs/tags/v*"}, GitHubActionsAllowedEvents: []string{"push", "workflow_dispatch"}, GitHubActionsAllowedRepositories: []string{"acme/widgets@101@202"}, GitHubActionsAllowedApps: []string{"widgets"}, GitHubActionsAllowedEnvironments: []string{"staging", "production"}, GitHubActionsAllowedWorkflowRefs: []string{"acme/release/.github/workflows/release.yml@" + workflowSHA}}}
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
	h := &Handler{cfg: &config.Config{GitHubActionsDefaultBranch: "trunk", GitHubActionsAllowedRefs: []string{"refs/heads/trunk", "refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push"}, GitHubActionsAllowedRepositories: []string{"acme/widgets@101@202"}, GitHubActionsAllowedApps: []string{"widgets"}, GitHubActionsAllowedEnvironments: []string{"staging"}, GitHubActionsAllowedWorkflowRefs: []string{"acme/release/.github/workflows/release.yml@" + workflowSHA}}}
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
