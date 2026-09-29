package worker

import (
	"context"
	"strings"
	"testing"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type claimedCatalogReader struct{ revision int64 }

func (r claimedCatalogReader) ActiveDatabaseCatalog(context.Context) (store.DatabaseCatalogRevision, error) {
	return store.DatabaseCatalogRevision{Revision: r.revision}, nil
}

func TestResolveClaimedFleetRuntimeDatabaseItemsRejectsChangedCatalogBeforeMaterial(t *testing.T) {
	source := ClaimedFleetDeploymentSource{Spec: &model.InfraSpec{App: "pilot", Databases: []model.DatabaseRequirement{{Name: "primary", Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL"}}}},
		Managed: VerifiedManagedDeployment{Accepted: store.AcceptedOperation{Deployment: &model.Deployment{}},
			CatalogRevision: 4, ProfileID: "fleet", RuntimeTargets: map[string]database.TargetIdentity{"primary": {BindingID: "primary"}}}}
	_, err := ResolveClaimedFleetRuntimeDatabaseItems(context.Background(), source, claimedCatalogReader{revision: 5},
		unreachableClaimedSecrets{})
	if err == nil || !strings.Contains(err.Error(), "catalog revision changed") {
		t.Fatalf("changed catalog reached material source: %v", err)
	}
}

type unreachableClaimedSecrets struct{}

func (unreachableClaimedSecrets) Resolve(context.Context, string) ([]byte, error) {
	panic("database material must not be read before catalog revision check")
}
