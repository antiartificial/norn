package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// PlatformReleaseIntent is independent of Fleet capacity resources. Its
// immutable bindings make a release request replayable and auditable without
// granting any provider or host effect to the API.
type PlatformReleaseIntent struct {
	SchemaVersion           int                      `json:"schemaVersion"`
	Name                    string                   `json:"name"`
	Generation              int64                    `json:"generation"`
	Environment             string                   `json:"environment"`
	OldSourceSHA            string                   `json:"oldSourceSha"`
	NewSourceSHA            string                   `json:"newSourceSha"`
	OldPublisherRunID       int64                    `json:"oldPublisherRunId"`
	PublisherRunID          int64                    `json:"publisherRunId"`
	OldSignedManifestSHA256 string                   `json:"oldSignedManifestSha256"`
	NewSignedManifestSHA256 string                   `json:"newSignedManifestSha256"`
	SignedReleaseTag        string                   `json:"signedReleaseTag"`
	ControlRoster           []PlatformReleaseControl `json:"controlRoster"`
	StateSHA256             string                   `json:"stateSha256"`
	HostKeysSHA256          string                   `json:"hostKeysSha256"`
	Status                  string                   `json:"status"`
	IntentDigest            string                   `json:"intentDigest"`
	ApprovedBy              string                   `json:"approvedBy,omitempty"`
	ApprovedAt              *time.Time               `json:"approvedAt,omitempty"`
	CreatedAt               time.Time                `json:"createdAt"`
}

// PlatformReleaseAttempt is a durable, single-active execution fence. The
// executor must use the returned ID on every checkpoint and final transition.
type PlatformReleaseAttempt struct {
	ID             string    `json:"id"`
	IntentName     string    `json:"intentName"`
	Generation     int64     `json:"generation"`
	IntentDigest   string    `json:"intentDigest"`
	IdempotencyKey string    `json:"idempotencyKey"`
	Status         string    `json:"status"`
	OwnerID        string    `json:"ownerId"`
	OwnerSession   string    `json:"ownerSession"`
	FenceEpoch     int64     `json:"fenceEpoch"`
	LeaseExpiresAt time.Time `json:"leaseExpiresAt"`
	NextOrdinal    int64     `json:"nextOrdinal"`
	StartedAt      time.Time `json:"startedAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type PlatformReleaseCheckpoint struct {
	Ordinal        int64     `json:"ordinal"`
	Phase          string    `json:"phase"`
	ControlName    string    `json:"controlName,omitempty"`
	ObservedSHA    string    `json:"observedSha,omitempty"`
	Health         string    `json:"health"`
	EvidenceSHA256 string    `json:"evidenceSha256"`
	RecordedAt     time.Time `json:"recordedAt"`
}

type PlatformReleaseControl struct {
	Name      string `json:"name"`
	TailnetIP string `json:"tailnetIp"`
	PrivateIP string `json:"privateIp"`
}

var (
	ErrPlatformReleaseInvalid  = errors.New("invalid platform release intent")
	ErrPlatformReleaseConflict = errors.New("platform release intent conflict")
	ErrPlatformReleaseNotFound = errors.New("platform release intent not found")
	platformReleaseNameRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	platformReleaseSHARe       = regexp.MustCompile(`^[a-f0-9]{40}$`)
	platformReleaseDigestRe    = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func (r PlatformReleaseIntent) Validate() error {
	if !platformReleaseNameRe.MatchString(r.Name) || r.Generation != 1 || r.Environment != "staging" ||
		!platformReleaseSHARe.MatchString(r.OldSourceSHA) || !platformReleaseSHARe.MatchString(r.NewSourceSHA) ||
		r.OldSourceSHA == r.NewSourceSHA || r.OldPublisherRunID <= 0 || r.PublisherRunID <= 0 || r.SignedReleaseTag != "platform-"+r.NewSourceSHA ||
		!platformReleaseDigestRe.MatchString(r.OldSignedManifestSHA256) || !platformReleaseDigestRe.MatchString(r.NewSignedManifestSHA256) ||
		!platformReleaseDigestRe.MatchString(r.StateSHA256) || !platformReleaseDigestRe.MatchString(r.HostKeysSHA256) ||
		len(r.ControlRoster) != 3 {
		return ErrPlatformReleaseInvalid
	}
	seen := map[string]bool{}
	addressSeen := map[string]bool{}
	for _, control := range r.ControlRoster {
		if !platformReleaseNameRe.MatchString(control.Name) || net.ParseIP(control.TailnetIP) == nil || net.ParseIP(control.PrivateIP) == nil || seen[control.Name] {
			return ErrPlatformReleaseInvalid
		}
		if addressSeen[control.TailnetIP] || addressSeen[control.PrivateIP] {
			return ErrPlatformReleaseInvalid
		}
		seen[control.Name] = true
		addressSeen[control.TailnetIP], addressSeen[control.PrivateIP] = true, true
	}
	for _, name := range []string{"control-01", "control-02", "control-03"} {
		if !seen[name] {
			return ErrPlatformReleaseInvalid
		}
	}
	return nil
}

func (s *V3OperationStore) platformReleaseIntentKey(name string) string {
	return s.prefix + "/v3/platform-release-intents/" + name
}

// EnsurePlatformReleaseIntent compares the entire immutable request on replay.
// A conflicting request cannot retarget an accepted intent.
func (s *V3OperationStore) EnsurePlatformReleaseIntent(ctx context.Context, request PlatformReleaseIntent) (*PlatformReleaseIntent, bool, error) {
	if err := request.Validate(); err != nil {
		return nil, false, err
	}
	request.SchemaVersion = 1
	request.Status = "Pending"
	request.IntentDigest = ""
	request.ApprovedBy = ""
	request.ApprovedAt = nil
	request.CreatedAt = time.Time{}
	canonical, _ := json.Marshal(request)
	digest := sha256.Sum256(canonical)
	request.IntentDigest = hex.EncodeToString(digest[:])
	key := s.platformReleaseIntentKey(request.Name)
	existing, err := s.kv.Get(ctx, key)
	if err != nil {
		return nil, false, err
	}
	if len(existing.Kvs) > 0 {
		var prior PlatformReleaseIntent
		if err := decodeV3Record(existing.Kvs[0].Value, &prior); err != nil {
			return nil, false, err
		}
		request.CreatedAt = prior.CreatedAt
		request.Status = prior.Status
		request.ApprovedBy = prior.ApprovedBy
		request.ApprovedAt = prior.ApprovedAt
		if !platformReleaseIntentEqual(prior, request) {
			return nil, false, ErrPlatformReleaseConflict
		}
		return &prior, false, nil
	}
	request.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, false, err
	}
	committed, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).Then(clientv3.OpPut(key, string(encoded))).Commit()
	if err != nil {
		return nil, false, err
	}
	if !committed.Succeeded {
		return s.EnsurePlatformReleaseIntent(ctx, request)
	}
	return &request, true, nil
}

func platformReleaseIntentEqual(a, b PlatformReleaseIntent) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func (s *V3OperationStore) GetPlatformReleaseIntent(ctx context.Context, name string) (*PlatformReleaseIntent, error) {
	if !platformReleaseNameRe.MatchString(name) {
		return nil, ErrPlatformReleaseInvalid
	}
	result, err := s.kv.Get(ctx, s.platformReleaseIntentKey(name))
	if err != nil {
		return nil, err
	}
	if len(result.Kvs) == 0 {
		return nil, ErrPlatformReleaseNotFound
	}
	var record PlatformReleaseIntent
	if err := decodeV3Record(result.Kvs[0].Value, &record); err != nil {
		return nil, fmt.Errorf("platform release intent corrupt: %w", err)
	}
	return &record, nil
}

func (s *V3OperationStore) ListPlatformReleaseIntents(ctx context.Context) ([]PlatformReleaseIntent, error) {
	result, err := s.kv.Get(ctx, s.prefix+"/v3/platform-release-intents/", clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	if len(result.Kvs) > 256 {
		return nil, fmt.Errorf("too many platform release intents")
	}
	records := make([]PlatformReleaseIntent, 0, len(result.Kvs))
	for _, kv := range result.Kvs {
		var record PlatformReleaseIntent
		if err := decodeV3Record(kv.Value, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, nil
}

func (s *V3OperationStore) ApprovePlatformReleaseIntent(ctx context.Context, name, digest, approver string) (*PlatformReleaseIntent, error) {
	if !platformReleaseDigestRe.MatchString(digest) || strings.TrimSpace(approver) == "" {
		return nil, ErrPlatformReleaseInvalid
	}
	key := s.platformReleaseIntentKey(name)
	result, err := s.kv.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(result.Kvs) == 0 {
		return nil, ErrPlatformReleaseNotFound
	}
	var record PlatformReleaseIntent
	if err := decodeV3Record(result.Kvs[0].Value, &record); err != nil {
		return nil, err
	}
	if record.IntentDigest != digest {
		return nil, ErrPlatformReleaseConflict
	}
	if record.Status == "Approved" {
		if record.ApprovedBy == approver {
			return &record, nil
		}
		return nil, ErrPlatformReleaseConflict
	}
	if record.Status != "Pending" {
		return nil, ErrPlatformReleaseConflict
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	record.Status, record.ApprovedBy, record.ApprovedAt = "Approved", approver, &now
	encoded, _ := json.Marshal(record)
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", result.Kvs[0].ModRevision)).Then(clientv3.OpPut(key, string(encoded))).Commit()
	if err != nil {
		return nil, err
	}
	if !txn.Succeeded {
		return nil, ErrPlatformReleaseConflict
	}
	return &record, nil
}

func (s *V3OperationStore) platformReleaseAttemptKey(name string) string {
	return s.prefix + "/v3/platform-release-attempts/" + name
}
func (s *V3OperationStore) platformReleaseCheckpointPrefix(name string) string {
	return s.prefix + "/v3/platform-release-checkpoints/" + name + "/"
}

func (s *V3OperationStore) GetPlatformReleaseAttempt(ctx context.Context, name string) (*PlatformReleaseAttempt, error) {
	if !platformReleaseNameRe.MatchString(name) {
		return nil, ErrPlatformReleaseInvalid
	}
	result, err := s.kv.Get(ctx, s.platformReleaseAttemptKey(name))
	if err != nil {
		return nil, err
	}
	if len(result.Kvs) == 0 {
		return nil, ErrPlatformReleaseNotFound
	}
	var record PlatformReleaseAttempt
	if err := decodeV3Record(result.Kvs[0].Value, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *V3OperationStore) StartPlatformReleaseAttempt(ctx context.Context, name, digest, idempotencyKey, ownerID, ownerSession string) (*PlatformReleaseAttempt, bool, error) {
	if !platformReleaseNameRe.MatchString(name) || !platformReleaseDigestRe.MatchString(digest) || len(idempotencyKey) < 8 || len(idempotencyKey) > 128 || len(ownerID) < 8 || len(ownerID) > 128 || len(ownerSession) < 16 || len(ownerSession) > 128 {
		return nil, false, ErrPlatformReleaseInvalid
	}
	intentKey, attemptKey := s.platformReleaseIntentKey(name), s.platformReleaseAttemptKey(name)
	intentKV, err := s.kv.Get(ctx, intentKey)
	if err != nil {
		return nil, false, err
	}
	if len(intentKV.Kvs) == 0 {
		return nil, false, ErrPlatformReleaseNotFound
	}
	var intent PlatformReleaseIntent
	if err := decodeV3Record(intentKV.Kvs[0].Value, &intent); err != nil {
		return nil, false, err
	}
	if intent.Status != "Approved" || intent.IntentDigest != digest {
		return nil, false, ErrPlatformReleaseConflict
	}
	prior, err := s.kv.Get(ctx, attemptKey)
	if err != nil {
		return nil, false, err
	}
	if len(prior.Kvs) > 0 {
		var existing PlatformReleaseAttempt
		if err := decodeV3Record(prior.Kvs[0].Value, &existing); err != nil {
			return nil, false, err
		}
		if existing.IdempotencyKey == idempotencyKey && existing.IntentDigest == digest && existing.OwnerID == ownerID && existing.OwnerSession == ownerSession {
			return &existing, false, nil
		}
		return nil, false, ErrPlatformReleaseConflict
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	sum := sha256.Sum256([]byte(name + ":" + digest + ":" + idempotencyKey))
	attempt := PlatformReleaseAttempt{ID: hex.EncodeToString(sum[:]), IntentName: name, Generation: intent.Generation, IntentDigest: digest, IdempotencyKey: idempotencyKey, Status: "Running", OwnerID: ownerID, OwnerSession: ownerSession, FenceEpoch: 1, LeaseExpiresAt: now.Add(10 * time.Minute), NextOrdinal: 1, StartedAt: now, UpdatedAt: now}
	encoded, _ := json.Marshal(attempt)
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(intentKey), "=", intentKV.Kvs[0].ModRevision), clientv3.Compare(clientv3.CreateRevision(attemptKey), "=", 0)).Then(clientv3.OpPut(attemptKey, string(encoded))).Commit()
	if err != nil {
		return nil, false, err
	}
	if !txn.Succeeded {
		return nil, false, ErrPlatformReleaseConflict
	}
	return &attempt, true, nil
}

func (s *V3OperationStore) AppendPlatformReleaseCheckpoint(ctx context.Context, name, attemptID, ownerID, ownerSession string, fenceEpoch int64, checkpoint PlatformReleaseCheckpoint) (*PlatformReleaseAttempt, error) {
	if !platformReleaseNameRe.MatchString(name) || !platformReleaseDigestRe.MatchString(attemptID) || checkpoint.Ordinal < 1 || checkpoint.Ordinal > 1024 || checkpoint.Phase == "" || checkpoint.Health == "" || !platformReleaseDigestRe.MatchString(checkpoint.EvidenceSHA256) {
		return nil, ErrPlatformReleaseInvalid
	}
	if checkpoint.ObservedSHA != "" && !platformReleaseSHARe.MatchString(checkpoint.ObservedSHA) {
		return nil, ErrPlatformReleaseInvalid
	}
	if checkpoint.ControlName != "" && !platformReleaseNameRe.MatchString(checkpoint.ControlName) {
		return nil, ErrPlatformReleaseInvalid
	}
	attemptKey := s.platformReleaseAttemptKey(name)
	result, err := s.kv.Get(ctx, attemptKey)
	if err != nil {
		return nil, err
	}
	if len(result.Kvs) == 0 {
		return nil, ErrPlatformReleaseNotFound
	}
	var attempt PlatformReleaseAttempt
	if err := decodeV3Record(result.Kvs[0].Value, &attempt); err != nil {
		return nil, err
	}
	if attempt.ID != attemptID || attempt.Status != "Running" || attempt.NextOrdinal != checkpoint.Ordinal || attempt.OwnerID != ownerID || attempt.OwnerSession != ownerSession || attempt.FenceEpoch != fenceEpoch || !time.Now().Before(attempt.LeaseExpiresAt) {
		return nil, ErrPlatformReleaseConflict
	}
	checkpoint.RecordedAt = time.Now().UTC().Truncate(time.Microsecond)
	checkpointKey := fmt.Sprintf("%s%020d", s.platformReleaseCheckpointPrefix(name), checkpoint.Ordinal)
	checkpointEncoded, _ := json.Marshal(checkpoint)
	attempt.NextOrdinal++
	attempt.UpdatedAt = checkpoint.RecordedAt
	attemptEncoded, _ := json.Marshal(attempt)
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(attemptKey), "=", result.Kvs[0].ModRevision), clientv3.Compare(clientv3.CreateRevision(checkpointKey), "=", 0)).Then(clientv3.OpPut(checkpointKey, string(checkpointEncoded)), clientv3.OpPut(attemptKey, string(attemptEncoded))).Commit()
	if err != nil {
		return nil, err
	}
	if !txn.Succeeded {
		return nil, ErrPlatformReleaseConflict
	}
	return &attempt, nil
}

func (s *V3OperationStore) ListPlatformReleaseCheckpoints(ctx context.Context, name string) ([]PlatformReleaseCheckpoint, error) {
	if !platformReleaseNameRe.MatchString(name) {
		return nil, ErrPlatformReleaseInvalid
	}
	result, err := s.kv.Get(ctx, s.platformReleaseCheckpointPrefix(name), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	if len(result.Kvs) > 1024 {
		return nil, ErrPlatformReleaseConflict
	}
	records := make([]PlatformReleaseCheckpoint, 0, len(result.Kvs))
	for _, kv := range result.Kvs {
		var record PlatformReleaseCheckpoint
		if err := decodeV3Record(kv.Value, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *V3OperationStore) FinishPlatformReleaseAttempt(ctx context.Context, name, attemptID, ownerID, ownerSession string, fenceEpoch int64, status string) (*PlatformReleaseAttempt, error) {
	if status != "Succeeded" && status != "Failed" {
		return nil, ErrPlatformReleaseInvalid
	}
	key := s.platformReleaseAttemptKey(name)
	result, err := s.kv.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(result.Kvs) == 0 {
		return nil, ErrPlatformReleaseNotFound
	}
	var attempt PlatformReleaseAttempt
	if err := decodeV3Record(result.Kvs[0].Value, &attempt); err != nil {
		return nil, err
	}
	if attempt.ID != attemptID || attempt.OwnerID != ownerID || attempt.OwnerSession != ownerSession || attempt.FenceEpoch != fenceEpoch || !time.Now().Before(attempt.LeaseExpiresAt) {
		return nil, ErrPlatformReleaseConflict
	}
	if attempt.Status == status {
		return &attempt, nil
	}
	if attempt.Status != "Running" {
		return nil, ErrPlatformReleaseConflict
	}
	attempt.Status = status
	attempt.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	encoded, _ := json.Marshal(attempt)
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", result.Kvs[0].ModRevision)).Then(clientv3.OpPut(key, string(encoded))).Commit()
	if err != nil {
		return nil, err
	}
	if !txn.Succeeded {
		return nil, ErrPlatformReleaseConflict
	}
	return &attempt, nil
}

func (s *V3OperationStore) RenewPlatformReleaseAttempt(ctx context.Context, name, attemptID, ownerID, ownerSession string, fenceEpoch int64) (*PlatformReleaseAttempt, error) {
	key := s.platformReleaseAttemptKey(name)
	result, err := s.kv.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(result.Kvs) == 0 {
		return nil, ErrPlatformReleaseNotFound
	}
	var attempt PlatformReleaseAttempt
	if err := decodeV3Record(result.Kvs[0].Value, &attempt); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if attempt.ID != attemptID || attempt.OwnerID != ownerID || attempt.OwnerSession != ownerSession || attempt.FenceEpoch != fenceEpoch || attempt.Status != "Running" || !now.Before(attempt.LeaseExpiresAt) {
		return nil, ErrPlatformReleaseConflict
	}
	attempt.LeaseExpiresAt = now.Add(10 * time.Minute)
	attempt.UpdatedAt = now
	encoded, _ := json.Marshal(attempt)
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", result.Kvs[0].ModRevision)).Then(clientv3.OpPut(key, string(encoded))).Commit()
	if err != nil {
		return nil, err
	}
	if !txn.Succeeded {
		return nil, ErrPlatformReleaseConflict
	}
	return &attempt, nil
}
