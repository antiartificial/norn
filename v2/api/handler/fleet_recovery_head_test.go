package handler

import (
	"strings"
	"testing"
)

func TestRecoveryWorkflowHeadSHASeparatesPlanAndRecoveryCommits(t *testing.T) {
	const planID = "70a6338c-0cb9-4a36-aeca-eead49539056"
	const runner = "github-actions:antiartificial/norn-fleet:39092844731:1"
	planCommit := strings.Repeat("9", 40)
	workflowHead := strings.Repeat("c", 40)
	legacy := planID + ":39092844731:1:" + planCommit + ":" + workflowHead
	if got := recoveryWorkflowHeadSHA(planID, runner, planCommit, workflowHead, ""); got != workflowHead {
		t.Fatalf("recorded OIDC head = %q", got)
	}
	if got := recoveryWorkflowHeadSHA(planID, runner, planCommit, "", legacy); got != workflowHead {
		t.Fatalf("exact legacy head = %q", got)
	}
	for name, binding := range map[string]string{
		"missing":        "",
		"wrong plan":     "other:39092844731:1:" + planCommit + ":" + workflowHead,
		"wrong run":      planID + ":39092844732:1:" + planCommit + ":" + workflowHead,
		"wrong attempt":  planID + ":39092844731:2:" + planCommit + ":" + workflowHead,
		"wrong plan sha": planID + ":39092844731:1:" + strings.Repeat("8", 40) + ":" + workflowHead,
		"invalid head":   planID + ":39092844731:1:" + planCommit + ":main",
		"extra field":    legacy + ":extra",
	} {
		t.Run(name, func(t *testing.T) {
			if got := recoveryWorkflowHeadSHA(planID, runner, planCommit, "", binding); got != "" {
				t.Fatalf("foreign or malformed legacy binding accepted: %q", got)
			}
		})
	}
	// An invalid recorded value cannot be replaced by an operator binding.
	if got := recoveryWorkflowHeadSHA(planID, runner, planCommit, "main", legacy); got != "" {
		t.Fatalf("invalid recorded head was overridden: %q", got)
	}
}
