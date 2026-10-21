package etcdstore

import (
	"context"
	"encoding/json"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// PlatformReleaseGenesisImport is an immutable provenance record for a first
// controller rollout completed before this API was installed. Import is never
// an execution request and never creates a new Pending intent or attempt.
type PlatformReleaseGenesisImport struct {
	Name                      string          `json:"name"`
	IntentDigest              string          `json:"intentDigest"`
	IntentBytesBase64         string          `json:"intentBytesBase64"`
	IntentSignatureBase64     string          `json:"intentSignatureBase64"`
	CompletionBytesBase64     string          `json:"completionBytesBase64"`
	CompletionSignatureBase64 string          `json:"completionSignatureBase64"`
	CheckpointsJSON           json.RawMessage `json:"checkpoints"`
	CheckpointCount           int             `json:"checkpointCount"`
	CheckpointsSHA256         string          `json:"checkpointsSha256"`
	SignerPublicKeySHA256     string          `json:"signerPublicKeySha256"`
	IntentPublicKeySHA256     string          `json:"intentPublicKeySha256"`
	OperatorSubject           string          `json:"operatorSubject"`
	OperatorDeviceID          string          `json:"operatorDeviceId"`
	ImportedAt                time.Time       `json:"importedAt"`
}

func (s *V3OperationStore) platformReleaseGenesisKey(name string) string {
	return s.prefix + "/v3/platform-release-genesis-imports/" + name
}

func (s *V3OperationStore) GetPlatformReleaseGenesisImport(ctx context.Context, name string) (*PlatformReleaseGenesisImport, error) {
	if !platformReleaseNameRe.MatchString(name) {
		return nil, ErrPlatformReleaseInvalid
	}
	result, err := s.kv.Get(ctx, s.platformReleaseGenesisKey(name))
	if err != nil {
		return nil, err
	}
	if len(result.Kvs) == 0 {
		return nil, ErrPlatformReleaseNotFound
	}
	var record PlatformReleaseGenesisImport
	if err := decodeV3Record(result.Kvs[0].Value, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// ImportPlatformReleaseGenesis performs a compare-and-swap over both the
// genesis and normal intent namespaces. Exact replay returns the prior record;
// a different receipt cannot replace or shadow a prior release identity.
func (s *V3OperationStore) ImportPlatformReleaseGenesis(ctx context.Context, request PlatformReleaseGenesisImport) (*PlatformReleaseGenesisImport, bool, error) {
	if !platformReleaseNameRe.MatchString(request.Name) || !platformReleaseDigestRe.MatchString(request.IntentDigest) || !platformReleaseDigestRe.MatchString(request.CheckpointsSHA256) || !platformReleaseDigestRe.MatchString(request.SignerPublicKeySHA256) || !platformReleaseDigestRe.MatchString(request.IntentPublicKeySHA256) || request.OperatorSubject == "" || request.OperatorDeviceID == "" || request.CheckpointCount < 1 || request.CheckpointCount > 1024 {
		return nil, false, ErrPlatformReleaseInvalid
	}
	key := s.platformReleaseGenesisKey(request.Name)
	prior, err := s.kv.Get(ctx, key)
	if err != nil {
		return nil, false, err
	}
	if len(prior.Kvs) > 0 {
		var existing PlatformReleaseGenesisImport
		if err := decodeV3Record(prior.Kvs[0].Value, &existing); err != nil {
			return nil, false, err
		}
		request.ImportedAt = existing.ImportedAt
		a, _ := json.Marshal(existing)
		b, _ := json.Marshal(request)
		if string(a) != string(b) {
			return nil, false, ErrPlatformReleaseConflict
		}
		return &existing, false, nil
	}
	request.ImportedAt = time.Now().UTC().Truncate(time.Microsecond)
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, false, err
	}
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0), clientv3.Compare(clientv3.CreateRevision(s.platformReleaseIntentKey(request.Name)), "=", 0)).Then(clientv3.OpPut(key, string(encoded))).Commit()
	if err != nil {
		return nil, false, err
	}
	if !txn.Succeeded {
		return nil, false, ErrPlatformReleaseConflict
	}
	return &request, true, nil
}
