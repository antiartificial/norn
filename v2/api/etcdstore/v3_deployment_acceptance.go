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

func (s *V3OperationStore) deploymentAcceptanceIndexKey(id string) string {
	return s.prefix + "/v3/deployment-acceptance-index/" + deploymentKeyPart(id)
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
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.CreateRevision(s.deploymentKey(id)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.deploymentAcceptanceIndexKey(id)), "=", 0),
	}
	puts := []clientv3.Op{
		clientv3.OpPut(s.deploymentKey(id), string(encoded)),
		clientv3.OpPut(s.deploymentAcceptanceIndexKey(id), s.acceptanceKey(a.Identity)),
	}
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

// GetDeployment resolves the signed acceptance before returning a deployment
// projection. Immutable placement comes from accepted region records; mutable
// status comes from the claim-fenced result records when present.
func (s *V3OperationStore) GetDeployment(ctx context.Context, id string) (*model.Deployment, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	index, err := s.kv.Get(ctx, s.deploymentAcceptanceIndexKey(id))
	if err != nil {
		return nil, err
	}
	if len(index.Kvs) != 1 {
		return nil, ErrNotFound
	}
	key := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, key)
	if err != nil {
		return nil, err
	}
	if loaded.record.Accepted.Intent.DeploymentID != id || s.acceptanceKey(loaded.record.Identity) != key {
		return nil, &store.AcceptanceSignatureError{Err: fmt.Errorf("deployment acceptance index link differs")}
	}
	accepted, err := s.replay(ctx, key, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil {
		return nil, err
	}
	if accepted.Deployment == nil || accepted.Deployment.ID != id {
		return nil, &store.AcceptanceSignatureError{Err: fmt.Errorf("signed deployment is missing")}
	}
	results, err := s.loadDeploymentRegionResults(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(results) > len(accepted.Regions) {
		return nil, &store.AcceptanceSignatureError{Err: fmt.Errorf("deployment has excess region results")}
	}
	byName := make(map[string]model.DeploymentRegion, len(results))
	for _, result := range results {
		byName[result.Region] = result
	}
	deployment := *accepted.Deployment
	deployment.Regions = make([]model.DeploymentRegion, 0, len(accepted.Regions))
	for _, region := range accepted.Regions {
		result, found := byName[region.Name]
		if found {
			if result.NomadRegion != region.NomadRegion || result.DesiredWeight != region.TrafficWeight || result.ActiveWeight < 0 || result.ActiveWeight > region.TrafficWeight || result.UpdatedAt.IsZero() || !validDeploymentStatus(result.Status) {
				return nil, &store.AcceptanceSignatureError{Err: fmt.Errorf("deployment region result differs from accepted placement")}
			}
			deployment.Regions = append(deployment.Regions, result)
			delete(byName, region.Name)
			continue
		}
		deployment.Regions = append(deployment.Regions, model.DeploymentRegion{DeploymentID: id, Region: region.Name, NomadRegion: region.NomadRegion, Status: model.StatusQueued, DesiredWeight: region.TrafficWeight, UpdatedAt: deployment.StartedAt})
	}
	if len(byName) != 0 {
		return nil, &store.AcceptanceSignatureError{Err: fmt.Errorf("deployment has foreign region results")}
	}
	return &deployment, nil
}
