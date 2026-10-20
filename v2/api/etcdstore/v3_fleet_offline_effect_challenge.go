package etcdstore

// This source-only target-etcd ledger has no mounted HTTP route and performs
// no provider effect. It retains one immutable challenge for each purpose.
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

var ErrOfflineEffectChallengeConflict = errors.New("offline effect challenge changed or authority is stale")

func validEffectChallengePurpose(purpose string) bool {
	return purpose == offline.EffectIssuePurpose || purpose == offline.EffectConsumePurpose
}
func (s *V3OperationStore) offlineEffectChallengeKey(planID, purpose string) string {
	return s.prefix + "/v3/fleet-offline-effect-challenges/" + planID + "/" + purpose
}
func (s *V3OperationStore) offlineEffectChallengeEventKey(planID, purpose string) string {
	return s.prefix + "/v3/fleet-offline-effect-challenge-events/" + planID + "/" + purpose + "/00000000000000000001"
}

// effectChallengeSnapshot is one revision-pinned read. The first probe only
// selects the dynamic owner and target keys; it is never accepted as evidence.
type effectChallengeSnapshot struct {
	keys         []string
	raw          [][]byte
	revisions    []int64
	grant        offline.EffectGrant
	binding      offline.Binding
	trust        terminalObserverTrust
	aliases      []string
	challenge    offline.EffectChallengeV1
	attempt      offline.Attempt
	epoch        int64
	hasChallenge bool
}

const (
	effectChallengeHead = iota
	effectChallengeEvent
	effectChallengeGrant
	effectChallengeIssued
	effectChallengeConsumed
	effectChallengeAttempt
	effectChallengeBinding
	effectChallengeCheckpoint
	effectChallengeCheckpointEvent
	effectChallengeOwner
	effectChallengePlan
	effectChallengeRegistry
	effectChallengeFirstAlias
	effectChallengeSecondAlias
	effectChallengeTarget
	effectChallengeFence
	effectChallengeEpoch
	effectChallengeAbandoned
	effectChallengeGithubPrep
	effectChallengeRunnerDispatch
	effectChallengeRunnerAttempts
	effectChallengeTerminal
	effectChallengeTerminalEvent
	effectChallengeObserverTrust
)

func (s *V3OperationStore) readEffectChallengeSnapshot(ctx context.Context, planID, purpose string) (effectChallengeSnapshot, error) {
	var out effectChallengeSnapshot
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
		s.offlineEffectChallengeKey(planID, purpose), s.offlineEffectChallengeEventKey(planID, purpose),
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
		if i == effectChallengeRunnerAttempts {
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
	for _, i := range []int{effectChallengeAttempt, effectChallengeBinding,
		effectChallengeCheckpoint, effectChallengeCheckpointEvent, effectChallengeOwner, effectChallengePlan, effectChallengeRegistry,
		effectChallengeFirstAlias, effectChallengeSecondAlias, effectChallengeTarget, effectChallengeFence, effectChallengeEpoch} {
		if out.revisions[i] == 0 {
			return out, ErrOfflineAttemptCorrupt
		}
	}
	for i := effectChallengeObserverTrust + 1; i < len(out.keys); i++ {
		if out.revisions[i] == 0 || string(out.raw[i]) != candidate.TargetID {
			return out, ErrOfflineAttemptCorrupt
		}
	}
	for _, i := range []int{effectChallengeAbandoned, effectChallengeGithubPrep, effectChallengeRunnerDispatch, effectChallengeRunnerAttempts} {
		if out.revisions[i] != 0 {
			return out, ErrOfflineEffectChallengeConflict
		}
	}
	if (out.revisions[effectChallengeTerminal] == 0) != (out.revisions[effectChallengeTerminalEvent] == 0) {
		return out, ErrOfflineAttemptCorrupt
	}
	if out.revisions[effectChallengeTerminal] != 0 {
		return out, ErrOfflineEffectChallengeConflict
	}
	if out.revisions[effectChallengeObserverTrust] == 0 {
		return out, ErrOfflineAttemptCorrupt
	}
	if out.trust, err = decodeTerminalObserverTrust(out.raw[effectChallengeObserverTrust]); err != nil {
		return out, err
	}
	binding, err := offline.DecodeBinding(out.raw[effectChallengeBinding])
	digest := sha256.Sum256(out.raw[effectChallengeBinding])
	if err != nil || binding.SchemaVersion != offline.BindingSchemaV2 || binding.PlanID != planID ||
		binding.TargetID != candidate.TargetID || binding.OwnerAuthorizationID != candidate.OwnerAuthorizationID {
		return out, ErrOfflineAttemptCorrupt
	}
	out.binding = binding
	attempt, err := decodeOfflineAttempt(out.raw[effectChallengeAttempt], planID)
	if err != nil || attempt.BindingSHA256 != hex.EncodeToString(digest[:]) ||
		attempt.PrincipalSubject != binding.RunnerPrincipalSubject || attempt.TargetID != binding.TargetID ||
		attempt.FenceGeneration != binding.FenceGeneration {
		return out, ErrOfflineAttemptCorrupt
	}
	out.attempt = attempt
	checkpoint, err := decodeOfflineCheckpointPair(out.raw[effectChallengeCheckpoint], out.raw[effectChallengeCheckpointEvent], planID)
	if err != nil || checkpoint.AttemptID != attempt.ID || checkpoint.Prechange == nil ||
		!checkpoint.Prechange.ProviderTopologyVerified || checkpoint.Prechange.BindingSHA256 != attempt.BindingSHA256 ||
		checkpoint.FenceGeneration != binding.FenceGeneration {
		return out, ErrOfflineAttemptCorrupt
	}
	if purpose == offline.EffectIssuePurpose {
		if out.revisions[effectChallengeGrant] != 0 || out.revisions[effectChallengeIssued] != 0 || out.revisions[effectChallengeConsumed] != 0 {
			return out, ErrOfflineEffectChallengeConflict
		}
	} else {
		grant, grantErr := decodeOfflineEffectGrantPair(out.raw[effectChallengeGrant], out.raw[effectChallengeIssued], out.raw[effectChallengeConsumed], planID)
		if grantErr != nil || grant.Phase != "issued" || grant.ConsumedAt != nil ||
			grant.AttemptID != attempt.ID || grant.BindingSHA256 != attempt.BindingSHA256 || grant.TargetID != binding.TargetID ||
			grant.FenceGeneration != binding.FenceGeneration || grant.SourceCommit != binding.SourceCommit ||
			grant.SavedPlanSHA256 != binding.SavedPlanSHA256 || grant.EffectSHA256 != binding.EffectSHA256 ||
			grant.Backend != binding.Backend || grant.ProviderAccountUUID != binding.ProviderAccountUUID ||
			grant.NodeName != binding.NodeName || grant.DropletID != binding.DropletID ||
			grant.PrechangeSHA256 != checkpoint.EvidenceSHA256 {
			return out, ErrOfflineEffectChallengeConflict
		}
		out.grant = grant
	}
	if string(out.raw[effectChallengeOwner]) != planID+":"+binding.OwnerEnvelopeSHA256 {
		return out, ErrOfflineAttemptCorrupt
	}
	var plan v3Record
	if decodeV3Record(out.raw[effectChallengePlan], &plan) != nil {
		return out, ErrOfflineAttemptCorrupt
	}
	var typed fleet.CapacityPlan
	if !validOfflineContractionPlan(plan.Operation, planID, &typed) {
		return out, ErrOfflineAttemptCorrupt
	}
	var registry v3FleetTargetRegistry
	if decodeV3Record(out.raw[effectChallengeRegistry], &registry) != nil || registry.Generation < 1 ||
		string(out.raw[effectChallengeFirstAlias]) != binding.TargetID || string(out.raw[effectChallengeSecondAlias]) != binding.TargetID {
		return out, ErrOfflineAttemptCorrupt
	}
	var target FleetTarget
	if decodeV3Record(out.raw[effectChallengeTarget], &target) != nil || target.TargetID != binding.TargetID ||
		target.Provider != "digitalocean" || target.ProviderAccount != binding.ProviderAccountUUID ||
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
	epoch, err := strconv.ParseInt(strings.TrimSpace(string(out.raw[effectChallengeEpoch])), 10, 64)
	if err != nil || epoch < 1 || checkpoint.AuthorityEpoch != epoch || (purpose == offline.EffectConsumePurpose && epoch != out.grant.AuthorityEpoch) {
		return out, ErrOfflineEffectChallengeConflict
	}
	var fence v3FleetTargetFence
	if decodeV3Record(out.raw[effectChallengeFence], &fence) != nil || !fence.Held || fence.HolderPlanID != planID ||
		fence.HolderNonceSHA256 != binding.DispatchNonceSHA256 || uint64(fence.Generation) != binding.FenceGeneration ||
		fence.AuthorityEpoch != epoch {
		return out, ErrOfflineEffectChallengeConflict
	}
	if out.revisions[effectChallengeHead] == 0 && out.revisions[effectChallengeEvent] == 0 {
		out.epoch = epoch
		return out, nil
	}
	if out.revisions[effectChallengeHead] == 0 || out.revisions[effectChallengeEvent] == 0 ||
		!bytes.Equal(out.raw[effectChallengeHead], out.raw[effectChallengeEvent]) {
		return out, ErrOfflineAttemptCorrupt
	}
	if decodeV3Record(out.raw[effectChallengeHead], &out.challenge) != nil ||
		out.challenge.Purpose != purpose || out.challenge.Validate(binding, attempt, epoch, out.challenge.IssuedAt) != nil ||
		(purpose == offline.EffectConsumePurpose && !effectChallengeMatchesGrant(out.challenge, out.grant)) ||
		out.challenge.ObserverSubject != out.trust.ObserverSubject || out.challenge.ObserverKeyID != out.trust.KeyID ||
		out.challenge.TrustGeneration != out.trust.Generation ||
		out.challenge.ObserverPublicKeySHA256 != terminalObserverPublicKeySHA256(out.trust) {
		return out, ErrOfflineEffectChallengeConflict
	}
	canonicalChallenge, err := json.Marshal(out.challenge)
	if err != nil || !bytes.Equal(canonicalChallenge, out.raw[effectChallengeHead]) {
		return out, ErrOfflineAttemptCorrupt
	}
	out.hasChallenge = true
	out.epoch = epoch
	return out, nil
}

func effectChallengeMatchesGrant(challenge offline.EffectChallengeV1, grant offline.EffectGrant) bool {
	raw, err := json.Marshal(grant)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(raw)
	return challenge.GrantID == grant.ID && challenge.GrantSHA256 == hex.EncodeToString(digest[:])
}

// GetOfflineEffectChallenge reads the immutable issue event independently of
// current eligibility. It works after grant transitions, trust rotation, or
// fence release and never authorizes accepting an attestation or an effect.
func (s *V3OperationStore) GetOfflineEffectChallenge(ctx context.Context, planID, purpose string) (offline.EffectChallengeV1, error) {
	if s == nil || uuid.Validate(planID) != nil || !validEffectChallengePurpose(purpose) {
		return offline.EffectChallengeV1{}, offline.ErrEffectAttestation
	}
	result, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineEffectAttestationKey(planID, purpose)),
		clientv3.OpGet(s.offlineEffectAttestationEventKey(planID, purpose)),
		clientv3.OpGet(s.offlineEffectChallengeConsumedKey(planID, purpose)),
		clientv3.OpGet(s.offlineEffectChallengeKey(planID, purpose)),
		clientv3.OpGet(s.offlineEffectChallengeEventKey(planID, purpose)),
		clientv3.OpGet(s.offlineEffectAcceptanceWatermarkKey(planID, purpose))).Commit()
	if err != nil {
		return offline.EffectChallengeV1{}, err
	}
	if len(result.Responses) != 6 {
		return offline.EffectChallengeV1{}, ErrOfflineAttemptCorrupt
	}
	var raw [6][]byte
	for i, item := range result.Responses {
		rows := item.GetResponseRange().Kvs
		if len(rows) > 1 {
			return offline.EffectChallengeV1{}, ErrOfflineAttemptCorrupt
		}
		if len(rows) == 1 {
			raw[i] = rows[0].Value
		}
	}
	challenge, acceptedHead, err := decodeEffectChallengeHead(raw[3], raw[4], planID, purpose)
	if err != nil {
		if err == ErrNotFound && (len(raw[0]) != 0 || len(raw[1]) != 0 || len(raw[2]) != 0 || len(raw[5]) != 0) {
			return offline.EffectChallengeV1{}, ErrOfflineAttemptCorrupt
		}
		return offline.EffectChallengeV1{}, err
	}
	if acceptedHead != nil {
		if _, err := verifyAcceptedEffectRows(raw, planID, purpose, challenge.ID); err != nil {
			return offline.EffectChallengeV1{}, err
		}
	} else if len(raw[0]) != 0 || len(raw[1]) != 0 || len(raw[2]) != 0 || len(raw[5]) != 0 {
		return offline.EffectChallengeV1{}, ErrOfflineAttemptCorrupt
	}
	return challenge, nil
}

// IssueOfflineEffectChallenge makes one immutable challenge under the exact
// bound attempt, checkpoint, owner, target, held fence, epoch, and pinned key.
// The consume purpose additionally requires the current unconsumed grant.
func (s *V3OperationStore) IssueOfflineEffectChallenge(ctx context.Context, planID, attemptID, purpose, observerSubject string) (offline.EffectChallengeV1, error) {
	return s.issueOfflineEffectChallengeWithClock(ctx, planID, attemptID, purpose, observerSubject, time.Now)
}
func (s *V3OperationStore) issueOfflineEffectChallengeWithClock(ctx context.Context, planID, attemptID, purpose, observerSubject string, clock func() time.Time) (offline.EffectChallengeV1, error) {
	if s == nil || clock == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || !validEffectChallengePurpose(purpose) {
		return offline.EffectChallengeV1{}, offline.ErrEffectAttestation
	}
	// Replay is historical recovery only. A caller cannot use this answer to
	// bypass the live fence/trust/grant checks required by Submit.
	if prior, readErr := s.GetOfflineEffectChallenge(ctx, planID, purpose); readErr == nil {
		if prior.AttemptID != attemptID || prior.ObserverSubject != observerSubject {
			return offline.EffectChallengeV1{}, ErrOfflineEffectChallengeConflict
		}
		return prior, nil
	} else if readErr != ErrNotFound {
		return offline.EffectChallengeV1{}, readErr
	}
	snapshot, err := s.readEffectChallengeSnapshot(ctx, planID, purpose)
	if err != nil {
		return offline.EffectChallengeV1{}, err
	}
	if snapshot.attempt.ID != attemptID || observerSubject != snapshot.trust.ObserverSubject {
		return offline.EffectChallengeV1{}, offline.ErrEffectAttestation
	}
	if snapshot.hasChallenge {
		return snapshot.challenge, nil
	}
	now := clock().UTC()
	if !now.Before(snapshot.binding.ExpiresAt) {
		return offline.EffectChallengeV1{}, offline.ErrEffectAttestation
	}
	nonce, err := offline.GenerateTerminalChallengeNonce()
	if err != nil {
		return offline.EffectChallengeV1{}, err
	}
	public, err := base64.RawURLEncoding.Strict().DecodeString(snapshot.trust.PublicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return offline.EffectChallengeV1{}, ErrOfflineAttemptCorrupt
	}
	var grant *offline.EffectGrant
	if purpose == offline.EffectConsumePurpose {
		grant = &snapshot.grant
	}
	challenge, err := offline.NewEffectChallengeV1(purpose, snapshot.binding, snapshot.attempt, snapshot.epoch,
		snapshot.trust.ObserverSubject, snapshot.trust.KeyID, snapshot.trust.Generation, ed25519.PublicKey(public), nonce, grant, now)
	if err != nil {
		return offline.EffectChallengeV1{}, err
	}
	if snapshot.binding.ExpiresAt.Before(challenge.ExpiresAt) {
		challenge.ExpiresAt = snapshot.binding.ExpiresAt
	}
	// A challenge never outlives the prechange evidence it depends on.
	var checkpoint offline.Checkpoint
	if decodeV3Record(snapshot.raw[effectChallengeCheckpoint], &checkpoint) != nil || checkpoint.Prechange == nil {
		return offline.EffectChallengeV1{}, ErrOfflineAttemptCorrupt
	}
	if checkpoint.Prechange.ExpiresAt.Before(challenge.ExpiresAt) {
		challenge.ExpiresAt = checkpoint.Prechange.ExpiresAt
	}
	if decisionTime := clock().UTC(); challenge.Validate(snapshot.binding, snapshot.attempt, snapshot.epoch, decisionTime) != nil {
		return offline.EffectChallengeV1{}, offline.ErrEffectAttestation
	}
	raw, err := json.Marshal(challenge)
	if err != nil {
		return offline.EffectChallengeV1{}, err
	}
	compares := make([]clientv3.Cmp, 0, len(snapshot.keys))
	for i, key := range snapshot.keys {
		if i == effectChallengeRunnerAttempts {
			compares = append(compares, clientv3.Compare(clientv3.CreateRevision(key).WithPrefix(), "=", 0))
		} else {
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(key), "=", snapshot.revisions[i]))
		}
	}
	result, err := s.kv.Txn(ctx).If(compares...).Then(
		clientv3.OpPut(s.offlineEffectChallengeKey(planID, purpose), string(raw)),
		clientv3.OpPut(s.offlineEffectChallengeEventKey(planID, purpose), string(raw)),
	).Commit()
	if err != nil {
		return offline.EffectChallengeV1{}, err
	}
	if !result.Succeeded {
		recovered, readErr := s.GetOfflineEffectChallenge(ctx, planID, purpose)
		if readErr == nil && recovered.AttemptID == attemptID && recovered.ObserverSubject == observerSubject {
			return recovered, nil
		}
		return offline.EffectChallengeV1{}, ErrOfflineEffectChallengeConflict
	}
	return challenge, nil
}
