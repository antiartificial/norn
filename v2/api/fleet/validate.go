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
	return report
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
