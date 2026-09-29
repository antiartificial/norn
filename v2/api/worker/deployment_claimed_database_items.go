package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"norn/v2/api/database"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

type ClaimedDeploymentCatalogReader interface {
	ActiveDatabaseCatalog(context.Context) (store.DatabaseCatalogRevision, error)
}

// ResolveClaimedFleetRuntimeDatabaseItems opens only the signed runtime
// identities against the active catalog revision. Connection material is
// returned solely for private Nomad Variable staging; callers must not log it.
func ResolveClaimedFleetRuntimeDatabaseItems(ctx context.Context, source ClaimedFleetDeploymentSource,
	catalogs ClaimedDeploymentCatalogReader, secrets database.SecretSource) (map[string]string, error) {
	if source.Spec == nil || source.Managed.Accepted.Deployment == nil {
		return nil, fmt.Errorf("claimed Fleet database source is unavailable")
	}
	runtimeCount := 0
	for _, requirement := range source.Spec.Databases {
		if requirement.Runtime != nil {
			runtimeCount++
		}
	}
	if runtimeCount != len(source.Managed.RuntimeTargets) {
		return nil, fmt.Errorf("claimed Fleet runtime database set differs from signed targets")
	}
	if len(source.Managed.RuntimeTargets) == 0 {
		return nil, nil
	}
	if catalogs == nil || secrets == nil || source.Managed.CatalogRevision < 1 || source.Managed.ProfileID == "" {
		return nil, fmt.Errorf("claimed Fleet database resolver is unavailable")
	}
	active, err := catalogs.ActiveDatabaseCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if active.Revision != source.Managed.CatalogRevision {
		return nil, fmt.Errorf("claimed Fleet database catalog revision changed")
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return nil, err
	}
	items := make(map[string]string)
	seen := make(map[string]bool)
	for _, requirement := range source.Spec.Databases {
		if requirement.Runtime == nil {
			continue
		}
		target, ok := source.Managed.RuntimeTargets[requirement.Name]
		if !ok || seen[requirement.Name] {
			return nil, fmt.Errorf("claimed Fleet runtime database set is incomplete")
		}
		seen[requirement.Name] = true
		resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: source.Managed.ProfileID,
			Purpose: database.PurposeApplication, LogicalResourceID: requirement.Name,
			Expected: &target, RequiredCapabilities: []database.Capability{database.CapabilityRuntime}})
		if err != nil {
			return nil, err
		}
		session, err := database.OpenSession(ctx, resolved, secrets)
		if err != nil {
			return nil, err
		}
		_, probeErr := session.Probe(ctx)
		var connection map[string]string
		if probeErr == nil {
			connection, probeErr = nomad.RuntimeDatabaseItems(requirement, resolved, session)
		}
		closeErr := session.Close()
		if probeErr != nil {
			return nil, probeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		for key, value := range connection {
			if _, duplicate := items[key]; duplicate {
				return nil, fmt.Errorf("claimed Fleet runtime database keys overlap")
			}
			items[key] = value
		}
		identity, err := json.Marshal(target)
		if err != nil {
			return nil, err
		}
		items[nomad.DatabaseTargetItemKey(requirement.Name)] = string(identity)
	}
	if len(seen) != len(source.Managed.RuntimeTargets) {
		return nil, fmt.Errorf("claimed Fleet runtime database set differs from signed targets")
	}
	return items, nil
}
