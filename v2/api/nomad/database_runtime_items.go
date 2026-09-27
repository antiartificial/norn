package nomad

import (
	"fmt"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// RuntimeDatabaseItems converts one opened, probed binding into the
// current-keyed private Variable items a managed or legacy Nomad job stages.
// The caller must resolve with Expected target identity and Probe the session
// before calling. No connection or PEM value belongs in the job spec.
func RuntimeDatabaseItems(requirement model.DatabaseRequirement, resolved database.ResolvedBinding, session *database.Session) (map[string]string, error) {
	items := map[string]string{}
	if requirement.Runtime == nil || session == nil {
		return nil, fmt.Errorf("database %q has no opened runtime target", requirement.Name)
	}
	if requirement.Runtime.Components != nil {
		values, err := session.RuntimeComponents()
		if err != nil {
			return nil, err
		}
		for field, value := range values {
			items[DatabaseComponentItemKey(requirement.Name, field)] = value
		}
	}
	if requirement.Runtime.TLS != nil {
		if resolved.Target.Engine != database.EngineMySQL || resolved.TLS.Mode != database.TLSVerifyFull || resolved.TLS.ClientCertRef != "" || resolved.TLS.ClientKeyRef != "" || resolved.TLS.ServerName != resolved.Endpoint.Host || requirement.Runtime.TLS.CAFileEnv == "" || requirement.Runtime.TLS.ClientCertFileEnv != "" || requirement.Runtime.TLS.ClientKeyFileEnv != "" {
			return nil, fmt.Errorf("database %q has an unqualified runtime TLS declaration", requirement.Name)
		}
		material, err := session.RuntimeTLSMaterial()
		if err != nil {
			return nil, err
		}
		ca := material["ca"]
		if len(ca) == 0 || len(material) != 1 {
			for _, value := range material {
				clear(value)
			}
			return nil, fmt.Errorf("database %q has incomplete or unqualified runtime TLS material", requirement.Name)
		}
		items[DatabaseTLSItemKey(requirement.Name, "ca")] = string(ca)
		clear(ca)
	} else if resolved.Target.Engine == database.EngineMySQL && resolved.TLS.Mode != database.TLSDisabled {
		return nil, fmt.Errorf("database %q requires a runtime TLS CA file declaration", requirement.Name)
	}
	if requirement.Runtime.Env != "" || requirement.Runtime.FileEnv != "" {
		value, err := session.RuntimeConnectionURL()
		if err != nil {
			return nil, err
		}
		items[DatabaseItemKey(requirement.Name)] = value
	}
	return items, nil
}
