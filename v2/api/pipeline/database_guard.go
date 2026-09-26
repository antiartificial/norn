package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

// Target-change guard. Staging revision-specific material does not fence
// database writers: a candidate job registered for a new target can write
// the new database as soon as it starts, while the running allocations still
// write the old one. Until the explicit database cutover lane (M6) supplies
// writer fencing and catch-up evidence, an ordinary deploy of a running app
// may not change the target of any database the app already uses. Changing
// only a credential (identical target identity) and rolling out new app code
// on unchanged targets are allowed.

// DatabaseBaselineKind records, by an authorized operator and after probing
// the targets, which named targets an app's running writers already use.
// It is the explicit evidence for a legacy-to-named transition (or any app
// whose running writers have no recorded targets); Norn cannot observe what
// a legacy allocation actually connects to, so this is an attestation plus
// an identity probe, not observation.
const DatabaseBaselineKind = "app.database-baseline"

// writerHistoryLimit bounds the history scan; history longer than this
// without a baseline is ambiguous and refused.
const writerHistoryLimit = 500

// writerSteps are deploy steps that cannot have produced a database writer
// (a failure at one of them leaves no migration or allocation behind).
var writerFreeSteps = map[string]bool{"clone": true, "admission": true, "build": true, "artifact-admission": true, "test": true, "snapshot": true}

// writerEvidence is every recorded target set that may belong to a live
// writer of the app: the latest baseline (a succeeded named deploy or a
// recorded baseline) plus later deploys that may have left writers (running,
// or failed/canceled at or after migration: partial rollouts, canaries).
type writerEvidence struct {
	sets []*recordedTargetSet
	// empty means no deploy or baseline ever ran and no job is registered.
	empty bool
	// ambiguous is set when some possible writers' targets are unknown.
	// sets still holds every writer target that is known, so a known
	// conflict is never hidden behind (or excused by) unknown history.
	ambiguous error
}

// errAmbiguousWriters marks history or runtime state from which Norn cannot
// establish which targets live writers use.
func ambiguousWriters(format string, args ...interface{}) error {
	return &DatabaseTargetError{Ambiguous: true, Reason: "running writers are ambiguous: " + fmt.Sprintf(format, args...) +
		"; record a verified baseline (" + DatabaseBaselineKind + ") or resolve the app's state first"}
}

// writerHistory establishes writer evidence for an app, scanning back to the
// latest baseline (a succeeded deploy or recorded baseline). Unknown writer
// targets mark the evidence ambiguous without discarding the known ones.
// The error result reports only failures to read history.
func (p *Pipeline) writerHistory(ctx context.Context, app, excludeOperationID string) (writerEvidence, error) {
	if p.DB == nil {
		return writerEvidence{}, fmt.Errorf("deployment history is unavailable")
	}
	operations, err := p.DB.ListOperations(ctx, store.OperationFilter{App: app, ExcludeID: excludeOperationID, Limit: writerHistoryLimit})
	if err != nil {
		return writerEvidence{}, fmt.Errorf("read deployment history: %w", err)
	}
	evidence := writerEvidence{}
	unknown := func(err error) {
		if evidence.ambiguous == nil {
			evidence.ambiguous = err
		}
	}
	for index := range operations {
		op := &operations[index]
		switch op.Kind {
		case DatabaseBaselineKind:
			if op.Status != model.OperationSucceeded {
				continue
			}
			set, err := recordedTargetSetFromPayload(op.Payload)
			if err != nil || set == nil {
				unknown(ambiguousWriters("baseline %s has no readable target set", op.ID))
			} else {
				evidence.sets = append(evidence.sets, set)
			}
			return evidence, nil
		case "app.deploy":
			switch {
			case op.Status == model.OperationSucceeded:
				set, err := recordedTargetSetFromPayload(op.Payload)
				if err != nil || set == nil {
					unknown(ambiguousWriters("the latest succeeded deploy %s has no recorded named targets (legacy or unbound writers)", op.ID))
				} else {
					evidence.sets = append(evidence.sets, set)
				}
				return evidence, nil
			case op.Status == model.OperationQueued:
				continue // not a writer yet; its own execution is guarded
			case deployWriterFree(op):
				continue
			default:
				set, err := recordedTargetSetFromPayload(op.Payload)
				if err != nil || set == nil {
					unknown(ambiguousWriters("deploy %s (%s) may have left writers without recorded named targets", op.ID, op.Status))
					continue
				}
				evidence.sets = append(evidence.sets, set)
			}
		}
	}
	if len(operations) >= writerHistoryLimit {
		unknown(ambiguousWriters("no baseline within the latest %d operations", writerHistoryLimit))
		return evidence, nil
	}
	// No baseline: writer-free, queued or partial history proves nothing about
	// jobs registered outside it (legacy or manual). The runtime must show
	// that no job of the app is registered; otherwise a baseline is needed.
	registered, err := p.appJobsRegistered(app)
	if err != nil {
		unknown(ambiguousWriters("runtime registration could not be checked (%v)", err))
	} else if registered != "" {
		unknown(ambiguousWriters("Nomad job %s is registered but no baseline records its writers' targets", registered))
	}
	evidence.empty = len(evidence.sets) == 0 && evidence.ambiguous == nil
	return evidence, nil
}

func deployWriterFree(op *model.Operation) bool {
	if op.Status == model.OperationCanceled && op.Attempts == 0 {
		return true
	}
	step, _ := op.Metadata["step"].(string)
	return (op.Status == model.OperationFailed || op.Status == model.OperationCanceled) && writerFreeSteps[step]
}

// appJobsRegistered returns the first of the app's Nomad job IDs that is
// registered in any region, or "".
func (p *Pipeline) appJobsRegistered(app string) (string, error) {
	if p.Nomad == nil {
		return "", fmt.Errorf("nomad is not connected")
	}
	spec, err := p.findSpec(app)
	ids := []string{app}
	regions := []model.ResolvedRegion{{NomadRegion: "global"}}
	if err == nil {
		ids = nomad.JobIDs(spec)
		regions = spec.ResolvedRegions()
	}
	for _, region := range regions {
		for _, id := range ids {
			registered, err := p.Nomad.JobRegistered(region.NomadRegion, id)
			if err != nil {
				return "", err
			}
			if registered {
				return id, nil
			}
		}
	}
	return "", nil
}

// requireRunningTargetsUnchanged refuses work that would let two different
// targets be written under the app's database contract. Against every
// possible live writer's recorded targets:
//   - a logical database may not change target;
//   - a newly added logical database is an independent addition only if no
//     running logical database was removed, or if it reuses a target a
//     running writer already uses (a pure rename). Removing one logical
//     database while adding another on a different target is a replacement
//     of running writers and is refused;
//   - ambiguous history or runtime state is refused (writerHistory), after
//     the known writer targets have been checked: a conflict with a known
//     writer is a definite refusal, never reported as mere ambiguity.
func (p *Pipeline) requireRunningTargetsUnchanged(ctx context.Context, app, excludeOperationID string, next []recordedNamedTarget) error {
	evidence, err := p.writerHistory(ctx, app, excludeOperationID)
	if err != nil || evidence.empty {
		return err
	}
	if err := knownTargetConflict(app, evidence, next); err != nil {
		return err
	}
	return evidence.ambiguous
}

func knownTargetConflict(app string, evidence writerEvidence, next []recordedNamedTarget) error {
	running := map[string][]database.TargetIdentity{}
	knownTargets := map[database.TargetIdentity]bool{}
	for _, set := range evidence.sets {
		for _, entry := range set.Targets {
			running[entry.Name] = append(running[entry.Name], entry.Target)
			knownTargets[entry.Target] = true
		}
	}
	nextNames := map[string]bool{}
	var added []recordedNamedTarget
	for _, entry := range next {
		nextNames[entry.Name] = true
		previous, used := running[entry.Name]
		if !used {
			added = append(added, entry)
			continue
		}
		for _, target := range previous {
			if target != entry.Target {
				return &DatabaseTargetError{Reason: fmt.Sprintf(
					"database %q of running app %s would move from %s (generation %d on %s/%d) to %s (generation %d on %s/%d); an ordinary deploy cannot move running writers — this requires the database cutover lane (M6), which is not implemented",
					entry.Name, app, target.BindingID, target.BindingGeneration, target.ServiceID, target.ServiceGeneration,
					entry.Target.BindingID, entry.Target.BindingGeneration, entry.Target.ServiceID, entry.Target.ServiceGeneration)}
			}
		}
	}
	var removed []string
	for name := range running {
		if !nextNames[name] {
			removed = append(removed, name)
		}
	}
	for _, entry := range added {
		if knownTargets[entry.Target] || len(removed) == 0 {
			continue
		}
		return &DatabaseTargetError{Reason: fmt.Sprintf(
			"adding database %q on %s while removing running database(s) %v replaces running writers rather than adding an independent database; this requires the database cutover lane (M6), which is not implemented",
			entry.Name, entry.Target.BindingID, removed)}
	}
	return nil
}

// runningTargets returns the single recorded target of each logical
// database across all possible live writers, refusing ambiguity.
func (p *Pipeline) runningTargets(ctx context.Context, app string) (map[string]database.TargetIdentity, error) {
	evidence, err := p.writerHistory(ctx, app, "")
	if err != nil {
		return nil, err
	}
	if evidence.ambiguous != nil {
		return nil, evidence.ambiguous
	}
	if evidence.empty {
		return nil, &DatabaseTargetError{Reason: fmt.Sprintf("app %s has no recorded named database targets", app)}
	}
	out := map[string]database.TargetIdentity{}
	for _, set := range evidence.sets {
		for _, entry := range set.Targets {
			if existing, ok := out[entry.Name]; ok && existing != entry.Target {
				return nil, ambiguousWriters("database %q has more than one possible live target", entry.Name)
			}
			out[entry.Name] = entry.Target
		}
	}
	return out, nil
}

// guardDeployTargets applies the target-change guard to an executing deploy
// using the targets it actually opened.
func (p *Pipeline) guardDeployTargets(ctx context.Context, st *state) error {
	if !st.spec.NamedDatabases() || st.database == nil {
		return nil
	}
	next := make([]recordedNamedTarget, 0, len(st.database.named))
	for name, bound := range st.database.named {
		next = append(next, recordedNamedTarget{Name: name, Target: bound.resolved.Target})
	}
	return p.requireRunningTargetsUnchanged(ctx, st.spec.App, st.claim.OperationID(), next)
}

// RunningDeliveryRevision returns the promoted delivery revision of one of
// the app's jobs after revalidating it: the revision must be staged for
// every runtime database, and each staged target identity must equal the
// target recorded by the app's latest succeeded deploy. Rollback, cron
// resubmission and function invocation reference only this revision, so they
// neither read mutable items nor substitute a target.
func (p *Pipeline) RunningDeliveryRevision(ctx context.Context, spec *model.InfraSpec, nomadRegion, jobID string) (nomad.DatabaseRevision, error) {
	if p.Nomad == nil {
		return nomad.DatabaseRevision{}, fmt.Errorf("nomad not connected")
	}
	expected, err := p.runningTargets(ctx, spec.App)
	if err != nil {
		return nomad.DatabaseRevision{}, err
	}
	promoted, err := p.Nomad.PromotedDatabaseRevision(nomadRegion, jobID)
	if err != nil {
		return nomad.DatabaseRevision{}, err
	}
	if promoted == 0 {
		return nomad.DatabaseRevision{}, &DatabaseTargetError{Reason: fmt.Sprintf("job %s has no promoted database delivery", jobID)}
	}
	material, err := p.Nomad.ReadDatabaseRevision(nomadRegion, jobID, promoted)
	if err != nil {
		return nomad.DatabaseRevision{}, err
	}
	if err := validateRunningDatabaseRevision(spec, jobID, material, expected); err != nil {
		return nomad.DatabaseRevision{}, err
	}
	return material, nil
}

func validateRunningDatabaseRevision(spec *model.InfraSpec, jobID string, material nomad.DatabaseRevision, expected map[string]database.TargetIdentity) error {
	for _, requirement := range spec.Databases {
		if requirement.Runtime == nil {
			continue
		}
		key := deliveryItemName(requirement.Name)
		var staged database.TargetIdentity
		want, recorded := expected[requirement.Name]
		complete := true
		if requirement.Runtime.Env != "" || requirement.Runtime.FileEnv != "" {
			complete = material.URLs[key] != ""
		}
		if requirement.Runtime.Components != nil {
			for _, field := range []string{"host", "user", "password", "name"} {
				if _, ok := material.Components[nomad.DatabaseComponentItemKey(requirement.Name, field)]; !ok {
					complete = false
				}
			}
		}
		if requirement.Runtime.TLS != nil {
			if material.TLS[nomad.DatabaseTLSItemKey(requirement.Name, "ca")] == "" ||
				material.TLS[nomad.DatabaseTLSItemKey(requirement.Name, "client_cert")] != "" ||
				material.TLS[nomad.DatabaseTLSItemKey(requirement.Name, "client_key")] != "" {
				complete = false
			}
		}
		if !complete || json.Unmarshal([]byte(material.Targets[key]), &staged) != nil || !recorded || staged != want {
			return &DatabaseTargetError{Reason: fmt.Sprintf("delivery revision %d of %s does not carry the running target and runtime material of database %q", material.Revision, jobID, requirement.Name)}
		}
	}
	return nil
}

// deliveryItemName is the item suffix nomad uses for a logical name.
func deliveryItemName(name string) string {
	key := nomad.DatabaseItemKey(name)
	return key[len("norn_db_url_"):]
}

// executeDatabaseBaseline records a verified baseline: every declared
// database's recorded target is re-resolved (generation-fenced) and probed
// for its declared identity, and the baseline still may not contradict
// recorded targets that appeared since acceptance.
func (p *Pipeline) executeDatabaseBaseline(ctx context.Context, op *model.Operation, claim store.OperationClaim, spec *model.InfraSpec) (*OperationResult, error) {
	if !spec.NamedDatabases() {
		return nil, &DatabaseTargetError{Reason: "a database baseline applies to named databases only"}
	}
	set, err := p.openDatabaseTargets(ctx, op.Payload, spec)
	if err != nil {
		return nil, err
	}
	defer set.Close()
	next := make([]recordedNamedTarget, 0, len(set.named))
	bindings := []string{}
	for name, bound := range set.named {
		next = append(next, recordedNamedTarget{Name: name, Target: bound.resolved.Target})
		bindings = append(bindings, fmt.Sprintf("%s=%s@%d", name, bound.resolved.Target.BindingID, bound.resolved.Target.BindingGeneration))
	}
	var targetErr *DatabaseTargetError
	if err := p.requireRunningTargetsUnchanged(ctx, spec.App, op.ID, next); err != nil && !(errors.As(err, &targetErr) && targetErr.Ambiguous) {
		return nil, err
	}
	sort.Strings(bindings)
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: "database baseline recorded for " + spec.App,
		Metadata: map[string]interface{}{"bindings": bindings, "probed": true}}, nil
}

// requireLiveClaim narrows the window in which an executor that lost its
// claim could perform an external write: it re-checks the claim against the
// database clock immediately before Nomad writes. It cannot make a Nomad
// write atomic with the claim (no transaction spans PostgreSQL and Nomad).
func (p *Pipeline) requireLiveClaim(ctx context.Context, st *state) error {
	if p.DB == nil || st.claim.OperationID() == "" {
		return nil
	}
	return p.DB.CheckOperationClaim(ctx, st.claim)
}
