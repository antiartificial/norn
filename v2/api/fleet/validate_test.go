package fleet

import (
	"strings"
	"testing"

	"norn/v2/api/model"
)

const validFleetDocument = `apiVersion: norn.dev/fleet/v1
kind: Cluster
metadata:
  repository: antiartificial/norn-fleet
  environment: production
  workflowURL: https://github.com/antiartificial/norn-fleet/actions/workflows/apply.yml
cluster:
  name: production-nyc3
  provider: digitalocean
  region: nyc3
nodePools:
  control:
    size: s-4vcpu-8gb
    min: 3
    desired: 3
    max: 5
    labels: { workload: control-plane }
    replacement:
      strategy: blueGreen
      requireCapacityHeadroom: true
      requireReadiness: true
      drainTimeout: 15m
  app:
    size: s-4vcpu-8gb
    min: 2
    desired: 2
    max: 8
    labels: { workload: app }
    replacement:
      strategy: blueGreen
      requireCapacityHeadroom: true
      requireReadiness: true
      drainTimeout: 15m
`

func TestParseAndValidateAcceptsSafeFleet(t *testing.T) {
	config, report := ParseAndValidate([]byte(validFleetDocument))
	if config == nil || !report.Valid {
		t.Fatalf("valid fleet rejected: %#v", report.Findings)
	}
	if config.NodePools["app"].Desired != 2 {
		t.Fatalf("app desired = %d", config.NodePools["app"].Desired)
	}
}

func TestFleetValidationRejectsUnsafeQuorumAndReplacement(t *testing.T) {
	document := strings.Replace(validFleetDocument, "desired: 3", "desired: 2", 1)
	document = strings.Replace(document, "requireCapacityHeadroom: true", "requireCapacityHeadroom: false", 1)
	_, report := ParseAndValidate([]byte(document))
	if report.Valid || !hasCode(report.Findings, "fleet.control-plane.quorum.unsafe") || !hasCode(report.Findings, "fleet.replacement.headroom.required") {
		t.Fatalf("unsafe fleet findings = %#v", report.Findings)
	}
}

func TestFleetParserRejectsUnknownFields(t *testing.T) {
	_, report := ParseAndValidate([]byte(validFleetDocument + "mispelled: true\n"))
	if report.Valid || !hasCode(report.Findings, "fleet.document.decode-failed") {
		t.Fatalf("unknown field findings = %#v", report.Findings)
	}
}

func TestFleetValidationRejectsUnsafeWorkflowURLsAndRepositoryIdentifiers(t *testing.T) {
	document := strings.Replace(validFleetDocument, "antiartificial/norn-fleet", "https://github.com/antiartificial/norn-fleet", 1)
	document = strings.Replace(document, "https://github.com/antiartificial/norn-fleet/actions/workflows/apply.yml", "javascript:alert(1)", 1)
	_, report := ParseAndValidate([]byte(document))
	if report.Valid || !hasCode(report.Findings, "fleet.metadata.repository.invalid") || !hasCode(report.Findings, "fleet.metadata.workflow-url.invalid") {
		t.Fatalf("unsafe metadata findings = %#v", report.Findings)
	}
}

func TestInfraSpecFleetCrossValidation(t *testing.T) {
	config, report := ParseAndValidate([]byte(validFleetDocument))
	if !report.Valid {
		t.Fatal(report.Findings)
	}
	spec := &model.InfraSpec{App: "toy", Placement: &model.PlacementSpec{NodePool: "missing"}, Processes: map[string]model.Process{"web": {Port: 8080, Health: &model.HealthSpec{Path: "/health"}}}}
	result := ValidateInfraSpec(spec, config, model.ValidateSpec(spec))
	if result.Valid {
		t.Fatal("unknown pool should fail")
	}
	found := false
	for _, finding := range result.Findings {
		if finding.Code == "infraspec.placement.node-pool.unknown" {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings = %#v", result.Findings)
	}
}

func hasCode(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
