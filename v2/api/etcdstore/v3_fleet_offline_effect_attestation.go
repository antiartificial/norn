package etcdstore

// Signed effect evidence is accepted on the target's held etcd authority.
// This ledger is source-only: no route, grant issuance/consumption, or
// provider mutation is performed by accepting an attestation.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/fleet/offline"
)

const effectAttestationReceiptSchema = "norn.fleet-offline-effect-accepted/v1"
const effectChallengeAcceptedHeadSchema = "norn.fleet-offline-effect-challenge-accepted/v1"

var ErrOfflineEffectAttestationConflict = errors.New("offline effect attestation changed or authority is stale")

type AcceptedOfflineEffectAttestation struct {
	SchemaVersion     string                    `json:"schemaVersion"`
	PlanID            string                    `json:"planId"`
	AttemptID         string                    `json:"attemptId"`
	Purpose           string                    `json:"purpose"`
	ChallengeID       string                    `json:"challengeId"`
	AcceptedAt        time.Time                 `json:"acceptedAt"`
	AttestationSHA256 string                    `json:"attestationSHA256"`
	Envelope          json.RawMessage           `json:"envelope"`
	ObserverPublicKey string                    `json:"observerPublicKey"`
	Challenge         offline.EffectChallengeV1 `json:"challenge"`
	Binding           offline.Binding           `json:"binding"`
	Attempt           offline.Attempt           `json:"attempt"`
	Grant             *offline.EffectGrant      `json:"grant,omitempty"`
}

// The challenge head advances once, in the same CAS as the three receipt
// rows. The immutable issue event stays as the original challenge. Even if
// every receipt row is later removed, the head prevents a second acceptance.
type effectChallengeAcceptedHead struct {
	SchemaVersion     string                    `json:"schemaVersion"`
	Challenge         offline.EffectChallengeV1 `json:"challenge"`
	AttestationSHA256 string                    `json:"attestationSHA256"`
	AcceptedAt        time.Time                 `json:"acceptedAt"`
}

func decodeEffectChallengeHead(headRaw, eventRaw []byte, planID, purpose string) (offline.EffectChallengeV1, *effectChallengeAcceptedHead, error) {
	if len(headRaw) == 0 && len(eventRaw) == 0 {
		return offline.EffectChallengeV1{}, nil, ErrNotFound
	}
	challenge, err := offline.DecodeEffectChallengeV1(eventRaw)
	if err != nil || challenge.PlanID != planID || challenge.Purpose != purpose || len(headRaw) == 0 {
		return offline.EffectChallengeV1{}, nil, ErrOfflineAttemptCorrupt
	}
	if bytes.Equal(headRaw, eventRaw) {
		return challenge, nil, nil
	}
	var accepted effectChallengeAcceptedHead
	if decodeV3Record(headRaw, &accepted) != nil || accepted.SchemaVersion != effectChallengeAcceptedHeadSchema ||
		accepted.Challenge != challenge || accepted.AcceptedAt.IsZero() ||
		!accepted.AcceptedAt.Before(challenge.ExpiresAt) {
		return offline.EffectChallengeV1{}, nil, ErrOfflineAttemptCorrupt
	}
	canonical, err := json.Marshal(accepted)
	if err != nil || !bytes.Equal(canonical, headRaw) {
		return offline.EffectChallengeV1{}, nil, ErrOfflineAttemptCorrupt
	}
	return challenge, &accepted, nil
}

func (s *V3OperationStore) offlineEffectAttestationKey(planID, purpose string) string {
	return s.prefix + "/v3/fleet-offline-effect-attestations/" + planID + "/" + purpose
}
func (s *V3OperationStore) offlineEffectAttestationEventKey(planID, purpose string) string {
	return s.prefix + "/v3/fleet-offline-effect-attestation-events/" + planID + "/" + purpose + "/00000000000000000001"
}
func (s *V3OperationStore) offlineEffectChallengeConsumedKey(planID, purpose string) string {
	return s.prefix + "/v3/fleet-offline-effect-challenge-consumed/" + planID + "/" + purpose + "/00000000000000000001"
}

func verifyAcceptedEffectReceipt(raw []byte, planID, attemptID, challengeID string) (AcceptedOfflineEffectAttestation, error) {
	var receipt AcceptedOfflineEffectAttestation
	if len(raw) == 0 || len(raw) > 128*1024 || decodeV3Record(raw, &receipt) != nil || receipt.SchemaVersion != effectAttestationReceiptSchema ||
		receipt.PlanID != planID || receipt.AttemptID != attemptID || receipt.ChallengeID != challengeID ||
		!validEffectChallengePurpose(receipt.Purpose) || receipt.Challenge.ID != challengeID || receipt.Challenge.Purpose != receipt.Purpose ||
		receipt.Challenge.PlanID != planID || receipt.Challenge.AttemptID != attemptID || receipt.Binding.PlanID != planID || receipt.Attempt.ID != attemptID ||
		receipt.AcceptedAt.IsZero() || !receipt.AcceptedAt.Before(receipt.Challenge.ExpiresAt) ||
		len(receipt.Envelope) == 0 || len(receipt.Envelope) > 32*1024 || !json.Valid(receipt.Envelope) {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(canonical, raw) {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	signed, err := offline.DecodeSignedEffectAttestationV1(receipt.Envelope)
	if err != nil {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	public, err := base64.RawURLEncoding.Strict().DecodeString(receipt.ObserverPublicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	epoch := receipt.Challenge.AuthorityEpoch
	digest, err := offline.VerifyEffectAttestationV1(signed, receipt.Challenge, receipt.Binding, receipt.Attempt, receipt.Grant,
		epoch, receipt.Challenge.ObserverSubject, receipt.Challenge.ObserverKeyID, ed25519.PublicKey(public), receipt.AcceptedAt)
	if err != nil || digest != receipt.AttestationSHA256 {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	return receipt, nil
}

// ReadAcceptedOfflineEffectAttestation reads historical evidence independently
// of the current fence/grant, so a lost submit response can be reconciled even
// after subsequent transitions. The exact signed bytes remain immutable.
func (s *V3OperationStore) ReadAcceptedOfflineEffectAttestation(ctx context.Context, planID, purpose, challengeID string) (AcceptedOfflineEffectAttestation, error) {
	if s == nil || uuid.Validate(planID) != nil || uuid.Validate(challengeID) != nil || !validEffectChallengePurpose(purpose) {
		return AcceptedOfflineEffectAttestation{}, offline.ErrEffectAttestation
	}
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineEffectAttestationKey(planID, purpose)),
		clientv3.OpGet(s.offlineEffectAttestationEventKey(planID, purpose)),
		clientv3.OpGet(s.offlineEffectChallengeConsumedKey(planID, purpose)),
		clientv3.OpGet(s.offlineEffectChallengeKey(planID, purpose)),
		clientv3.OpGet(s.offlineEffectChallengeEventKey(planID, purpose)),
	).Commit()
	if err != nil {
		return AcceptedOfflineEffectAttestation{}, err
	}
	if len(response.Responses) != 5 {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	var raw [5][]byte
	for i, item := range response.Responses {
		rows := item.GetResponseRange().Kvs
		if len(rows) > 1 {
			return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
		}
		if len(rows) == 1 {
			raw[i] = rows[0].Value
		}
	}
	return verifyAcceptedEffectRows(raw, planID, purpose, challengeID)
}

// The five rows are read at one etcd revision. A transitioned challenge head
// with a missing receipt tuple is corruption, never pristine eligibility.
func verifyAcceptedEffectRows(raw [5][]byte, planID, purpose, challengeID string) (AcceptedOfflineEffectAttestation, error) {
	challenge, acceptedHead, headErr := decodeEffectChallengeHead(raw[3], raw[4], planID, purpose)
	if headErr != nil && headErr != ErrNotFound {
		return AcceptedOfflineEffectAttestation{}, headErr
	}
	if len(raw[0]) == 0 && len(raw[1]) == 0 && len(raw[2]) == 0 {
		if acceptedHead != nil {
			return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
		}
		return AcceptedOfflineEffectAttestation{}, ErrNotFound
	}
	if len(raw[0]) == 0 || !bytes.Equal(raw[0], raw[1]) || len(raw[2]) == 0 || acceptedHead == nil || headErr != nil {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	var candidate AcceptedOfflineEffectAttestation
	if decodeV3Record(raw[0], &candidate) != nil || uuid.Validate(candidate.AttemptID) != nil {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	receipt, err := verifyAcceptedEffectReceipt(raw[0], planID, candidate.AttemptID, challengeID)
	if err != nil || receipt.Purpose != purpose {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	if !bytes.Equal(raw[2], []byte(receipt.AttestationSHA256)) {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	if challenge != receipt.Challenge || acceptedHead.AttestationSHA256 != receipt.AttestationSHA256 ||
		!acceptedHead.AcceptedAt.Equal(receipt.AcceptedAt) {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	return receipt, nil
}

// SubmitOfflineEffectAttestation atomically consumes one challenge and stores
// the exact canonical signed envelope plus target-owned acceptance time.
// ISSUE leaves grant absent; CONSUME leaves the existing grant issued.
func (s *V3OperationStore) SubmitOfflineEffectAttestation(ctx context.Context, planID, attemptID, purpose, challengeID string, rawEnvelope []byte) (AcceptedOfflineEffectAttestation, error) {
	return s.submitOfflineEffectAttestationWithClock(ctx, planID, attemptID, purpose, challengeID, rawEnvelope, time.Now)
}
func (s *V3OperationStore) submitOfflineEffectAttestationWithClock(ctx context.Context, planID, attemptID, purpose, challengeID string, rawEnvelope []byte, clock func() time.Time) (AcceptedOfflineEffectAttestation, error) {
	if s == nil || clock == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || uuid.Validate(challengeID) != nil || !validEffectChallengePurpose(purpose) {
		return AcceptedOfflineEffectAttestation{}, offline.ErrEffectAttestation
	}
	signed, err := offline.DecodeSignedEffectAttestationV1(rawEnvelope)
	if err != nil || signed.Attestation.Purpose != purpose || signed.Attestation.ChallengeID != challengeID {
		return AcceptedOfflineEffectAttestation{}, offline.ErrEffectAttestation
	}
	if prior, readErr := s.ReadAcceptedOfflineEffectAttestation(ctx, planID, purpose, challengeID); readErr == nil {
		if prior.AttemptID == attemptID && bytes.Equal(prior.Envelope, rawEnvelope) {
			return prior, nil
		}
		return AcceptedOfflineEffectAttestation{}, ErrOfflineEffectAttestationConflict
	} else if !errors.Is(readErr, ErrNotFound) {
		return AcceptedOfflineEffectAttestation{}, readErr
	}
	snapshot, err := s.readEffectChallengeSnapshot(ctx, planID, purpose)
	if err != nil {
		return AcceptedOfflineEffectAttestation{}, err
	}
	if !snapshot.hasChallenge || snapshot.challenge.ID != challengeID || snapshot.attempt.ID != attemptID {
		return AcceptedOfflineEffectAttestation{}, offline.ErrEffectAttestation
	}
	public, err := base64.RawURLEncoding.Strict().DecodeString(snapshot.trust.PublicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return AcceptedOfflineEffectAttestation{}, ErrOfflineAttemptCorrupt
	}
	var grant *offline.EffectGrant
	if purpose == offline.EffectConsumePurpose {
		grant = &snapshot.grant
	}
	acceptedAt := clock().UTC()
	digest, err := offline.VerifyEffectAttestationV1(signed, snapshot.challenge, snapshot.binding, snapshot.attempt, grant, snapshot.epoch,
		snapshot.trust.ObserverSubject, snapshot.trust.KeyID, ed25519.PublicKey(public), acceptedAt)
	if err != nil {
		return AcceptedOfflineEffectAttestation{}, err
	}
	receipt := AcceptedOfflineEffectAttestation{SchemaVersion: effectAttestationReceiptSchema, PlanID: planID, AttemptID: attemptID,
		Purpose: purpose, ChallengeID: challengeID, AcceptedAt: acceptedAt, AttestationSHA256: digest,
		Envelope: append(json.RawMessage(nil), rawEnvelope...), ObserverPublicKey: snapshot.trust.PublicKey,
		Challenge: snapshot.challenge, Binding: snapshot.binding, Attempt: snapshot.attempt, Grant: grant}
	// Refresh the target authority clock directly before the commit; no client
	// timestamp can extend freshness or the challenge's lifetime.
	receipt.AcceptedAt = clock().UTC()
	digest, err = offline.VerifyEffectAttestationV1(signed, snapshot.challenge, snapshot.binding, snapshot.attempt, grant, snapshot.epoch,
		snapshot.trust.ObserverSubject, snapshot.trust.KeyID, ed25519.PublicKey(public), receipt.AcceptedAt)
	if err != nil || digest != receipt.AttestationSHA256 {
		return AcceptedOfflineEffectAttestation{}, offline.ErrEffectAttestation
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return AcceptedOfflineEffectAttestation{}, err
	}
	acceptedHead := effectChallengeAcceptedHead{SchemaVersion: effectChallengeAcceptedHeadSchema,
		Challenge: snapshot.challenge, AttestationSHA256: digest, AcceptedAt: receipt.AcceptedAt}
	headRaw, err := json.Marshal(acceptedHead)
	if err != nil {
		return AcceptedOfflineEffectAttestation{}, err
	}
	compares := make([]clientv3.Cmp, 0, len(snapshot.keys)+3)
	for i, key := range snapshot.keys {
		if i == effectChallengeRunnerAttempts {
			compares = append(compares, clientv3.Compare(clientv3.CreateRevision(key).WithPrefix(), "=", 0))
		} else {
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(key), "=", snapshot.revisions[i]))
		}
	}
	compares = append(compares,
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectChallengeConsumedKey(planID, purpose)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectAttestationKey(planID, purpose)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineEffectAttestationEventKey(planID, purpose)), "=", 0),
	)
	result, err := s.kv.Txn(ctx).If(compares...).Then(
		clientv3.OpPut(s.offlineEffectChallengeKey(planID, purpose), string(headRaw)),
		clientv3.OpPut(s.offlineEffectChallengeConsumedKey(planID, purpose), digest),
		clientv3.OpPut(s.offlineEffectAttestationKey(planID, purpose), string(raw)),
		clientv3.OpPut(s.offlineEffectAttestationEventKey(planID, purpose), string(raw)),
	).Commit()
	if err != nil {
		return AcceptedOfflineEffectAttestation{}, err
	}
	if !result.Succeeded {
		prior, readErr := s.ReadAcceptedOfflineEffectAttestation(ctx, planID, purpose, challengeID)
		if readErr == nil && prior.AttemptID == attemptID && bytes.Equal(prior.Envelope, rawEnvelope) {
			return prior, nil
		}
		return AcceptedOfflineEffectAttestation{}, ErrOfflineEffectAttestationConflict
	}
	return receipt, nil
}
