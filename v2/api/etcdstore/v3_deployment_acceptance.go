package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

type v3DeploymentRecord struct {
	Deployment model.Deployment `json:"deployment"`
}

type v3DeploymentRegionRecord struct {
	Region model.ResolvedRegion `json:"region"`
}

func deploymentKeyPart(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (s *V3OperationStore) deploymentKey(id string) string {
	return s.prefix + "/v3/deployments/" + deploymentKeyPart(id)
}

func (s *V3OperationStore) deploymentRegionPrefix(id string) string {
	return s.prefix + "/v3/deployment-regions/" + deploymentKeyPart(id) + "/"
}

func (s *V3OperationStore) deploymentRegionKey(id, name string) string {
	return s.deploymentRegionPrefix(id) + deploymentKeyPart(name)
}

// Deployment and every resolved region join the signed acceptance and app
// gate in one etcd transaction. No caller may enable this path before an
// executable deployment worker is available.
func (s *V3OperationStore) prepareDeploymentAdmission(a store.OperationAcceptance) ([]clientv3.Cmp, []clientv3.Op, error) {
	if a.Deployment == nil {
		return nil, nil, nil
	}
	if len(a.Regions) > 32 {
		return nil, nil, &store.AcceptanceValidationError{Reason: "etcd deployment has too many regions"}
	}
	id := a.Deployment.ID
	deployment := *a.Deployment
	deployment.Regions = nil
	encoded, err := json.Marshal(v3DeploymentRecord{Deployment: deployment})
	if err != nil {
		return nil, nil, err
	}
	compares := []clientv3.Cmp{clientv3.Compare(clientv3.CreateRevision(s.deploymentKey(id)), "=", 0)}
	puts := []clientv3.Op{clientv3.OpPut(s.deploymentKey(id), string(encoded))}
	for _, region := range a.Regions {
		encoded, err := json.Marshal(v3DeploymentRegionRecord{Region: region})
		if err != nil {
			return nil, nil, err
		}
		key := s.deploymentRegionKey(id, region.Name)
		compares = append(compares, clientv3.Compare(clientv3.CreateRevision(key), "=", 0))
		puts = append(puts, clientv3.OpPut(key, string(encoded)))
	}
	return compares, puts, nil
}

func (s *V3OperationStore) loadAcceptedDeployment(ctx context.Context, id string) (*model.Deployment, []model.ResolvedRegion, error) {
	if id == "" {
		return nil, nil, nil
	}
	response, err := s.kv.Get(ctx, s.deploymentKey(id))
	if err != nil {
		return nil, nil, err
	}
	if len(response.Kvs) != 1 {
		return nil, nil, fmt.Errorf("accepted deployment is missing")
	}
	var record v3DeploymentRecord
	if err := decodeV3Record(response.Kvs[0].Value, &record); err != nil || record.Deployment.ID != id {
		return nil, nil, fmt.Errorf("accepted deployment record differs from signed ID")
	}
	response, err = s.kv.Get(ctx, s.deploymentRegionPrefix(id), clientv3.WithPrefix(), clientv3.WithRev(response.Header.Revision))
	if err != nil {
		return nil, nil, err
	}
	regions := make([]model.ResolvedRegion, 0, len(response.Kvs))
	seen := map[string]bool{}
	for _, item := range response.Kvs {
		var regionRecord v3DeploymentRegionRecord
		if err := decodeV3Record(item.Value, &regionRecord); err != nil || regionRecord.Region.Name == "" || seen[regionRecord.Region.Name] || string(item.Key) != s.deploymentRegionKey(id, regionRecord.Region.Name) {
			return nil, nil, fmt.Errorf("accepted deployment region record is invalid")
		}
		seen[regionRecord.Region.Name] = true
		regions = append(regions, regionRecord.Region)
	}
	sort.Slice(regions, func(i, j int) bool { return regions[i].Name < regions[j].Name })
	return &record.Deployment, regions, nil
}
