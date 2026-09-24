package pipeline

import (
	"context"
	"errors"
	"time"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// DatabaseHealth reports whether an app's current database target is
// reachable as its declared identity. It is independent of Consul service
// health: the probe opens the target's private material and verifies
// current_database() and current_user. Engines without an adapter are
// reported as unsupported, never as healthy.
type DatabaseHealth struct {
	Database          string    `json:"database"`
	Status            string    `json:"status"` // ok | failed | unsupported
	DatabaseName      string    `json:"databaseName,omitempty"`
	ServiceID         string    `json:"serviceId,omitempty"`
	BindingID         string    `json:"bindingId,omitempty"`
	BindingGeneration uint64    `json:"bindingGeneration,omitempty"`
	Engine            string    `json:"engine,omitempty"`
	ServerVersion     string    `json:"serverVersion,omitempty"`
	Detail            string    `json:"detail,omitempty"`
	CheckedAt         time.Time `json:"checkedAt"`
}

// DatabaseHealth probes every database the app declares (the legacy mapping
// for a v1 app). It requires a database profile.
func (p *Pipeline) DatabaseHealth(ctx context.Context, spec *model.InfraSpec) ([]DatabaseHealth, error) {
	if p == nil || p.DatabaseTargets == nil {
		return nil, &DatabaseTargetError{Reason: "no database profile is configured"}
	}
	out := []DatabaseHealth{}
	if !spec.DeclaresDatabase() {
		return out, nil
	}
	names := []string{""}
	if spec.NamedDatabases() {
		names = names[:0]
		for _, requirement := range spec.Databases {
			names = append(names, requirement.Name)
		}
	}
	for _, name := range names {
		out = append(out, p.probeDatabase(ctx, spec, name))
	}
	return out, nil
}

func (p *Pipeline) probeDatabase(ctx context.Context, spec *model.InfraSpec, name string) DatabaseHealth {
	health := DatabaseHealth{Database: name, Status: "failed", CheckedAt: time.Now().UTC()}
	var resolved database.ResolvedBinding
	var err error
	if name == "" {
		resolved, _, err = p.DatabaseTargets.resolve(ctx, spec, nil, nil)
	} else {
		var resolver *database.Resolver
		if resolver, _, err = p.DatabaseTargets.resolverAt(ctx); err == nil {
			resolved, err = resolver.Resolve(database.ResolveRequest{DeploymentProfileID: p.DatabaseTargets.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: name})
		}
	}
	if err != nil {
		health.Detail = err.Error()
		return health
	}
	target := resolved.Target
	health.DatabaseName, health.ServiceID, health.BindingID, health.BindingGeneration, health.Engine = target.Database, target.ServiceID, target.BindingID, target.BindingGeneration, string(target.Engine)
	session, err := database.OpenSession(ctx, resolved, p.DatabaseTargets.Secrets)
	if err != nil {
		var resolverErr *database.ResolverError
		if errors.As(err, &resolverErr) && (resolverErr.Code == database.CodeUnsupportedEngine || resolverErr.Code == database.CodeUnsupportedCapability) {
			health.Status = "unsupported"
			health.Detail = "connection adapter or transport is not implemented for this target"
			return health
		}
		health.Detail = err.Error()
		return health
	}
	defer session.Close()
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := session.Probe(probeCtx)
	if err != nil {
		health.Detail = err.Error() // probe errors carry only a SQLSTATE
		return health
	}
	health.Status, health.ServerVersion = "ok", result.ServerVersion
	return health
}
