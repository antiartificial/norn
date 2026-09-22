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

func TestFleetValidationAcceptsVPCOnlyTLSManagedDatabases(t *testing.T) {
	document := validFleetDocument + `managedDatabases:
  - name: production-primary
    engine: postgresql
    size: db-s-2vcpu-4gb
    region: nyc3
    network:
      exposure: vpc-only
      tls: required
    readReplica:
      name: production-reader
      region: sfo3
  - name: reporting
    engine: mysql
    size: db-s-1vcpu-2gb
    region: nyc3
    network:
      exposure: vpc-only
      tls: required
`
	config, report := ParseAndValidate([]byte(document))
	if config == nil || !report.Valid {
		t.Fatalf("valid managed databases rejected: %#v", report.Findings)
	}
	if len(config.ManagedDatabases) != 2 || config.ManagedDatabases[0].ReadReplica == nil {
		t.Fatalf("managed databases = %#v", config.ManagedDatabases)
	}
}

func TestFleetValidationRejectsManagedDatabasePublicAccessAndInvalidReplica(t *testing.T) {
	document := validFleetDocument + `managedDatabases:
  - name: primary
    engine: redis
    size: ""
    region: ""
    network:
      exposure: public
      tls: optional
    readReplica:
      name: primary
      region: ""
  - name: primary
    engine: mysql
    size: db-s-1vcpu-2gb
    region: nyc3
    network:
      exposure: vpc-only
      tls: required
`
	_, report := ParseAndValidate([]byte(document))
	for _, code := range []string{
		"fleet.managed-database.engine.unsupported",
		"fleet.managed-database.size.required",
		"fleet.managed-database.region.required",
		"fleet.managed-database.network.exposure.required",
		"fleet.managed-database.network.tls.required",
		"fleet.managed-database.read-replica.name.duplicate",
		"fleet.managed-database.read-replica.region.required",
		"fleet.managed-database.name.duplicate",
	} {
		if !hasCode(report.Findings, code) {
			t.Fatalf("expected %s in %#v", code, report.Findings)
		}
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
