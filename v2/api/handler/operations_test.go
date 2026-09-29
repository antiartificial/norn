package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"norn/v2/api/model"
)

func TestReleaseOperationReadableIsBoundToExactWorkloadReceipt(t *testing.T) {
	ci := &CIIdentity{Provider: "github-actions", Repository: "acme/widgets", RepositoryID: "1", RepositoryOwnerID: "2", RunID: "77", RunAttempt: "1", WorkflowRef: "acme/widgets/.github/workflows/caller.yml@abc", WorkflowSHA: "abc", JobWorkflowRef: "acme/release/.github/workflows/release.yml@def", JobWorkflowSHA: "def", Ref: "refs/heads/main", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	op := &model.Operation{Kind: "app.preflight", App: "widgets", Metadata: map[string]interface{}{"environment": "staging", "requestCI": ci}}
	req := WithAccessPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/operations/op-1", nil), &AccessPrincipal{Scopes: []string{ScopeReleaseStage}, App: "widgets", Environment: "staging", CI: ci})
	if !releaseOperationReadable(req, op) {
		t.Fatal("matching stage workload cannot read its own operation")
	}
	other := *ci
	other.RunID = "78"
	req = WithAccessPrincipal(req, &AccessPrincipal{Scopes: []string{ScopeReleaseStage}, App: "widgets", Environment: "staging", CI: &other})
	if releaseOperationReadable(req, op) {
		t.Fatal("different workflow run read operation")
	}
	req = WithAccessPrincipal(req, &AccessPrincipal{Scopes: []string{ScopeReleasePromote}, App: "widgets", Environment: "staging", CI: ci})
	if releaseOperationReadable(req, op) {
		t.Fatal("promotion scope read a staging preflight")
	}
}
