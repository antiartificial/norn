package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
)

func TestGitHubOIDCExchangeWithoutPostgresIdentityStoreFailsClosed(t *testing.T) {
	h := &Handler{cfg: &config.Config{APIToken: strings.Repeat("k", 40), Environment: "staging"}}
	recorder := httptest.NewRecorder()
	h.ExchangeGitHubActionsOIDC(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/auth/github-actions/exchange", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing PostgreSQL identity store status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestFleetOIDCExchangeWithTypedNilIdentityStoreFailsClosed(t *testing.T) {
	var identities *etcdstore.AuthStore
	recorder := httptest.NewRecorder()
	ExchangeFleetGitHubActionsOIDC(&config.Config{APIToken: strings.Repeat("k", 40), Environment: "staging"}, identities,
		recorder, httptest.NewRequest(http.MethodPost, "/api/v1/auth/github-actions/exchange", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("typed nil Fleet identity store status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

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
	for _, workflowRef := range []string{
		"acme/norn-fleet/.github/workflows/apply.yml@refs/heads/feature",
		"acme/norn-fleet/.github/workflows/apply.yml@refs/tags/v1",
		"acme/norn-fleet/.github/workflows/apply.yml@" + strings.Repeat("a", 40),
	} {
		claims.WorkflowRef = workflowRef
		if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "apply"}); err == nil {
			t.Fatalf("Fleet workflow ref %q outside protected main was accepted", workflowRef)
		}
	}
	claims.WorkflowRef = "acme/norn-fleet/.github/workflows/apply.yml@refs/heads/main"
	claims.RefProtected = "false"
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "apply"}); err == nil {
		t.Fatal("unprotected Fleet ref accepted")
	}
	claims.RefProtected = "true"
	h.cfg.GitHubActionsAllowedRefs = []string{"refs/heads/main", "refs/heads/feature"}
	claims.Ref = "refs/heads/feature"
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "apply"}); err == nil {
		t.Fatal("non-main Fleet source ref accepted despite broader general allowlist")
	}
	claims.Ref = "refs/heads/main"
	claims.RepositoryID = "other"
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "apply"}); err == nil {
		t.Fatal("repository rename/reuse identity accepted")
	}
	claims.RepositoryID = "101"
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "plan"}); err == nil {
		t.Fatal("unallowlisted fleet intent accepted")
	}
}

func TestGitHubActionsFleetAuthorityOnlyAllowsProtectedStagingApplyAndRecovery(t *testing.T) {
	applySHA := strings.Repeat("a", 40)
	recoverSHA := strings.Repeat("b", 40)
	h := &Handler{cfg: &config.Config{
		FleetAuthorityOnly:                    true,
		GitHubActionsAllowedRefs:              []string{"refs/heads/main"},
		GitHubActionsAllowedEvents:            []string{"push", "workflow_dispatch"},
		GitHubActionsFleetAllowedRepository:   "acme/norn-fleet@101@202",
		GitHubActionsFleetAllowedEnvironments: []string{"staging"},
		GitHubActionsFleetAllowedIntents:      []string{"apply", "recover"},
		GitHubActionsFleetAllowedWorkflowRefs: []string{
			"acme/norn-fleet/.github/workflows/apply.yml@" + applySHA,
			"acme/norn-fleet/.github/workflows/recover.yml@" + recoverSHA,
		},
	}}
	claims := &githubActionsClaims{
		Repository: "acme/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202", Environment: "staging",
		Ref: "refs/heads/main", EventName: "push", RefProtected: "true",
		WorkflowRef: "acme/norn-fleet/.github/workflows/apply.yml@refs/heads/main", WorkflowSHA: applySHA,
	}
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "staging", Intent: "apply"}); err != nil {
		t.Fatalf("protected staging apply rejected: %v", err)
	}
	claims.EventName = "workflow_dispatch"
	claims.WorkflowRef = "acme/norn-fleet/.github/workflows/recover.yml@refs/heads/main"
	claims.WorkflowSHA = recoverSHA
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "staging", Intent: "recover"}); err != nil {
		t.Fatalf("protected staging recovery rejected: %v", err)
	}
	claims.Environment = "production"
	if _, err := h.authorizeGitHubActionsClaims(claims, githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "production", Intent: "recover"}); err == nil {
		t.Fatal("production Fleet exchange accepted by staging-only authority")
	}
}

func TestGitHubActionsWorkflowRunOnlyAuthorizesPinnedWorkStagingRecovery(t *testing.T) {
	sha := strings.Repeat("b", 40)
	h := &Handler{cfg: &config.Config{
		FleetAuthorityOnly:                    true,
		GitHubActionsAllowedRefs:              []string{"refs/heads/main"},
		GitHubActionsAllowedEvents:            []string{"push", "workflow_dispatch", "workflow_run"},
		GitHubActionsFleetAllowedRepository:   "acme/norn-fleet@101@202",
		GitHubActionsFleetAllowedEnvironments: []string{"work-staging"},
		GitHubActionsFleetAllowedIntents:      []string{"apply", "recover"},
		GitHubActionsFleetAllowedWorkflowRefs: []string{"acme/norn-fleet/.github/workflows/recover.yml@" + sha},
	}}
	c := &githubActionsClaims{Repository: "acme/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202", Environment: "work-staging", Ref: "refs/heads/main", RefProtected: "true", EventName: "workflow_run", WorkflowRef: "acme/norn-fleet/.github/workflows/recover.yml@refs/heads/main", WorkflowSHA: sha}
	r := githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "staging", Intent: "recover"}
	if _, err := h.authorizeGitHubActionsClaims(c, r); err != nil {
		t.Fatalf("automatic Fleet recovery rejected: %v", err)
	}
	for name, alter := range map[string]func(*githubActionsClaims, *githubActionsExchangeRequest){
		"apply intent": func(_ *githubActionsClaims, r *githubActionsExchangeRequest) { r.Intent = "apply" },
		"release lane": func(_ *githubActionsClaims, r *githubActionsExchangeRequest) {
			r.Scope = ScopeReleaseStage
			r.App = "app"
		},
		"wrong workflow": func(c *githubActionsClaims, _ *githubActionsExchangeRequest) {
			c.WorkflowRef = "acme/norn-fleet/.github/workflows/apply.yml@refs/heads/main"
		},
		"wrong workflow SHA": func(c *githubActionsClaims, _ *githubActionsExchangeRequest) { c.WorkflowSHA = strings.Repeat("a", 40) },
		"unprotected ref":    func(c *githubActionsClaims, _ *githubActionsExchangeRequest) { c.RefProtected = "false" },
		"other environment":  func(c *githubActionsClaims, _ *githubActionsExchangeRequest) { c.Environment = "staging" },
		"other repository":   func(c *githubActionsClaims, _ *githubActionsExchangeRequest) { c.RepositoryID = "999" },
	} {
		t.Run(name, func(t *testing.T) {
			changed, request := *c, r
			alter(&changed, &request)
			if _, err := h.authorizeGitHubActionsClaims(&changed, request); err == nil {
				t.Fatal("workflow_run identity accepted")
			}
		})
	}
}

func TestGitHubActionsFleetAuthorityOnlyMapsExactWorkStagingClaim(t *testing.T) {
	workflowSHA := strings.Repeat("a", 40)
	h := &Handler{cfg: &config.Config{
		FleetAuthorityOnly: true, Environment: "staging",
		GitHubActionsAllowedRefs: []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push"},
		GitHubActionsFleetAllowedRepository:   "acme/norn-fleet@101@202",
		GitHubActionsFleetAllowedEnvironments: []string{"work-staging"},
		GitHubActionsFleetAllowedIntents:      []string{"apply"},
		GitHubActionsFleetAllowedWorkflowRefs: []string{"acme/norn-fleet/.github/workflows/apply.yml@" + workflowSHA},
	}}
	claims := &githubActionsClaims{Repository: "acme/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202", Environment: "work-staging", Ref: "refs/heads/main", EventName: "push", WorkflowRef: "acme/norn-fleet/.github/workflows/apply.yml@refs/heads/main", WorkflowSHA: workflowSHA, RefProtected: "true"}
	request := githubActionsExchangeRequest{Scope: ScopeFleetOperate, Environment: "staging", Intent: "apply"}
	ci, err := h.authorizeGitHubActionsClaims(claims, request)
	if err != nil || ci.Environment != "staging" || ci.GitHubEnvironment != "work-staging" {
		t.Fatalf("work-staging claim mapping: ci=%+v err=%v", ci, err)
	}
	for _, environment := range []string{"staging", "production", "work-staging-extra"} {
		claims.Environment = environment
		if _, err := h.authorizeGitHubActionsClaims(claims, request); err == nil {
			t.Fatalf("GitHub environment %q accepted outside exact work-staging policy", environment)
		}
	}
	claims.Environment = "work-staging"
	request.Environment = "production"
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err == nil {
		t.Fatal("work-staging claim accepted for production control environment")
	}
}

func TestGitHubActionsExternalAdmissionUsesExactFleetApplyRecoveryIdentity(t *testing.T) {
	workflowSHA := strings.Repeat("a", 40)
	h := &Handler{cfg: &config.Config{
		GitHubActionsAllowedRefs: []string{"refs/heads/main"}, GitHubActionsAllowedEvents: []string{"push", "workflow_dispatch"},
		GitHubActionsFleetAllowedRepository: "acme/norn-fleet@101@202", GitHubActionsFleetAllowedEnvironments: []string{"staging"}, GitHubActionsFleetAllowedIntents: []string{"apply", "recover"},
		GitHubActionsFleetAllowedWorkflowRefs: []string{"acme/norn-fleet/.github/workflows/apply.yml@" + workflowSHA},
	}}
	claims := &githubActionsClaims{Repository: "acme/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202", Environment: "staging", Ref: "refs/heads/main", EventName: "push", RefProtected: "true", WorkflowRef: "acme/norn-fleet/.github/workflows/apply.yml@refs/heads/main", WorkflowSHA: workflowSHA}
	request := githubActionsExchangeRequest{Scope: ScopeFleetExternalAdmission, App: "hello-norn-mysql", Environment: "staging", Intent: "apply"}
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err != nil {
		t.Fatalf("exact external Fleet apply identity rejected: %v", err)
	}
	request.Intent = "plan"
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err == nil {
		t.Fatal("plan identity received external admission scope")
	}
	request.Intent, claims.RepositoryID = "recover", "999"
	if _, err := h.authorizeGitHubActionsClaims(claims, request); err == nil {
		t.Fatal("different numeric Fleet repository identity received external admission scope")
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
