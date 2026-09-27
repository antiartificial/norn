package database

// CatalogInspection is the redacted operator view of a catalog revision.
// It shows identity, topology, generations and whether credential and TLS
// material is configured; it never shows a secret or a secret reference.
type CatalogInspection struct {
	Revision int64                `json:"revision"`
	Digest   string               `json:"digest"`
	Services []ServiceInspection  `json:"services"`
	Bindings []BindingSummary     `json:"bindings"`
	Profiles []ProfileInspection  `json:"profiles"`
	Retired  []RetiredResource    `json:"retired"`
	Engines  map[Engine]EngineUse `json:"engines"`
}

type ServiceInspection struct {
	ID            string            `json:"id"`
	Generation    uint64            `json:"generation"`
	Purpose       ServicePurpose    `json:"purpose"`
	Engine        Engine            `json:"engine"`
	EngineVersion string            `json:"engineVersion"`
	ProviderRef   string            `json:"providerRef"`
	Endpoint      DatabaseEndpoint  `json:"endpoint"`
	Topology      DatabaseTopology  `json:"topology"`
	TLS           DatabaseTLSPolicy `json:"tls"`
	Capabilities  []Capability      `json:"capabilities"`
}

type BindingSummary struct {
	ID                   string  `json:"id"`
	ServiceID            string  `json:"serviceId"`
	Database             string  `json:"database"`
	Role                 string  `json:"role"`
	Generation           uint64  `json:"generation"`
	TLSMode              TLSMode `json:"tlsMode"`
	CredentialConfigured bool    `json:"credentialConfigured"`
	CAConfigured         bool    `json:"caConfigured"`
	ClientCertConfigured bool    `json:"clientCertConfigured"`
}

type ProfileInspection struct {
	ID                string             `json:"id"`
	Topology          DeploymentTopology `json:"topology"`
	AvailabilityClass AvailabilityClass  `json:"availabilityClass"`
	DatabaseBindings  map[string]string  `json:"databaseBindings"`
	LegacyPostgres    *BindingSummary    `json:"legacyPostgres,omitempty"`
}

// EngineUse is an honest capability report: whether Norn has an adapter
// that can connect to, probe, dump and restore the engine in this build.
type EngineUse struct {
	Services         int  `json:"services"`
	AdapterAvailable bool `json:"adapterAvailable"`
}

// InspectCatalog builds the redacted view.
func InspectCatalog(revision int64, digest string, catalog Catalog) CatalogInspection {
	out := CatalogInspection{Revision: revision, Digest: digest, Services: []ServiceInspection{}, Bindings: []BindingSummary{}, Profiles: []ProfileInspection{},
		Retired: append([]RetiredResource{}, catalog.Retired...), Engines: map[Engine]EngineUse{}}
	for _, service := range catalog.Services {
		out.Services = append(out.Services, ServiceInspection{ID: service.ID, Generation: service.Generation, Purpose: service.Purpose, Engine: service.Engine,
			EngineVersion: service.EngineVersion, ProviderRef: service.ProviderRef, Endpoint: service.Endpoint, Topology: service.Topology, TLS: service.TLS,
			Capabilities: append([]Capability{}, service.Recovery.Capabilities...)})
		use := out.Engines[service.Engine]
		use.Services++
		use.AdapterAvailable = service.Engine == EnginePostgreSQL
		out.Engines[service.Engine] = use
	}
	for _, binding := range catalog.Bindings {
		out.Bindings = append(out.Bindings, BindingSummary{ID: binding.ID, ServiceID: binding.ServiceID, Database: binding.Database, Role: binding.Role,
			Generation: binding.Generation, TLSMode: binding.TLS.Mode, CredentialConfigured: binding.CredentialRef != "",
			CAConfigured: binding.TLS.CARef != "", ClientCertConfigured: binding.TLS.ClientCertRef != ""})
	}
	for _, profile := range catalog.Profiles {
		inspection := ProfileInspection{ID: profile.ID, Topology: profile.Topology, AvailabilityClass: profile.AvailabilityClass, DatabaseBindings: map[string]string{}}
		for logical, binding := range profile.DatabaseBindings {
			inspection.DatabaseBindings[logical] = binding
		}
		if legacy := profile.LegacyPostgres; legacy != nil {
			inspection.LegacyPostgres = &BindingSummary{ID: legacy.MappingID, ServiceID: legacy.ServiceID, Role: legacy.Role, Generation: legacy.Generation,
				TLSMode: legacy.TLS.Mode, CredentialConfigured: legacy.CredentialRef != "", CAConfigured: legacy.TLS.CARef != "", ClientCertConfigured: legacy.TLS.ClientCertRef != ""}
		}
		out.Profiles = append(out.Profiles, inspection)
	}
	return out
}
