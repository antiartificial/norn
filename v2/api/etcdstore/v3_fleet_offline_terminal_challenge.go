package etcdstore

// Terminal challenge issuance belongs to the target Fleet's etcd authority.
// These methods neither observe providers nor accept an attestation, and have
// no route. The management observer can recover a lost issue response through
// the immutable event; it cannot cause a second nonce to be issued.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/fleet"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/offline"
)

var ErrOfflineTerminalChallengeConflict = errors.New("offline terminal challenge changed or authority is stale")

const terminalObserverTrustSchema = "norn.fleet-offline-terminal-observer-trust/v1"

// Provisioned independently by the target authority; issuance only reads it.
// A later submit path must verify against this same pinned public key.
type terminalObserverTrust struct {
	SchemaVersion   string `json:"schemaVersion"`
	Generation      uint64 `json:"generation"`
	ObserverSubject string `json:"observerSubject"`
	KeyID           string `json:"keyId"`
	PublicKey       string `json:"publicKey"`
}

func (s *V3OperationStore) offlineTerminalObserverTrustKey() string {
	return s.prefix + "/v3/fleet-offline-terminal-observer-trust"
}

func decodeTerminalObserverTrust(raw []byte) (terminalObserverTrust, error) {
	var trust terminalObserverTrust
	if decodeV3Record(raw, &trust) != nil || trust.SchemaVersion != terminalObserverTrustSchema || trust.Generation == 0 ||
		trust.ObserverSubject == "" || trust.KeyID == "" || strings.ContainsAny(trust.ObserverSubject+trust.KeyID, "\x00\r\n\t ") {
		return terminalObserverTrust{}, ErrOfflineAttemptCorrupt
	}
	public, err := base64.RawURLEncoding.Strict().DecodeString(trust.PublicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return terminalObserverTrust{}, ErrOfflineAttemptCorrupt
	}
	canonical, err := json.Marshal(trust)
	if err != nil || !bytes.Equal(canonical, raw) {
		return terminalObserverTrust{}, ErrOfflineAttemptCorrupt
	}
	return trust, nil
}

func terminalObserverPublicKeySHA256(trust terminalObserverTrust) string {
	public, _ := base64.RawURLEncoding.Strict().DecodeString(trust.PublicKey)
	digest := sha256.Sum256(public)
	return hex.EncodeToString(digest[:])
}

func (s *V3OperationStore) offlineTerminalChallengeKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-terminal-challenges/" + planID
}

func (s *V3OperationStore) offlineTerminalChallengeEventKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-terminal-challenge-events/" + planID + "/00000000000000000001"
}

// terminalChallengeSnapshot is one revision-pinned read. The first probe only
// selects the dynamic owner and target keys; it is never accepted as evidence.
type terminalChallengeSnapshot struct {
	keys         []string
	raw          [][]byte
	revisions    []int64
	grant        offline.EffectGrant
	binding      offline.Binding
	trust        terminalObserverTrust
	aliases      []string
	challenge    offline.TerminalChallengeV1
	hasChallenge bool
}

const (
	challengeHead = iota
	challengeEvent
	challengeGrant
	challengeIssued
	challengeConsumed
	challengeAttempt
	challengeBinding
	challengeCheckpoint
	challengeCheckpointEvent
	challengeOwner
	challengePlan
	challengeRegistry
	challengeFirstAlias
	challengeSecondAlias
	challengeTarget
	challengeFence
	challengeEpoch
	challengeAbandoned
	challengeGithubPrep
	challengeRunnerDispatch
	challengeRunnerAttempts
	challengeTerminal
	challengeTerminalEvent
	challengeObserverTrust
)

func (s *V3OperationStore) readTerminalChallengeSnapshot(ctx context.Context, planID string) (terminalChallengeSnapshot, error) {
	var out terminalChallengeSnapshot
	probe, err := s.kv.Get(ctx, s.offlineDispatchKey(planID))
	if err != nil {
		return out, err
	}
	if len(probe.Kvs) != 1 || probe.Header.Revision < 1 {
		return out, ErrOfflineAttemptCorrupt
	}
	candidate, err := offline.DecodeBinding(probe.Kvs[0].Value)
	if err != nil || candidate.PlanID != planID || candidate.TargetID == "" || candidate.OwnerAuthorizationID == "" {
		return out, ErrOfflineAttemptCorrupt
	}
	// This second probe only discovers the registered target's alias key names.
	// Every value is re-read below at the binding probe's fixed revision and
	// compared again by ModRevision at the issuance CAS.
	targetProbe, err := s.kv.Get(ctx, s.fleetTargetKey(candidate.TargetID), clientv3.WithRev(probe.Header.Revision))
	if err != nil {
		return out, err
	}
	if len(targetProbe.Kvs) != 1 {
		return out, ErrOfflineAttemptCorrupt
	}
	var aliasCandidate FleetTarget
	if decodeV3Record(targetProbe.Kvs[0].Value, &aliasCandidate) != nil ||
		aliasCandidate.TargetID != candidate.TargetID || len(aliasCandidate.Aliases) < 2 || len(aliasCandidate.Aliases) > 32 {
		return out, ErrOfflineAttemptCorrupt
	}
	seenAliases := make(map[string]struct{}, len(aliasCandidate.Aliases))
	for _, alias := range aliasCandidate.Aliases {
		if alias == "" || len(alias) > 256 {
			return out, ErrOfflineAttemptCorrupt
		}
		if _, exists := seenAliases[alias]; exists {
			return out, ErrOfflineAttemptCorrupt
		}
		seenAliases[alias] = struct{}{}
	}
	out.aliases = append([]string(nil), aliasCandidate.Aliases...)
	firstAlias, secondAlias := out.aliases[0], out.aliases[1]
	out.keys = []string{
		s.offlineTerminalChallengeKey(planID), s.offlineTerminalChallengeEventKey(planID),
		s.offlineEffectGrantKey(planID), s.offlineEffectGrantEventKey(planID, 1), s.offlineEffectGrantEventKey(planID, 2),
		s.offlineAttemptKey(planID), s.offlineDispatchKey(planID), s.offlineCheckpointKey(planID), s.offlineCheckpointEventKey(planID, 1),
		s.offlineOwnerConsumptionKey(candidate.OwnerAuthorizationID), s.opKey(planID), s.fleetTargetRegistryKey(),
		s.fleetTargetAliasKey(firstAlias), s.fleetTargetAliasKey(secondAlias),
		s.fleetTargetKey(candidate.TargetID), s.fleetTargetFenceKey(candidate.TargetID), s.fleetAuthorityEpochKey(),
		s.fleetTargetAbandonedKey(planID), s.fleetGitHubDispatchPreparationKey(planID), s.fleetRunnerDispatchKey(planID),
		s.fleetRunnerAttemptPrefix(planID), s.offlineTerminalKey(planID), s.offlineTerminalEventKey(planID),
		s.offlineTerminalObserverTrustKey(),
	}
	for _, alias := range out.aliases[2:] {
		out.keys = append(out.keys, s.fleetTargetAliasKey(alias))
	}
	ops := make([]clientv3.Op, len(out.keys))
	for i, key := range out.keys {
		if i == challengeRunnerAttempts {
			ops[i] = clientv3.OpGet(key, clientv3.WithPrefix(), clientv3.WithRev(probe.Header.Revision))
		} else {
			ops[i] = clientv3.OpGet(key, clientv3.WithRev(probe.Header.Revision))
		}
	}
	response, err := s.kv.Txn(ctx).Then(ops...).Commit()
	if err != nil {
		return out, err
	}
	if len(response.Responses) != len(out.keys) {
		return out, ErrOfflineAttemptCorrupt
	}
	out.raw = make([][]byte, len(out.keys))
	out.revisions = make([]int64, len(out.keys))
	for i, item := range response.Responses {
		result := item.GetResponseRange()
		if result == nil || len(result.Kvs) > 1 {
			return out, ErrOfflineAttemptCorrupt
		}
		if len(result.Kvs) == 1 {
			out.raw[i] = result.Kvs[0].Value
			out.revisions[i] = result.Kvs[0].ModRevision
		}
	}
	for _, i := range []int{challengeGrant, challengeIssued, challengeConsumed, challengeAttempt, challengeBinding,
		challengeCheckpoint, challengeCheckpointEvent, challengeOwner, challengePlan, challengeRegistry,
		challengeFirstAlias, challengeSecondAlias, challengeTarget, challengeFence, challengeEpoch} {
		if out.revisions[i] == 0 {
			return out, ErrOfflineAttemptCorrupt
		}
	}
	for i := challengeObserverTrust + 1; i < len(out.keys); i++ {
		if out.revisions[i] == 0 || string(out.raw[i]) != candidate.TargetID {
			return out, ErrOfflineAttemptCorrupt
		}
	}
	for _, i := range []int{challengeAbandoned, challengeGithubPrep, challengeRunnerDispatch, challengeRunnerAttempts} {
		if out.revisions[i] != 0 {
			return out, ErrOfflineTerminalChallengeConflict
		}
	}
	if (out.revisions[challengeTerminal] == 0) != (out.revisions[challengeTerminalEvent] == 0) {
		return out, ErrOfflineAttemptCorrupt
	}
	if out.revisions[challengeTerminal] != 0 {
		return out, ErrOfflineTerminalChallengeConflict
	}
	if out.revisions[challengeObserverTrust] == 0 {
		return out, ErrOfflineAttemptCorrupt
	}
	if out.trust, err = decodeTerminalObserverTrust(out.raw[challengeObserverTrust]); err != nil {
		return out, err
	}
	grant, err := decodeOfflineEffectGrantPair(out.raw[challengeGrant], out.raw[challengeIssued], out.raw[challengeConsumed], planID)
	if err != nil || grant.Phase != "consumed" || grant.ConsumedAt == nil {
		return out, ErrOfflineAttemptCorrupt
	}
	out.grant = grant
	binding, err := offline.DecodeBinding(out.raw[challengeBinding])
	digest := sha256.Sum256(out.raw[challengeBinding])
	if err != nil || binding.SchemaVersion != offline.BindingSchemaV2 || binding.PlanID != planID ||
		binding.TargetID != grant.TargetID || binding.TargetID != candidate.TargetID ||
		binding.OwnerAuthorizationID != candidate.OwnerAuthorizationID || binding.FenceGeneration != grant.FenceGeneration ||
		hex.EncodeToString(digest[:]) != grant.BindingSHA256 || binding.SourceCommit != grant.SourceCommit ||
		binding.SavedPlanSHA256 != grant.SavedPlanSHA256 || binding.EffectSHA256 != grant.EffectSHA256 ||
		binding.Backend != grant.Backend || binding.ProviderAccountUUID != grant.ProviderAccountUUID ||
		binding.NodeName != grant.NodeName || binding.DropletID != grant.DropletID {
		return out, ErrOfflineAttemptCorrupt
	}
	out.binding = binding
	attempt, err := decodeOfflineAttempt(out.raw[challengeAttempt], planID)
	if err != nil || attempt.ID != grant.AttemptID || attempt.BindingSHA256 != grant.BindingSHA256 ||
		attempt.PrincipalSubject != binding.RunnerPrincipalSubject || attempt.TargetID != grant.TargetID ||
		attempt.FenceGeneration != grant.FenceGeneration {
		return out, ErrOfflineAttemptCorrupt
	}
	checkpoint, err := decodeOfflineCheckpointPair(out.raw[challengeCheckpoint], out.raw[challengeCheckpointEvent], planID)
	if err != nil || checkpoint.AttemptID != attempt.ID || checkpoint.EvidenceSHA256 != grant.PrechangeSHA256 ||
		checkpoint.AuthorityEpoch != grant.AuthorityEpoch || checkpoint.FenceGeneration != grant.FenceGeneration {
		return out, ErrOfflineAttemptCorrupt
	}
	if string(out.raw[challengeOwner]) != planID+":"+binding.OwnerEnvelopeSHA256 {
		return out, ErrOfflineAttemptCorrupt
	}
	var plan v3Record
	if decodeV3Record(out.raw[challengePlan], &plan) != nil {
		return out, ErrOfflineAttemptCorrupt
	}
	var typed fleet.CapacityPlan
	if !validOfflineContractionPlan(plan.Operation, planID, &typed) {
		return out, ErrOfflineAttemptCorrupt
	}
	var registry v3FleetTargetRegistry
	if decodeV3Record(out.raw[challengeRegistry], &registry) != nil || registry.Generation < 1 ||
		string(out.raw[challengeFirstAlias]) != binding.TargetID || string(out.raw[challengeSecondAlias]) != binding.TargetID {
		return out, ErrOfflineAttemptCorrupt
	}
	var target FleetTarget
	if decodeV3Record(out.raw[challengeTarget], &target) != nil || target.TargetID != binding.TargetID ||
		target.Provider != "digitalocean" || target.ProviderAccount != grant.ProviderAccountUUID ||
		target.StateBackend != "s3://"+binding.Backend.Bucket+"/"+binding.Backend.Key ||
		!slices.Equal(target.Aliases, out.aliases) ||
		!slices.Contains(target.Aliases, "cluster:"+typed.Cluster) || !slices.Contains(target.Aliases, "environment:staging") {
		return out, ErrOfflineAttemptCorrupt
	}
	_, canonical, err := lifecycle.CanonicalTarget(lifecycle.TargetIdentity{Provider: target.Provider,
		ProviderAccount: target.ProviderAccount, StateBackend: target.StateBackend})
	if err != nil || canonical != binding.TargetID {
		return out, ErrOfflineAttemptCorrupt
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(string(out.raw[challengeEpoch])), 10, 64)
	if err != nil || epoch < 1 || epoch != grant.AuthorityEpoch {
		return out, ErrOfflineTerminalChallengeConflict
	}
	var fence v3FleetTargetFence
	if decodeV3Record(out.raw[challengeFence], &fence) != nil || !fence.Held || fence.HolderPlanID != planID ||
		fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 || uint64(fence.Generation) != grant.FenceGeneration ||
		fence.AuthorityEpoch != grant.AuthorityEpoch {
		return out, ErrOfflineTerminalChallengeConflict
	}
	if out.revisions[challengeHead] == 0 && out.revisions[challengeEvent] == 0 {
		return out, nil
	}
	if out.revisions[challengeHead] == 0 || out.revisions[challengeEvent] == 0 ||
		!bytes.Equal(out.raw[challengeHead], out.raw[challengeEvent]) {
		return out, ErrOfflineAttemptCorrupt
	}
	if decodeV3Record(out.raw[challengeHead], &out.challenge) != nil ||
		out.challenge.ValidateForGrant(grant, out.challenge.IssuedAt) != nil ||
		out.challenge.DispatchNonceSHA256 != binding.DispatchNonceSHA256 || out.challenge.HolderID != planID ||
		out.challenge.ObserverSubject != out.trust.ObserverSubject || out.challenge.ObserverKeyID != out.trust.KeyID ||
		out.challenge.TrustGeneration != out.trust.Generation ||
		out.challenge.ObserverPublicKeySHA256 != terminalObserverPublicKeySHA256(out.trust) {
		return out, ErrOfflineTerminalChallengeConflict
	}
	canonicalChallenge, err := json.Marshal(out.challenge)
	if err != nil || !bytes.Equal(canonicalChallenge, out.raw[challengeHead]) {
		return out, ErrOfflineAttemptCorrupt
	}
	out.hasChallenge = true
	return out, nil
}

// GetOfflineTerminalChallenge recovers the exact immutable challenge even
// after its expiry. Readback does not authorize observation or an effect.
func (s *V3OperationStore) GetOfflineTerminalChallenge(ctx context.Context, planID string) (offline.TerminalChallengeV1, error) {
	if s == nil || uuid.Validate(planID) != nil {
		return offline.TerminalChallengeV1{}, ErrOfflineAttemptCorrupt
	}
	snapshot, err := s.readTerminalChallengeSnapshot(ctx, planID)
	if err != nil {
		return offline.TerminalChallengeV1{}, err
	}
	if !snapshot.hasChallenge {
		return offline.TerminalChallengeV1{}, ErrNotFound
	}
	return snapshot.challenge, nil
}

// IssueOfflineTerminalChallenge atomically creates the challenge head and
// event only for the exact consumed attempt. An existing challenge is returned
// verbatim on exact replay, including after a lost response. No lease/TTL can
// silently erase evidence of a prior challenge.
func (s *V3OperationStore) IssueOfflineTerminalChallenge(ctx context.Context, planID, attemptID, grantID,
	verifierSubject, observerSubject string) (offline.TerminalChallengeV1, error) {
	return s.issueOfflineTerminalChallengeWithClock(ctx, planID, attemptID, grantID, verifierSubject, observerSubject, time.Now)
}

func (s *V3OperationStore) issueOfflineTerminalChallengeWithClock(ctx context.Context, planID, attemptID, grantID,
	verifierSubject, observerSubject string, clock func() time.Time) (offline.TerminalChallengeV1, error) {
	if s == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || uuid.Validate(grantID) != nil {
		return offline.TerminalChallengeV1{}, offline.ErrTerminalAttestation
	}
	if clock == nil {
		return offline.TerminalChallengeV1{}, offline.ErrTerminalAttestation
	}
	snapshot, err := s.readTerminalChallengeSnapshot(ctx, planID)
	if err != nil {
		return offline.TerminalChallengeV1{}, err
	}
	if snapshot.grant.AttemptID != attemptID || snapshot.grant.ID != grantID {
		return offline.TerminalChallengeV1{}, offline.ErrTerminalAttestation
	}
	if observerSubject != snapshot.trust.ObserverSubject {
		return offline.TerminalChallengeV1{}, offline.ErrTerminalAttestation
	}
	if snapshot.hasChallenge {
		if snapshot.challenge.VerifierSubject != verifierSubject || snapshot.challenge.ObserverSubject != observerSubject {
			return offline.TerminalChallengeV1{}, ErrOfflineTerminalChallengeConflict
		}
		return snapshot.challenge, nil
	}
	nonce, err := offline.GenerateTerminalChallengeNonce()
	if err != nil {
		return offline.TerminalChallengeV1{}, err
	}
	now := clock().UTC()
	challenge, err := offline.NewTerminalChallengeV1(snapshot.grant, verifierSubject, observerSubject,
		snapshot.trust.KeyID, snapshot.trust.Generation, terminalObserverPublicKeySHA256(snapshot.trust),
		snapshot.binding.DispatchNonceSHA256, planID, nonce, now.UTC())
	if err != nil {
		return offline.TerminalChallengeV1{}, err
	}
	// The persisted issue time is authority-owned. An injectable clock is used
	// only by store tests; no route accepts caller-supplied timestamps. Refresh
	// it immediately before CAS so slow preflight cannot backdate issuance.
	if decisionTime := clock().UTC(); decisionTime.Before(challenge.IssuedAt) ||
		!decisionTime.Before(challenge.ExpiresAt) {
		return offline.TerminalChallengeV1{}, offline.ErrTerminalAttestation
	}
	encoded, err := json.Marshal(challenge)
	if err != nil {
		return offline.TerminalChallengeV1{}, err
	}
	compares := make([]clientv3.Cmp, 0, len(snapshot.keys))
	for i, key := range snapshot.keys {
		if i == challengeRunnerAttempts {
			compares = append(compares, clientv3.Compare(clientv3.CreateRevision(key).WithPrefix(), "=", 0))
		} else {
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(key), "=", snapshot.revisions[i]))
		}
	}
	result, err := s.kv.Txn(ctx).If(compares...).Then(
		clientv3.OpPut(s.offlineTerminalChallengeKey(planID), string(encoded)),
		clientv3.OpPut(s.offlineTerminalChallengeEventKey(planID), string(encoded)),
	).Commit()
	if err != nil {
		return offline.TerminalChallengeV1{}, err
	}
	if !result.Succeeded {
		// A winning concurrent request may have committed. Recover only the
		// exact same attempt, grant, and principals; all other races fail.
		recovered, readErr := s.readTerminalChallengeSnapshot(ctx, planID)
		if readErr == nil && recovered.hasChallenge && recovered.grant.AttemptID == attemptID && recovered.grant.ID == grantID &&
			recovered.challenge.VerifierSubject == verifierSubject && recovered.challenge.ObserverSubject == observerSubject {
			return recovered.challenge, nil
		}
		return offline.TerminalChallengeV1{}, ErrOfflineTerminalChallengeConflict
	}
	return challenge, nil
}
