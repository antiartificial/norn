package etcdstore

// This file mirrors store/fleet_targets.go on etcd (docs/v3/fleet-controller/
// plan.md, §2.2, WP4): the target registry, targets, immutable aliases, the
// per-target mutation fence and the singleton authority epoch. PG locks rows
// in a fixed order inside one transaction (store/fleet_targets.go's header
// comment); etcd has no equivalent cross-call lock, so every method here
// instead returns the ModRevision it read, and a caller that composes several
// reads into one mutation (as the race test below does, standing in for
// WP8b) puts every one of those ModRevisions into a single Txn's compares.
// Either all of them still hold at commit time, or the whole mutation is
// rejected and must be re-decided from fresh reads (never retried blindly:
// T4).
//
// Key layout, relative to s.prefix (plan.md §2.2):
//
//	/v3/fleet-target-registry                   JSON {"generation": N}; a
//	                                             missing key means
//	                                             generation 0, i.e. no target
//	                                             has ever been registered
//	                                             (Q1) -- etcd has no
//	                                             migration step to pre-seed
//	                                             this the way PG's migration
//	                                             48 does.
//	/v3/fleet-targets/<id>                       JSON FleetTarget, including
//	                                             its Aliases. Aliases are
//	                                             also stored at their own key
//	                                             below for O(1) conflict
//	                                             detection; the two are
//	                                             always written in the same
//	                                             Txn, so this is a
//	                                             denormalized read path, not
//	                                             a second ledger (m18-style).
//	/v3/fleet-target-aliases/<sha256(alias)>     the owning target ID as a
//	                                             plain string. Hashed because
//	                                             an alias ("cluster:prod")
//	                                             may contain characters that
//	                                             are awkward etcd key
//	                                             segments; created with
//	                                             CreateRevision==0, and never
//	                                             overwritten once claimed
//	                                             (aliases are immutable in
//	                                             this pass).
//	/v3/fleet-target-fences/<id>                 JSON fence record. Created
//	                                             by registration in the free
//	                                             state, atomically with the
//	                                             target (same invariant as
//	                                             PG): GetFleetTargetFence
//	                                             treats a missing row as an
//	                                             error, never as free, so two
//	                                             acquirers can never race an
//	                                             insert.
//	/v3/fleet-target-abandoned/<planID>          JSON abandoned-plan record
//	                                             (written by WP9a; H7: this
//	                                             also covers plans on
//	                                             unregistered clusters). WP4
//	                                             only reads this key, from the
//	                                             registration in-flight scan.
//	/v3/fleet-authority-epoch                    a bare decimal string (m8).
//	                                             A missing key means epoch 1
//	                                             (the PG migration's seeded
//	                                             initial value); there is no
//	                                             reason/activated_at field
//	                                             the way PG's column has one,
//	                                             matching the key layout
//	                                             plan.md §2.2 specifies.
//
// Registration's in-flight scan (M1) mirrors fleetTargetInFlightSQL in
// store/fleet_targets.go: it reads every dispatch preparation (which exists
// for both the "prepared" and "bound" PG-equivalent states, since a binding
// is only ever created once its preparation already exists and is never
// deleted), resolves each one's plan cluster and environment, skips it if
// abandoned or if its latest runner attempt succeeded, and otherwise counts
// it as in flight. The registration Txn then compares the ModRevision of the
// whole preparations prefix, and of the bindings prefix, against the
// revision the scan observed (plan.md §2.2's "etcd: the registration txn
// compares ModRevision(prefix).WithPrefix() of the preparations and
// bindings prefixes against the read revision"): any key created or changed
// under either prefix between the scan and the commit fails the Txn, so a
// plan that newly went in flight during registration can never slip through.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet/lifecycle"
)

// ErrFleetAuthorityEpochConflict is returned by AdvanceFleetAuthorityEpoch
// when expected no longer matches the stored epoch (a lost CAS race). It
// mirrors store.ErrFleetAuthorityEpochConflict but is this backend's own
// sentinel, consistent with ErrNotFound and ErrFleetGitHubDispatchBound above.
var ErrFleetAuthorityEpochConflict = errors.New("fleet authority epoch changed")

// FleetTarget is the durable identity of one registered target plus its
// immutable aliases. It mirrors store.FleetTarget.
type FleetTarget struct {
	TargetID                string
	Provider                string
	ProviderAccount         string
	StateBackend            string
	CreatedAt               time.Time
	RegistrationOperationID string
	Aliases                 []string
}

// v3FleetTargetRegistry is the registry singleton's stored value.
type v3FleetTargetRegistry struct {
	Generation int64 `json:"generation"`
}

// v3FleetTargetFence is one target's stored fence row. Field names mirror
// store/fleet_targets_migration.go's fleet_target_fences columns. It stores
// no attempt ID (m18): holder attempts are always re-derived from
// ListFleetRunnerAttempts, exactly as on PG.
type v3FleetTargetFence struct {
	Generation        int64      `json:"generation"`
	Held              bool       `json:"held"`
	HolderPlanID      string     `json:"holderPlanId"`
	HolderNonceSHA256 string     `json:"holderNonceSha256"`
	AuthorityEpoch    int64      `json:"authorityEpoch"`
	LastReleasePlanID string     `json:"lastReleasePlanId"`
	LastReleaseReason string     `json:"lastReleaseReason"`
	LastReleaseAt     *time.Time `json:"lastReleaseAt,omitempty"`
	Revision          int64      `json:"revision"`
}

// v3FleetTargetAbandoned is one break-glass abandon record (B1/H7). WP4
// only reads this key; WP9a writes it.
type v3FleetTargetAbandoned struct {
	PlanID      string    `json:"planId"`
	NonceSHA256 string    `json:"nonceSha256"`
	TargetID    string    `json:"targetId"`
	OperationID string    `json:"operationId"`
	AbandonedAt time.Time `json:"abandonedAt"`
}

func (s *V3OperationStore) fleetTargetRegistryKey() string {
	return s.prefix + "/v3/fleet-target-registry"
}
func (s *V3OperationStore) fleetTargetKey(targetID string) string {
	return s.prefix + "/v3/fleet-targets/" + targetID
}
func (s *V3OperationStore) fleetTargetAliasKey(alias string) string {
	sum := sha256.Sum256([]byte(alias))
	return s.prefix + "/v3/fleet-target-aliases/" + hex.EncodeToString(sum[:])
}
func (s *V3OperationStore) fleetTargetFenceKey(targetID string) string {
	return s.prefix + "/v3/fleet-target-fences/" + targetID
}
func (s *V3OperationStore) fleetTargetAbandonedKey(planID string) string {
	return s.prefix + "/v3/fleet-target-abandoned/" + planID
}
func (s *V3OperationStore) fleetAuthorityEpochKey() string {
	return s.prefix + "/v3/fleet-authority-epoch"
}

// fleetGitHubDispatchPreparationsPrefix and fleetRunnerDispatchesPrefix name
// the two prefixes the registration in-flight scan reads and the compares
// above guard. They must stay literally in sync with
// fleetGitHubDispatchPreparationKey (v3_fleet_github_dispatch.go) and
// fleetRunnerDispatchKey (v3_fleet_runner_attempt.go).
func (s *V3OperationStore) fleetGitHubDispatchPreparationsPrefix() string {
	return s.prefix + "/v3/fleet-github-dispatch-preparations/"
}
func (s *V3OperationStore) fleetRunnerDispatchesPrefix() string {
	return s.prefix + "/v3/fleet-runner-dispatches/"
}

// RegisterFleetTarget registers identity with the given aliases, attributed
// to operationID. It is idempotent: replaying an identical identity and
// alias set returns the existing target without bumping the registry
// generation again (it performs no write at all in that case). Aliases are
// immutable: an alias already bound to a different target refuses as
// lifecycle.CodeFleetTargetAliasConflict. Registration refuses
// (lifecycle.CodeFleetTargetRegistrationInFlight) while any plan whose
// cluster or environment would resolve to the new target is in flight (M1).
//
// A Txn that fails its compares committed nothing, so it is re-decided from
// fresh reads (a bounded number of times) rather than surfaced: this is what
// PG's registry FOR UPDATE gives a concurrent registration for free (the
// loser waits, then sees the winner's rows). A Commit error, by contrast, is
// indeterminate and is returned as-is (T4).
func (s *V3OperationStore) RegisterFleetTarget(ctx context.Context, identity lifecycle.TargetIdentity, aliases []string, operationID string) (*FleetTarget, error) {
	canon, targetID, err := lifecycle.CanonicalTarget(identity)
	if err != nil {
		return nil, err
	}
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, fmt.Errorf("fleet target registration requires an operation ID")
	}
	clusterNames, environments, unique, err := classifyFleetTargetAliases(aliases)
	if err != nil {
		return nil, err
	}
	for range fleetTargetRegisterAttempts {
		target, committed, err := s.registerFleetTargetOnce(ctx, canon, targetID, operationID, clusterNames, environments, unique)
		if err != nil || committed {
			return target, err
		}
	}
	return nil, fmt.Errorf("fleet target registration changed concurrently")
}

const fleetTargetRegisterAttempts = 8

// registerFleetTargetOnce is one read-decide-commit round of
// RegisterFleetTarget. committed=false with a nil error means the Txn's
// compares failed and nothing was written.
func (s *V3OperationStore) registerFleetTargetOnce(ctx context.Context, canon lifecycle.TargetIdentity, targetID, operationID string, clusterNames, environments, unique []string) (*FleetTarget, bool, error) {
	target, plan, err := s.prepareFleetTargetRegistration(ctx, canon, targetID, operationID, clusterNames, environments, unique)
	if err != nil {
		return nil, false, err
	}
	if len(plan.puts) == 0 {
		return target, true, nil
	}
	txn, err := s.kv.Txn(ctx).If(plan.compares...).Then(plan.puts...).Commit()
	if err != nil {
		return nil, false, err
	}
	return target, txn.Succeeded, nil
}

// fleetTargetMutationPlan is the compares and puts of one fence-domain
// mutation, decided from fresh reads but not yet committed, so the signed
// acceptance (WP9a) can commit them in its own Txn.
type fleetTargetMutationPlan struct {
	compares []clientv3.Cmp
	puts     []clientv3.Op
}

// prepareFleetTargetRegistration is the read-decide half of one registration
// round. An empty plan means an idempotent replay: nothing to write.
func (s *V3OperationStore) prepareFleetTargetRegistration(ctx context.Context, canon lifecycle.TargetIdentity, targetID, operationID string, clusterNames, environments, unique []string) (*FleetTarget, fleetTargetMutationPlan, error) {
	none := fleetTargetMutationPlan{}
	registryKey := s.fleetTargetRegistryKey()
	registryResponse, err := s.kv.Get(ctx, registryKey)
	if err != nil {
		return nil, none, err
	}
	var generation, registryRevision int64
	if len(registryResponse.Kvs) == 1 {
		var registry v3FleetTargetRegistry
		if err := decodeV3Record(registryResponse.Kvs[0].Value, &registry); err != nil {
			return nil, none, fmt.Errorf("fleet target registry is corrupt: %w", err)
		}
		generation, registryRevision = registry.Generation, registryResponse.Kvs[0].ModRevision
	} else if err := s.fleetTargetRegistryMissingGuard(ctx); err != nil {
		return nil, none, err
	}

	// The epoch key is seeded ("1", the value a missing key already reads
	// as) by the first registration, so from then on a missing epoch key is
	// an error instead of silently reading as 1 again: deleting it can never
	// move the epoch backwards and revive a superseded fence.
	epochKey := s.fleetAuthorityEpochKey()
	epochResponse, err := s.kv.Get(ctx, epochKey)
	if err != nil {
		return nil, none, err
	}
	seedEpoch := len(epochResponse.Kvs) == 0

	targetKey := s.fleetTargetKey(targetID)
	targetResponse, err := s.kv.Get(ctx, targetKey)
	if err != nil {
		return nil, none, err
	}
	existing := len(targetResponse.Kvs) == 1
	var target FleetTarget
	var targetRevision int64
	if existing {
		if err := decodeV3Record(targetResponse.Kvs[0].Value, &target); err != nil {
			return nil, none, fmt.Errorf("fleet target is corrupt: %w", err)
		}
		targetRevision = targetResponse.Kvs[0].ModRevision
	} else {
		target = FleetTarget{
			TargetID: targetID, Provider: canon.Provider, ProviderAccount: canon.ProviderAccount,
			StateBackend: canon.StateBackend, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
			RegistrationOperationID: operationID,
		}
	}

	// Resolve every alias's current owner before writing anything, so a
	// conflict partway through never leaves a half-registered target.
	var newAliases []string
	aliasCompares := make([]clientv3.Cmp, 0, len(unique))
	for _, alias := range unique {
		key := s.fleetTargetAliasKey(alias)
		response, err := s.kv.Get(ctx, key)
		if err != nil {
			return nil, none, err
		}
		if len(response.Kvs) == 0 {
			newAliases = append(newAliases, alias)
			aliasCompares = append(aliasCompares, clientv3.Compare(clientv3.CreateRevision(key), "=", 0))
			continue
		}
		if owner := string(response.Kvs[0].Value); owner != targetID {
			return nil, none, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetAliasConflict}
		}
		// Already bound to this target; aliases are immutable, so this is
		// only a guard against an impossible concurrent rewrite.
		aliasCompares = append(aliasCompares, clientv3.Compare(clientv3.ModRevision(key), "=", response.Kvs[0].ModRevision))
	}

	if existing && len(newAliases) == 0 {
		// Idempotent replay: nothing changed, so nothing is written and the
		// registry generation does not bump again.
		return &target, none, nil
	}

	inFlightCompares, inFlight, err := s.fleetTargetInFlightCompares(ctx, clusterNames, environments)
	if err != nil {
		return nil, none, err
	}
	if inFlight {
		return nil, none, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetRegistrationInFlight}
	}

	compares := make([]clientv3.Cmp, 0, 7+len(aliasCompares)+len(inFlightCompares))
	compares = append(compares, clientv3.Compare(clientv3.ModRevision(registryKey), "=", registryRevision))
	if existing {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(targetKey), "=", targetRevision))
	} else {
		compares = append(compares,
			clientv3.Compare(clientv3.CreateRevision(targetKey), "=", 0),
			clientv3.Compare(clientv3.CreateRevision(s.fleetTargetFenceKey(targetID)), "=", 0))
	}
	if seedEpoch {
		compares = append(compares, clientv3.Compare(clientv3.CreateRevision(epochKey), "=", 0))
	}
	compares = append(compares, aliasCompares...)
	compares = append(compares, inFlightCompares...)

	target.Aliases = mergeFleetTargetAliases(target.Aliases, newAliases)
	targetRecord, err := json.Marshal(target)
	if err != nil {
		return nil, none, err
	}
	puts := make([]clientv3.Op, 0, 4+len(newAliases))
	puts = append(puts, clientv3.OpPut(targetKey, string(targetRecord)))
	for _, alias := range newAliases {
		puts = append(puts, clientv3.OpPut(s.fleetTargetAliasKey(alias), targetID))
	}
	if !existing {
		// The free fence row is created with the target, so every later
		// acquire locks an existing row instead of racing a create (two
		// acquirers both reading "no row" would otherwise both decide the
		// fence is free).
		fenceRecord, err := json.Marshal(v3FleetTargetFence{})
		if err != nil {
			return nil, none, err
		}
		puts = append(puts, clientv3.OpPut(s.fleetTargetFenceKey(targetID), string(fenceRecord)))
	}
	nextRegistry, err := json.Marshal(v3FleetTargetRegistry{Generation: generation + 1})
	if err != nil {
		return nil, none, err
	}
	puts = append(puts, clientv3.OpPut(registryKey, string(nextRegistry)))
	if seedEpoch {
		puts = append(puts, clientv3.OpPut(epochKey, "1"))
	}

	return &target, fleetTargetMutationPlan{compares: compares, puts: puts}, nil
}

// fleetTargetInFlightCompares scans every dispatch preparation for a plan
// whose cluster or environment would resolve to the new target (M1,
// mirroring fleetTargetInFlightSQL), and returns the two prefix compares the
// caller's registration Txn must include so a plan that goes in flight
// after this scan, but before the Txn commits, cannot slip through.
func (s *V3OperationStore) fleetTargetInFlightCompares(ctx context.Context, clusterNames, environments []string) ([]clientv3.Cmp, bool, error) {
	preparationPrefix := s.fleetGitHubDispatchPreparationsPrefix()
	bindingPrefix := s.fleetRunnerDispatchesPrefix()
	preparations, err := s.kv.Get(ctx, preparationPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, false, err
	}
	bindings, err := s.kv.Get(ctx, bindingPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, false, err
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(preparationPrefix).WithPrefix(), "<", preparations.Header.Revision+1),
		clientv3.Compare(clientv3.ModRevision(bindingPrefix).WithPrefix(), "<", bindings.Header.Revision+1),
	}

	clusterSet := make(map[string]bool, len(clusterNames))
	for _, cluster := range clusterNames {
		clusterSet[cluster] = true
	}
	environmentSet := make(map[string]bool, len(environments))
	for _, environment := range environments {
		environmentSet[environment] = true
	}

	for _, kv := range preparations.Kvs {
		var preparation FleetGitHubDispatchPreparation
		if err := decodeV3Record(kv.Value, &preparation); err != nil {
			return nil, false, fmt.Errorf("fleet GitHub dispatch preparation is corrupt: %w", err)
		}
		if preparation.PlanID == "" {
			continue
		}
		matches := environmentSet[preparation.FleetEnvironment]
		if !matches {
			cluster, err := s.fleetCapacityPlanCluster(ctx, preparation.PlanID)
			if err != nil {
				return nil, false, err
			}
			matches = clusterSet[cluster]
		}
		if !matches {
			continue
		}
		abandoned, err := s.fleetTargetPlanAbandoned(ctx, preparation.PlanID)
		if err != nil {
			return nil, false, err
		}
		if abandoned {
			// H7: an abandoned plan is never in flight, including on an
			// unregistered cluster.
			continue
		}
		succeeded, err := s.fleetTargetLatestAttemptSucceeded(ctx, preparation.PlanID)
		if err != nil {
			return nil, false, err
		}
		if !succeeded {
			return compares, true, nil
		}
	}
	return compares, false, nil
}

func (s *V3OperationStore) fleetCapacityPlanCluster(ctx context.Context, planID string) (string, error) {
	record, _, err := s.load(ctx, planID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	cluster, _ := record.Operation.Payload["cluster"].(string)
	return cluster, nil
}

func (s *V3OperationStore) fleetTargetPlanAbandoned(ctx context.Context, planID string) (bool, error) {
	response, err := s.kv.Get(ctx, s.fleetTargetAbandonedKey(planID))
	if err != nil {
		return false, err
	}
	return len(response.Kvs) == 1, nil
}

func (s *V3OperationStore) fleetTargetLatestAttemptSucceeded(ctx context.Context, planID string) (bool, error) {
	attempts, _, err := s.listFleetRunnerAttempts(ctx, planID)
	if err != nil {
		return false, err
	}
	if len(attempts) == 0 {
		return false, nil
	}
	return attempts[len(attempts)-1].Status == "succeeded", nil
}

// fleetTargetRegistryMissingGuard fails closed when the registry key is
// absent but targets exist. PG's registry row is seeded by migration 48, so
// a missing row there is a query error; on etcd a missing key is the normal
// never-registered state (generation 0), and this check keeps a deleted
// registry key from silently reading as "empty" (every admission unfenced)
// or restarting the generation at 1.
func (s *V3OperationStore) fleetTargetRegistryMissingGuard(ctx context.Context) error {
	response, err := s.kv.Get(ctx, s.prefix+"/v3/fleet-targets/", clientv3.WithPrefix(), clientv3.WithCountOnly())
	if err != nil {
		return err
	}
	if response.Count != 0 {
		return fmt.Errorf("fleet target registry key is missing while %d targets are registered", response.Count)
	}
	return nil
}

func classifyFleetTargetAliases(aliases []string) (clusterNames, environments, unique []string, err error) {
	seen := make(map[string]bool, len(aliases))
	for _, alias := range aliases {
		kind, value, ok := lifecycle.ParseAlias(alias)
		if !ok {
			return nil, nil, nil, fmt.Errorf("fleet target alias %q is not a recognized kind", alias)
		}
		if seen[alias] {
			continue
		}
		seen[alias] = true
		unique = append(unique, alias)
		switch kind {
		case lifecycle.AliasKindCluster:
			clusterNames = append(clusterNames, value)
		case lifecycle.AliasKindEnvironment:
			environments = append(environments, value)
		}
	}
	return clusterNames, environments, unique, nil
}

func mergeFleetTargetAliases(existing, add []string) []string {
	if len(add) == 0 {
		return existing
	}
	merged := append(append([]string{}, existing...), add...)
	sort.Strings(merged)
	return merged
}

// GetFleetTarget returns targetID's identity and aliases, or nil if it has
// never been registered.
func (s *V3OperationStore) GetFleetTarget(ctx context.Context, targetID string) (*FleetTarget, error) {
	response, err := s.kv.Get(ctx, s.fleetTargetKey(targetID))
	if err != nil {
		return nil, err
	}
	if len(response.Kvs) == 0 {
		return nil, nil
	}
	var target FleetTarget
	if err := decodeV3Record(response.Kvs[0].Value, &target); err != nil {
		return nil, fmt.Errorf("fleet target is corrupt: %w", err)
	}
	return &target, nil
}

// ResolveFleetTargetForPlan resolves cluster's alias and, if environment is
// non-empty, cross-checks the environment alias for the dispatch lane
// (plan.md §2.2's two-step resolution). It returns the registry key's
// ModRevision at the moment of the read, so a caller composing a larger Txn
// (WP8b) can compare it alongside the fence and epoch revisions (T1): the
// registry-empty decision and alias resolution are then still valid at
// commit time, or the Txn is rejected.
//
// registryEmpty=true means the registry has never had a target registered;
// callers must then behave exactly as they did before this change, and
// targetID is always "". A non-empty registry with no matching cluster
// alias returns a *lifecycle.FenceError with Code
// lifecycle.CodeFleetTargetUnregistered. A cluster alias and an environment
// alias that resolve to different targets return
// lifecycle.CodeFleetTargetAliasConflict.
func (s *V3OperationStore) ResolveFleetTargetForPlan(ctx context.Context, cluster, environment string) (targetID string, registryEmpty bool, registryRevision int64, err error) {
	registryKey := s.fleetTargetRegistryKey()
	response, err := s.kv.Get(ctx, registryKey)
	if err != nil {
		return "", false, 0, err
	}
	if len(response.Kvs) == 0 {
		if err := s.fleetTargetRegistryMissingGuard(ctx); err != nil {
			return "", false, 0, err
		}
		return "", true, 0, nil
	}
	var registry v3FleetTargetRegistry
	if err := decodeV3Record(response.Kvs[0].Value, &registry); err != nil {
		return "", false, 0, fmt.Errorf("fleet target registry is corrupt: %w", err)
	}
	revision := response.Kvs[0].ModRevision
	if registry.Generation == 0 {
		return "", true, revision, nil
	}
	clusterResponse, err := s.kv.Get(ctx, s.fleetTargetAliasKey("cluster:"+cluster))
	if err != nil {
		return "", false, 0, err
	}
	if len(clusterResponse.Kvs) == 0 {
		return "", false, revision, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetUnregistered}
	}
	clusterTarget := string(clusterResponse.Kvs[0].Value)
	if environment != "" {
		environmentResponse, err := s.kv.Get(ctx, s.fleetTargetAliasKey("environment:"+environment))
		if err != nil {
			return "", false, 0, err
		}
		if len(environmentResponse.Kvs) == 1 {
			if envTarget := string(environmentResponse.Kvs[0].Value); envTarget != clusterTarget {
				return "", false, revision, &lifecycle.FenceError{Code: lifecycle.CodeFleetTargetAliasConflict}
			}
		}
	}
	return clusterTarget, false, revision, nil
}

// GetFleetTargetFence reads one target's fence row. A missing row means the
// target is not registered and is an error (fail closed): returning a free
// zero value here would let two acquirers race a create. It returns the
// row's ModRevision for the same reason ResolveFleetTargetForPlan does.
func (s *V3OperationStore) GetFleetTargetFence(ctx context.Context, targetID string) (lifecycle.FenceFacts, int64, error) {
	response, err := s.kv.Get(ctx, s.fleetTargetFenceKey(targetID))
	if err != nil {
		return lifecycle.FenceFacts{}, 0, err
	}
	if len(response.Kvs) == 0 {
		return lifecycle.FenceFacts{}, 0, fmt.Errorf("fleet target %q has no fence row", targetID)
	}
	var stored v3FleetTargetFence
	if err := decodeV3Record(response.Kvs[0].Value, &stored); err != nil {
		return lifecycle.FenceFacts{}, 0, fmt.Errorf("fleet target fence is corrupt: %w", err)
	}
	facts := lifecycle.FenceFacts{
		TargetID: targetID, Generation: stored.Generation, Held: stored.Held,
		HolderPlanID: stored.HolderPlanID, HolderNonceSHA256: stored.HolderNonceSHA256,
		AuthorityEpoch: stored.AuthorityEpoch, Revision: stored.Revision,
	}
	if stored.LastReleaseReason != "" {
		at := time.Time{}
		if stored.LastReleaseAt != nil {
			at = *stored.LastReleaseAt
		}
		facts.LastRelease = &lifecycle.Release{PlanID: stored.LastReleasePlanID, Reason: stored.LastReleaseReason, At: at}
	}
	return facts, response.Kvs[0].ModRevision, nil
}

// fleetTargetFenceRecord converts fence facts to their stored shape. It is
// used by the race test below, standing in for WP8b's real acquire write.
func fleetTargetFenceRecord(f lifecycle.FenceFacts) v3FleetTargetFence {
	record := v3FleetTargetFence{
		Generation: f.Generation, Held: f.Held, HolderPlanID: f.HolderPlanID,
		HolderNonceSHA256: f.HolderNonceSHA256, AuthorityEpoch: f.AuthorityEpoch, Revision: f.Revision,
	}
	if f.LastRelease != nil {
		at := f.LastRelease.At
		record.LastReleasePlanID, record.LastReleaseReason, record.LastReleaseAt = f.LastRelease.PlanID, f.LastRelease.Reason, &at
	}
	return record
}

// fleetAuthorityEpochWithRevision is FleetAuthorityEpoch plus the key's
// ModRevision, for callers composing a larger Txn (WP8b).
func (s *V3OperationStore) fleetAuthorityEpochWithRevision(ctx context.Context) (int64, int64, error) {
	response, err := s.kv.Get(ctx, s.fleetAuthorityEpochKey())
	if err != nil {
		return 0, 0, err
	}
	if len(response.Kvs) == 0 {
		// No migration step seeds this on etcd (H2/m8); an unwritten key is
		// the same initial value PG's migration seeds: epoch 1. The first
		// registration writes the key, so once the registry exists a missing
		// epoch key means it was deleted and is an error, never epoch 1
		// again (which would let a superseded fence's epoch become current).
		registry, err := s.kv.Get(ctx, s.fleetTargetRegistryKey(), clientv3.WithCountOnly())
		if err != nil {
			return 0, 0, err
		}
		if registry.Count != 0 {
			return 0, 0, fmt.Errorf("fleet authority epoch key is missing while the target registry exists")
		}
		if err := s.fleetTargetRegistryMissingGuard(ctx); err != nil {
			return 0, 0, err
		}
		return 1, 0, nil
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(string(response.Kvs[0].Value)), 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("fleet authority epoch is corrupt: %w", err)
	}
	return epoch, response.Kvs[0].ModRevision, nil
}

// FleetAuthorityEpoch reads the singleton authority epoch.
func (s *V3OperationStore) FleetAuthorityEpoch(ctx context.Context) (int64, error) {
	epoch, _, err := s.fleetAuthorityEpochWithRevision(ctx)
	return epoch, err
}

// AdvanceFleetAuthorityEpoch CASes the singleton epoch forward from
// expected. Advancing touches no fence or attempt row: old-epoch fences
// become Uncertain/AuthoritySuperseded until they are re-bound or released
// (plan.md §2.2), not rewritten here. reason is accepted for parity with
// PG's AdvanceFleetAuthorityEpoch, but the etcd value is a bare decimal
// string with nowhere to record it (m8's key layout).
func (s *V3OperationStore) AdvanceFleetAuthorityEpoch(ctx context.Context, expected int64, reason string) (int64, error) {
	_ = reason
	epoch, revision, err := s.fleetAuthorityEpochWithRevision(ctx)
	if err != nil {
		return 0, err
	}
	if epoch != expected {
		return 0, ErrFleetAuthorityEpochConflict
	}
	next := epoch + 1
	txn, err := s.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", revision)).
		Then(clientv3.OpPut(s.fleetAuthorityEpochKey(), strconv.FormatInt(next, 10))).
		Commit()
	if err != nil {
		return 0, err
	}
	if !txn.Succeeded {
		return 0, ErrFleetAuthorityEpochConflict
	}
	return next, nil
}
