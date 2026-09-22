package fleet

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"norn/v2/api/model"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

func Parse(document []byte) (*Document, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(document))
	decoder.KnownFields(true)
	var config Document
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode fleet document: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode fleet document: multiple YAML documents are not allowed")
		}
		return nil, fmt.Errorf("decode fleet document: %w", err)
	}
	return &config, nil
}

func Validate(config *Document) *ValidationReport {
	report := &ValidationReport{SchemaVersion: "norn.validation-report/v1", DocumentKind: "fleet", Valid: true, Findings: []Finding{}}
	if config == nil {
		report.add("error", "fleet.document.required", "", "fleet document is required", "Provide a norn.dev/fleet/v1 Cluster document.")
		return report
	}
	report.Name = config.Cluster.Name
	if config.APIVersion != APIVersion {
		report.add("error", "fleet.api-version.unsupported", "apiVersion", fmt.Sprintf("apiVersion must be %q", APIVersion), "Pin the document to the supported fleet contract.")
	}
	if config.Kind != Kind {
		report.add("error", "fleet.kind.unsupported", "kind", fmt.Sprintf("kind must be %q", Kind), "Use a Cluster document.")
	}
	if !namePattern.MatchString(config.Cluster.Name) {
		report.add("error", "fleet.cluster.name.invalid", "cluster.name", "cluster name must be DNS-compatible", "Use lowercase letters, numbers, and hyphens.")
	}
	if !providerPattern.MatchString(config.Cluster.Provider) {
		report.add("error", "fleet.cluster.provider.invalid", "cluster.provider", "provider is required and must be a portable provider name", "For the initial repository use digitalocean.")
	}
	if strings.TrimSpace(config.Cluster.Region) == "" {
		report.add("error", "fleet.cluster.region.required", "cluster.region", "provider region is required", "Set the provider region used by this environment definition.")
	}
	if repository := strings.TrimSpace(config.Metadata.Repository); repository != "" && !repositoryPattern.MatchString(repository) {
		report.add("error", "fleet.metadata.repository.invalid", "metadata.repository", "repository must be an owner/repository identifier", "Use a repository slug such as organization/norn-fleet.")
	}
	if workflowURL := strings.TrimSpace(config.Metadata.WorkflowURL); workflowURL != "" {
		parsed, err := url.Parse(workflowURL)
		if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil {
			report.add("error", "fleet.metadata.workflow-url.invalid", "metadata.workflowURL", "workflowURL must be an absolute HTTPS URL without embedded credentials", "Use the HTTPS URL of the protected apply workflow.")
		}
	}
	if len(config.NodePools) == 0 {
		report.add("error", "fleet.node-pools.required", "nodePools", "at least one node pool is required", "Declare control, ingress, or application capacity.")
	}

	names := make([]string, 0, len(config.NodePools))
	for name := range config.NodePools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		pool := config.NodePools[name]
		field := "nodePools." + name
		if !namePattern.MatchString(name) {
			report.add("error", "fleet.node-pool.name.invalid", field, "node pool name must be DNS-compatible", "Use lowercase letters, numbers, and hyphens.")
		}
		if strings.TrimSpace(pool.Size) == "" {
			report.add("error", "fleet.node-pool.size.required", field+".size", "VM size is required", "Pin an immutable provider VM size slug.")
		}
		if pool.Min < 0 || pool.Desired < 0 || pool.Max < 0 {
			report.add("error", "fleet.node-pool.capacity.negative", field, "capacity values cannot be negative", "Use min <= desired <= max.")
		}
		if pool.Max == 0 {
			report.add("error", "fleet.node-pool.max.required", field+".max", "max must be greater than zero", "Set an explicit upper capacity bound.")
		}
		if pool.Min > pool.Desired || pool.Desired > pool.Max {
			report.add("error", "fleet.node-pool.capacity.order", field, "capacity must satisfy min <= desired <= max", "Correct the declared capacity bounds.")
		}
		strategy := pool.Replacement.Strategy
		if strategy == "" {
			strategy = "blueGreen"
		}
		if strategy != "blueGreen" && strategy != "rolling" {
			report.add("error", "fleet.replacement.strategy.invalid", field+".replacement.strategy", "strategy must be blueGreen or rolling", "Prefer blueGreen for immutable VM replacement.")
		}
		if strategy == "blueGreen" {
			if !pool.Replacement.RequireCapacityHeadroom {
				report.add("error", "fleet.replacement.headroom.required", field+".replacement.requireCapacityHeadroom", "blueGreen replacement requires capacity headroom", "Set requireCapacityHeadroom: true.")
			}
			if !pool.Replacement.RequireReadiness {
				report.add("error", "fleet.replacement.readiness.required", field+".replacement.requireReadiness", "blueGreen replacement requires readiness assurance", "Set requireReadiness: true.")
			}
		}
		if pool.Replacement.DrainTimeout == "" {
			report.add("warning", "fleet.replacement.drain-timeout.missing", field+".replacement.drainTimeout", "drain timeout is not explicit", "Set a workload-appropriate duration such as 15m.")
		} else if duration, err := time.ParseDuration(pool.Replacement.DrainTimeout); err != nil || duration <= 0 || duration > 24*time.Hour {
			report.add("error", "fleet.replacement.drain-timeout.invalid", field+".replacement.drainTimeout", "drain timeout must be a positive duration no greater than 24h", "Use a Go duration such as 15m.")
		}

		role := strings.ToLower(pool.Labels["workload"])
		switch role {
		case "control", "control-plane":
			if pool.Desired < 3 || pool.Desired%2 == 0 {
				report.add("error", "fleet.control-plane.quorum.unsafe", field+".desired", "control-plane pools require an odd desired count of at least three", "Use three or five Consul/Nomad servers.")
			}
		case "ingress":
			if pool.Min < 2 {
				report.add("error", "fleet.ingress.redundancy.insufficient", field+".min", "ingress pools require at least two nodes", "Set min to at least 2 so one ingress node can fail.")
			}
		case "app", "worker":
			if pool.Min < 2 {
				report.add("warning", "fleet.app.headroom.low", field+".min", "application pool cannot tolerate one node loss at minimum capacity", "Use at least two nodes for HA workloads.")
			}
		}
	}
	validateManagedDatabases(config.ManagedDatabases, report)
	return report
}

func validateManagedDatabases(databases []ManagedDatabase, report *ValidationReport) {
	names := make(map[string]struct{}, len(databases))
	for i, database := range databases {
		field := fmt.Sprintf("managedDatabases[%d]", i)
		name := strings.TrimSpace(database.Name)
		if !namePattern.MatchString(name) {
			report.add("error", "fleet.managed-database.name.invalid", field+".name", "managed database name must be DNS-compatible", "Use lowercase letters, numbers, and hyphens.")
		} else if _, duplicate := names[name]; duplicate {
			report.add("error", "fleet.managed-database.name.duplicate", field+".name", fmt.Sprintf("managed database name %q is declared more than once", name), "Give each managed database and replica a distinct resource name.")
		} else {
			names[name] = struct{}{}
		}
		if database.Engine != "postgresql" && database.Engine != "mysql" {
			report.add("error", "fleet.managed-database.engine.unsupported", field+".engine", "managed database engine must be postgresql or mysql", "Choose postgresql or mysql.")
		}
		if strings.TrimSpace(database.Size) == "" {
			report.add("error", "fleet.managed-database.size.required", field+".size", "managed database size is required", "Pin the provider size slug selected during review.")
		}
		if strings.TrimSpace(database.Region) == "" {
			report.add("error", "fleet.managed-database.region.required", field+".region", "managed database region is required", "Set the provider region for this managed database.")
		}
		if database.Network.Exposure != "vpc-only" {
			report.add("error", "fleet.managed-database.network.exposure.required", field+".network.exposure", "managed databases must be VPC-only", "Set network.exposure: vpc-only; public database access is not supported by this contract.")
		}
		if database.Network.TLS != "required" {
			report.add("error", "fleet.managed-database.network.tls.required", field+".network.tls", "managed databases must require TLS", "Set network.tls: required.")
		}
		if replica := database.ReadReplica; replica != nil {
			replicaName := strings.TrimSpace(replica.Name)
			if !namePattern.MatchString(replicaName) {
				report.add("error", "fleet.managed-database.read-replica.name.invalid", field+".readReplica.name", "read replica name must be DNS-compatible", "Use lowercase letters, numbers, and hyphens.")
			} else if replicaName == name {
				report.add("error", "fleet.managed-database.read-replica.name.duplicate", field+".readReplica.name", "read replica name must differ from its primary", "Give the read replica a separate resource name.")
			} else if _, duplicate := names[replicaName]; duplicate {
				report.add("error", "fleet.managed-database.read-replica.name.duplicate", field+".readReplica.name", fmt.Sprintf("read replica name %q is already declared", replicaName), "Give each managed database and replica a distinct resource name.")
			} else {
				names[replicaName] = struct{}{}
			}
			if strings.TrimSpace(replica.Region) == "" {
				report.add("error", "fleet.managed-database.read-replica.region.required", field+".readReplica.region", "read replica region is required", "Set the provider region for the read replica.")
			}
		}
	}
}

func ValidateInfraSpec(spec *model.InfraSpec, config *Document, base *model.ValidationResult) *model.ValidationResult {
	if base == nil {
		base = model.ValidateSpec(spec)
	}
	if spec == nil || config == nil {
		return base
	}
	check := func(field, pool string) {
		if pool == "" {
			return
		}
		if _, ok := config.NodePools[pool]; !ok {
			base.Valid = false
			base.Findings = append(base.Findings, model.ValidationFinding{Severity: "error", Code: "infraspec.placement.node-pool.unknown", Field: field, Message: fmt.Sprintf("nodePool %q is not declared by the fleet document", pool), Remediation: "Declare the pool in norn-fleet or reference an existing logical pool."})
		}
	}
	if spec.Placement != nil {
		check("placement.nodePool", spec.Placement.NodePool)
	}
	return base
}

func Digest(document []byte) string {
	sum := sha256.Sum256(document)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ParseAndValidate(document []byte) (*Document, *ValidationReport) {
	config, err := Parse(document)
	if err != nil {
		report := &ValidationReport{SchemaVersion: "norn.validation-report/v1", DocumentKind: "fleet", Valid: false, Findings: []Finding{{Severity: "error", Code: "fleet.document.decode-failed", Field: "document", Message: err.Error(), Remediation: "Fix YAML syntax and unknown fields."}}}
		return nil, report
	}
	return config, Validate(config)
}

func (r *ValidationReport) add(severity, code, field, message, remediation string) {
	if severity == "error" {
		r.Valid = false
	}
	r.Findings = append(r.Findings, Finding{Severity: severity, Code: code, Field: field, Message: message, Remediation: remediation})
}
