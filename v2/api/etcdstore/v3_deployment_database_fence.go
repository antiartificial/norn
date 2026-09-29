package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// deploymentDatabaseCatalogCompare pins the active catalog pointer in the
// same transaction that signs and accepts a deployment's database targets.
// Worker-side Expected resolution remains necessary for credential material.
func (s *V3OperationStore) deploymentDatabaseCatalogCompare(ctx context.Context, op model.Operation) (*clientv3.Cmp, error) {
	if op.Kind != "app.deploy" || op.Payload == nil {
		return nil, nil
	}
	raw, present := op.Payload["databaseTargets"]
	if !present {
		return nil, nil
	}
	value, ok := raw.(string)
	if !ok {
		return nil, &store.AcceptanceValidationError{Reason: "deployment database target set must be an exact signed string"}
	}
	var signed struct {
		Schema          string `json:"schema"`
		ProfileID       string `json:"profileId"`
		CatalogRevision int64  `json:"catalogRevision"`
		Targets         []struct {
			Name   string                  `json:"name"`
			Target database.TargetIdentity `json:"target"`
		} `json:"targets"`
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&signed) != nil || decoder.Decode(new(any)) != io.EOF || signed.Schema != "norn.database-targets/v1" ||
		signed.ProfileID == "" || signed.CatalogRevision < 1 || len(signed.Targets) == 0 {
		return nil, &store.AcceptanceValidationError{Reason: "deployment signed database target set is invalid"}
	}
	active, err := s.ActiveDatabaseCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("deployment database catalog is unavailable: %w", err)
	}
	if active.Revision != signed.CatalogRevision {
		return nil, store.ErrDatabaseCatalogRevisionConflict
	}
	comparison := clientv3.Compare(clientv3.Value(s.databaseCatalogActiveKey()), "=", strconv.FormatInt(active.Revision, 10))
	return &comparison, nil
}
