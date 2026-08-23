package cmd

import (
	"bytes"
	"strings"
	"testing"

	"norn/v2/cli/api"
)

func TestPrintProductionReadinessIncludesBlockersAndRemediation(t *testing.T) {
	report := &api.ProductionReadinessReport{
		Status:  "blocked",
		Summary: api.ProductionReadinessSummary{Passed: 1, Warnings: 1, Failed: 1, Total: 3},
		Checks: []api.ProductionReadinessCheck{
			{ID: "control.credentials", Category: "control", Status: "pass", Detail: "configured"},
			{ID: "audit.durable", Category: "operations", Status: "warn", Detail: "process-local", Remediation: "persist audit records"},
			{ID: "nomad.acl", Category: "scheduler", Status: "fail", Detail: "disabled", Remediation: "enable ACLs"},
		},
	}
	var out bytes.Buffer
	printProductionReadiness(&out, report)
	text := out.String()
	for _, expected := range []string{"status=blocked", "nomad.acl", "enable ACLs", "audit.durable", "persist audit records"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("output missing %q:\n%s", expected, text)
		}
	}
}

func TestParseProductionEvidence(t *testing.T) {
	evidence, err := parseProductionEvidence([]string{"snapshot=s3://receipts/db-1", "rto=42s"})
	if err != nil {
		t.Fatal(err)
	}
	if evidence["rto"] != "42s" {
		t.Fatalf("evidence=%v", evidence)
	}
	if _, err := parseProductionEvidence([]string{"missing-separator"}); err == nil {
		t.Fatal("invalid evidence was accepted")
	}
}
