package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/saga"
)

func databaseEnvConflictError(conflicts []string) error {
	return &DatabaseTargetError{Reason: fmt.Sprintf("%s delivered by the database binding must not also come from app secrets or provisioned services", strings.Join(conflicts, ", "))}
}

// checkSecretConflicts refuses an app whose secrets would shadow a
// Norn-delivered database variable. It runs at deploy acceptance and again
// before the migration side effect, since secrets can change in between.
func (p *Pipeline) checkSecretConflicts(spec *model.InfraSpec) error {
	if p.Secrets == nil || !spec.NamedDatabases() {
		return nil
	}
	keys, err := p.Secrets.List(spec.App)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list app secrets: %w", err)
	}
	present := make(map[string]string, len(keys))
	for _, key := range keys {
		present[key] = ""
	}
	if conflicts := spec.DatabaseEnvConflicts(present); len(conflicts) > 0 {
		return databaseEnvConflictError(conflicts)
	}
	return nil
}

// deliverDatabases stages the recorded targets' runtime connection values
// in the private Nomad Variables every job of the app reads (see
// nomad/database_delivery.go), fenced by the catalog revision the targets
// were re-resolved against. It runs in the submit step, after safety
// snapshots and migrations used the same targets and before any job is
// submitted; the submitted job version reads the staged revision, so
// running allocations are not cut over until promoteDatabases.
func (p *Pipeline) deliverDatabases(ctx context.Context, st *state, sg *saga.Saga) error {
	if !st.spec.NamedDatabases() || !nomad.HasRuntimeDatabases(st.spec) {
		return nil
	}
	set, err := p.databaseForState(ctx, st)
	if err != nil {
		return err
	}
	if p.Nomad == nil {
		return fmt.Errorf("nomad not connected")
	}
	if err := p.guardDeployTargets(ctx, st); err != nil {
		return err
	}
	// nomad/jobs/<jobID> is private to a job only if no other app's job
	// shares the ID (app "a-b" versus process "b" of app "a").
	if err := p.requireDistinctJobIDs(st.spec); err != nil {
		return err
	}
	items := map[string]string{}
	targets := map[string]string{}
	delivered := []string{}
	for _, requirement := range st.spec.Databases {
		if requirement.Runtime == nil {
			continue
		}
		bound := set.named[requirement.Name]
		if bound == nil {
			return &DatabaseTargetError{Reason: fmt.Sprintf("database %q was not bound at acceptance", requirement.Name)}
		}
		if err := bound.requireCapabilities(database.CapabilityRuntime); err != nil {
			return err
		}
		connection, err := databaseConnectionItems(requirement, bound)
		if err != nil {
			return err
		}
		for key, value := range connection {
			items[key] = value
		}
		identity, err := json.Marshal(bound.resolved.Target)
		if err != nil {
			return err
		}
		targets[requirement.Name] = string(identity)
		delivered = append(delivered, fmt.Sprintf("%s=%s@%d", requirement.Name, bound.resolved.Target.BindingID, bound.resolved.Target.BindingGeneration))
	}
	for name, identity := range targets {
		items[nomad.DatabaseTargetItemKey(name)] = identity
	}
	if err := p.requireLiveClaim(ctx, st); err != nil {
		return err
	}
	for _, region := range st.spec.ResolvedRegions() {
		for _, jobID := range nomad.DatabaseDeliveryJobIDs(st.spec) {
			if !deliveryJobRunsInRegion(st.spec, jobID, region.Name) {
				continue
			}
			if err := p.Nomad.DeliverDatabaseVariable(region.NomadRegion, jobID, items, set.revision); err != nil {
				return err
			}
		}
		_ = sg.Log(ctx, "database.staged", "database connections staged in the private runtime variables", map[string]string{
			"step": "submit", "region": region.Name, "databases": strings.Join(delivered, ","), "catalogRevision": fmt.Sprintf("%d", set.revision),
		})
	}
	st.deliveryRevision = set.revision
	return nil
}

// databaseConnectionItems transfers only the declared runtime shape from a
// probed, bound session. The CA bytes become a private, revisioned Nomad
// Variable item; the job spec contains only its file path template.
func databaseConnectionItems(requirement model.DatabaseRequirement, bound *boundDatabase) (map[string]string, error) {
	items := map[string]string{}
	if requirement.Runtime == nil || bound == nil || bound.session == nil {
		return nil, &DatabaseTargetError{Reason: fmt.Sprintf("database %q has no opened runtime target", requirement.Name)}
	}
	if requirement.Runtime.Components != nil {
		values, err := bound.session.RuntimeComponents()
		if err != nil {
			return nil, err
		}
		for field, value := range values {
			items[nomad.DatabaseComponentItemKey(requirement.Name, field)] = value
		}
	}
	if requirement.Runtime.TLS != nil {
		if bound.resolved.Target.Engine != database.EngineMySQL || bound.resolved.TLS.Mode != database.TLSVerifyFull || bound.resolved.TLS.ClientCertRef != "" || bound.resolved.TLS.ClientKeyRef != "" || bound.resolved.TLS.ServerName != bound.resolved.Endpoint.Host || requirement.Runtime.TLS.CAFileEnv == "" || requirement.Runtime.TLS.ClientCertFileEnv != "" || requirement.Runtime.TLS.ClientKeyFileEnv != "" {
			return nil, &DatabaseTargetError{Reason: fmt.Sprintf("database %q has an unqualified runtime TLS declaration", requirement.Name)}
		}
		material, err := bound.session.RuntimeTLSMaterial()
		if err != nil {
			return nil, err
		}
		ca := material["ca"]
		if len(ca) == 0 || len(material) != 1 {
			for _, value := range material {
				clear(value)
			}
			return nil, &DatabaseTargetError{Reason: fmt.Sprintf("database %q has incomplete or unqualified runtime TLS material", requirement.Name)}
		}
		items[nomad.DatabaseTLSItemKey(requirement.Name, "ca")] = string(ca)
		clear(ca)
	} else if bound.resolved.Target.Engine == database.EngineMySQL && bound.resolved.TLS.Mode != database.TLSDisabled {
		return nil, &DatabaseTargetError{Reason: fmt.Sprintf("database %q requires a runtime TLS CA file declaration", requirement.Name)}
	}
	if requirement.Runtime.Env != "" || requirement.Runtime.FileEnv != "" {
		value, err := bound.session.RuntimeConnectionURL()
		if err != nil {
			return nil, err
		}
		items[nomad.DatabaseItemKey(requirement.Name)] = value
	}
	return items, nil
}

// promoteDatabases makes the staged delivery current once the rollout is
// ready, so function invocations, cron resubmissions and rollbacks read the
// accepted target. A newer promotion by a later deploy makes this one stale.
func (p *Pipeline) promoteDatabases(ctx context.Context, st *state, sg *saga.Saga) error {
	if st.deliveryRevision == 0 {
		return nil
	}
	if err := p.requireLiveClaim(ctx, st); err != nil {
		return err
	}
	for _, region := range st.spec.ResolvedRegions() {
		for _, jobID := range nomad.DatabaseDeliveryJobIDs(st.spec) {
			if !deliveryJobRunsInRegion(st.spec, jobID, region.Name) {
				continue
			}
			if err := p.Nomad.PromoteDatabaseVariable(region.NomadRegion, jobID, st.deliveryRevision); err != nil {
				return err
			}
		}
		_ = sg.Log(ctx, "database.promoted", "database delivery promoted after rollout readiness", map[string]string{
			"step": "healthy", "region": region.Name, "catalogRevision": fmt.Sprintf("%d", st.deliveryRevision),
		})
	}
	return nil
}

// requireDistinctJobIDs refuses delivery when any Nomad job ID of this app
// (service job, periodic jobs) equals a job ID of another discovered app.
// It cannot see apps added later or jobs registered outside Norn; a later
// app whose ID collides is refused when it delivers, but a v1 app without
// delivery would still overwrite the colliding job (pre-existing v2 hazard).
func (p *Pipeline) requireDistinctJobIDs(spec *model.InfraSpec) error {
	specs, err := model.DiscoverAllApps(p.AppsDir)
	if err != nil {
		return fmt.Errorf("discover apps: %w", err)
	}
	owner := map[string]string{}
	for _, other := range specs {
		if other.App == spec.App {
			continue
		}
		for _, id := range nomad.JobIDs(other) {
			owner[id] = other.App
		}
	}
	for _, id := range nomad.JobIDs(spec) {
		if app, taken := owner[id]; taken {
			return &DatabaseTargetError{Reason: fmt.Sprintf("Nomad job ID %s of app %s collides with app %s; its private delivery variable would be shared", id, spec.App, app)}
		}
	}
	return nil
}

// deliveryJobRunsInRegion keeps the service variable in every region (it is
// also the source for function invocations) and periodic variables only
// where the scheduled process runs.
func deliveryJobRunsInRegion(spec *model.InfraSpec, jobID, region string) bool {
	if jobID == spec.App {
		return true
	}
	process, ok := spec.Processes[strings.TrimPrefix(jobID, spec.App+"-")]
	return ok && spec.ProcessRunsInRegion(process, region)
}
