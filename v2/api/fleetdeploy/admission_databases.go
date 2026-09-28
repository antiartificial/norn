package fleetdeploy

import (
	"encoding/json"
	"fmt"
	"sort"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

// BindFirstFleetDeploymentDatabases selects exact current catalog identities
// for every named database of a first Fleet deployment. It returns the string
// carried in the signed operation payload; no connection material is included.
// The caller must accept this payload under the same catalog revision, and
// the claimed worker re-resolves and probes every runtime target before use.
func BindFirstFleetDeploymentDatabases(spec *model.InfraSpec, profile string,
	active store.DatabaseCatalogRevision) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("first Fleet database source is unavailable")
	}
	if spec.Migrations != "" || spec.MigrationDatabase != "" || spec.MigrationPostcondition != nil {
		return "", fmt.Errorf("first Fleet deployment cannot skip declared database migration")
	}
	if !spec.NamedDatabases() {
		if spec.Infrastructure != nil && spec.Infrastructure.Postgres != nil {
			return "", fmt.Errorf("first Fleet deployment cannot use an ambient legacy PostgreSQL target")
		}
		return "", nil
	}
	if profile == "" || active.Revision < 1 {
		return "", fmt.Errorf("first Fleet database profile or catalog is unavailable")
	}
	for _, finding := range spec.DatabaseDeclarationFindings() {
		if finding.Severity == "error" {
			return "", fmt.Errorf("first Fleet database declaration is invalid at %s", finding.Field)
		}
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return "", err
	}
	type targetEntry struct {
		Name   string                  `json:"name"`
		Target database.TargetIdentity `json:"target"`
	}
	set := struct {
		Schema          string        `json:"schema"`
		ProfileID       string        `json:"profileId"`
		CatalogRevision int64         `json:"catalogRevision"`
		Targets         []targetEntry `json:"targets"`
	}{Schema: "norn.database-targets/v1", ProfileID: profile, CatalogRevision: active.Revision, Targets: []targetEntry{}}
	for _, requirement := range spec.Databases {
		for _, capability := range requirement.Capabilities {
			if capability == string(database.CapabilityMigration) || capability == string(database.CapabilitySnapshot) || capability == string(database.CapabilityRestore) {
				return "", fmt.Errorf("first Fleet deployment cannot skip database lifecycle capability %s", capability)
			}
		}
		required := make([]database.Capability, 0, len(requirement.Capabilities))
		for _, capability := range requirement.Capabilities {
			if capability != string(database.CapabilityRestore) {
				required = append(required, database.Capability(capability))
			}
		}
		resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: profile,
			Purpose: database.PurposeApplication, LogicalResourceID: requirement.Name,
			RequiredCapabilities: required})
		if err != nil {
			return "", err
		}
		if err := pipeline.RequireQualifiedMySQLRuntime(spec, requirement, resolved); err != nil {
			return "", err
		}
		if requirement.Runtime != nil && resolved.Target.Engine == database.EngineMySQL {
			if requirement.Runtime.Components == nil || requirement.Runtime.Env != "" || requirement.Runtime.FileEnv != "" ||
				(resolved.TLS.Mode != database.TLSDisabled && (requirement.Runtime.TLS == nil || requirement.Runtime.TLS.CAFileEnv == "" ||
					requirement.Runtime.TLS.ClientCertFileEnv != "" || requirement.Runtime.TLS.ClientKeyFileEnv != "")) {
				return "", fmt.Errorf("first Fleet MySQL runtime shape is unqualified")
			}
		}
		if requirement.Runtime != nil && resolved.Target.Engine == database.EnginePostgreSQL && (requirement.Runtime.Components != nil || requirement.Runtime.TLS != nil) {
			return "", fmt.Errorf("first Fleet PostgreSQL runtime shape is unqualified")
		}
		set.Targets = append(set.Targets, targetEntry{Name: requirement.Name, Target: resolved.Target})
	}
	sort.Slice(set.Targets, func(i, j int) bool { return set.Targets[i].Name < set.Targets[j].Name })
	encoded, err := json.Marshal(set)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
