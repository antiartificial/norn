package etcdstore

import (
	"context"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestFleetReleaseEntryRefusesIncompleteAggregateBeforeStoreAccess(t *testing.T) {
	adapter := &V3OperationStore{}
	acceptance := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Kind: "app.deploy"},
		Operation:  model.Operation{Kind: "app.deploy", Source: "release-control-api", Payload: map[string]interface{}{}},
		Deployment: &model.Deployment{Environment: "staging", SourceKind: "release"},
		Regions:    []model.ResolvedRegion{{TrafficWeight: 100}},
		Admission:  store.OperationAdmissionPolicy{OneActiveMutablePerApp: true}}
	if _, err := adapter.AcceptFleetReleaseDeployment(context.Background(), acceptance); err == nil {
		t.Fatal("incomplete Fleet release reached the etcd transaction")
	}
	acceptance.Deployment.Environment = "production"
	if _, err := adapter.AcceptFleetReleaseDeployment(context.Background(), acceptance); err == nil {
		t.Fatal("production Fleet release reached staging acceptance")
	}
}
