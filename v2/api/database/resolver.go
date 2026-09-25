package database

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"norn/v2/api/model"
)

var (
	identifierPattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,126}[a-z0-9])?$`)
	referencePattern     = regexp.MustCompile(`^[a-z][a-z0-9+.-]{0,31}:[A-Za-z0-9._/-]{1,512}$`)
	serverNamePattern    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	engineVersionPattern = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,4}){0,2}$`)
	postgresRolePattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,63}$`)
	mysqlDatabasePattern = regexp.MustCompile(`^[A-Za-z0-9_$]{1,64}$`)
	mysqlUserPattern     = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
)

// engineCapabilities is the complete set of capabilities a catalog may declare
// per engine. PostgreSQL and MySQL are separate adapters; wire compatibility
// never implies another engine's recovery semantics, and CockroachDB is not an
// accepted engine. Declaring a capability does not implement its adapter.
var engineCapabilities = map[Engine]map[Capability]bool{
	EnginePostgreSQL: {
		CapabilityRuntime: true, CapabilityMigration: true, CapabilitySnapshot: true, CapabilityRestore: true,
		CapabilityHealth: true, CapabilityPITR: true, CapabilityManagedReadiness: true,
	},
	EngineMySQL: {
		CapabilityRuntime: true, CapabilityMigration: true, CapabilitySnapshot: true, CapabilityRestore: true,
		CapabilityHealth: true,
	},
}

// implementedCapabilities is narrower than catalog vocabulary. A catalog
// may describe future recovery policy, but accepted work may use only paths
// implemented by this binary.
var implementedCapabilities = map[Engine]map[Capability]bool{
	EnginePostgreSQL: {
		CapabilityRuntime: true, CapabilityMigration: true, CapabilitySnapshot: true, CapabilityRestore: true,
		CapabilityHealth: true, CapabilityPITR: true, CapabilityManagedReadiness: true,
	},
	EngineMySQL: {CapabilityRuntime: true, CapabilityHealth: true},
}

var knownCapabilities = map[Capability]bool{
	CapabilityRuntime: true, CapabilityMigration: true, CapabilitySnapshot: true, CapabilityRestore: true,
	CapabilityHealth: true, CapabilityPITR: true, CapabilityManagedReadiness: true,
}

var tlsRank = map[TLSMode]int{TLSDisabled: 0, TLSVerifyCA: 1, TLSVerifyFull: 2}

// Resolver is an immutable, validated view of one database catalog.
type Resolver struct {
	services map[string]DatabaseService
	bindings map[string]DatabaseBinding
	profiles map[string]DeploymentProfile
}

// NewResolver validates the complete catalog before any resolution is
// possible, so a missing reference or unsupported engine fails before side
// effects rather than on first use.
func NewResolver(catalog Catalog) (*Resolver, error) {
	if err := ValidateCatalog(catalog); err != nil {
		return nil, err
	}
	catalog = cloneCatalog(catalog)
	resolver := &Resolver{
		services: make(map[string]DatabaseService, len(catalog.Services)),
		bindings: make(map[string]DatabaseBinding, len(catalog.Bindings)),
		profiles: make(map[string]DeploymentProfile, len(catalog.Profiles)),
	}
	for _, service := range catalog.Services {
		resolver.services[service.ID] = service
	}
	for _, binding := range catalog.Bindings {
		resolver.bindings[binding.ID] = binding
	}
	for _, profile := range catalog.Profiles {
		resolver.profiles[profile.ID] = profile
	}
	return resolver, nil
}

// ValidateCatalog checks syntax, references, engine/purpose/capability
// support, TLS policy satisfaction and application/control separation.
func ValidateCatalog(catalog Catalog) error {
	if catalog.APIVersion != APIVersion {
		return invalid("apiVersion", "", "catalog apiVersion is unsupported")
	}
	services := make(map[string]DatabaseService, len(catalog.Services))
	for index, service := range catalog.Services {
		label := resourceLabel("services", index, service.ID)
		if err := validateService(service, label); err != nil {
			return err
		}
		if _, duplicate := services[service.ID]; duplicate {
			return invalid("id", label, "service ID is duplicated")
		}
		services[service.ID] = service
	}
	bindings := make(map[string]DatabaseBinding, len(catalog.Bindings))
	targets := map[string]string{}
	for index, binding := range catalog.Bindings {
		label := resourceLabel("bindings", index, binding.ID)
		if binding.APIVersion != APIVersion {
			return invalid("apiVersion", label, "binding apiVersion is unsupported")
		}
		if !identifierPattern.MatchString(binding.ID) {
			return invalid("id", label, "binding ID is invalid")
		}
		if _, duplicate := bindings[binding.ID]; duplicate {
			return invalid("id", label, "binding ID is duplicated")
		}
		service, found := services[binding.ServiceID]
		if !found {
			return notFound("serviceId", label, "binding references an unknown service")
		}
		if binding.Generation == 0 {
			return invalid("generation", label, "binding generation must be positive")
		}
		if !validDatabaseName(service.Engine, binding.Database) {
			return invalid("database", label, "database name is invalid for the service engine")
		}
		if !validRoleName(service.Engine, binding.Role) {
			return invalid("role", label, "role name is invalid for the service engine")
		}
		if !referencePattern.MatchString(binding.CredentialRef) {
			return invalid("credentialRef", label, "credential reference is missing or malformed")
		}
		if binding.ConsistencyGroup != "" && !identifierPattern.MatchString(binding.ConsistencyGroup) {
			return invalid("consistencyGroup", label, "consistency group is invalid")
		}
		if err := validateClientTLS(service.TLS, binding.TLS, label); err != nil {
			return err
		}
		target := binding.ServiceID + "\x00" + binding.Database + "\x00" + binding.Role
		if _, duplicate := targets[target]; duplicate {
			return invalid("database", label, "another binding already names this service database and role")
		}
		targets[target] = binding.ID
		bindings[binding.ID] = binding
	}
	profiles := make(map[string]bool, len(catalog.Profiles))
	legacy := map[string]LegacyPostgresDefault{}
	for index, profile := range catalog.Profiles {
		label := resourceLabel("profiles", index, profile.ID)
		if err := validateProfile(profile, label, services, bindings); err != nil {
			return err
		}
		if profiles[profile.ID] {
			return invalid("id", label, "profile ID is duplicated")
		}
		profiles[profile.ID] = true
		if profile.LegacyPostgres != nil {
			// A MappingID is one binding identity; profiles may share it only
			// with an identical definition.
			if existing, shared := legacy[profile.LegacyPostgres.MappingID]; shared && existing != *profile.LegacyPostgres {
				return invalid("legacyPostgres.mappingId", label, "legacy mapping ID is shared with a different definition")
			}
			legacy[profile.LegacyPostgres.MappingID] = *profile.LegacyPostgres
		}
	}
	if err := validateRetired(catalog, services, bindings); err != nil {
		return err
	}
	return validatePurposeSeparation(catalog, services)
}

func validateRetired(catalog Catalog, services map[string]DatabaseService, bindings map[string]DatabaseBinding) error {
	retired := map[RetiredResource]bool{}
	for index, tombstone := range catalog.Retired {
		label := resourceLabel("retired", index, tombstone.ID)
		if tombstone.Kind != RetiredService && tombstone.Kind != RetiredBinding {
			return invalid("retired.kind", label, "retired resource kind is unsupported")
		}
		if !identifierPattern.MatchString(tombstone.ID) {
			return invalid("retired.id", label, "retired resource ID is invalid")
		}
		if retired[tombstone] {
			return invalid("retired", label, "retired resource is duplicated")
		}
		retired[tombstone] = true
		if _, live := services[tombstone.ID]; live && tombstone.Kind == RetiredService {
			return invalid("retired", label, "a retired service ID cannot be reused")
		}
		if _, live := bindings[tombstone.ID]; live && tombstone.Kind == RetiredBinding {
			return invalid("retired", label, "a retired binding ID cannot be reused")
		}
	}
	for index, profile := range catalog.Profiles {
		if profile.LegacyPostgres != nil && retired[RetiredResource{Kind: RetiredBinding, ID: profile.LegacyPostgres.MappingID}] {
			return invalid("legacyPostgres.mappingId", resourceLabel("profiles", index, profile.ID), "a retired binding ID cannot be reused as a legacy mapping")
		}
	}
	return nil
}

func validateService(service DatabaseService, label string) error {
	if service.APIVersion != APIVersion {
		return invalid("apiVersion", label, "service apiVersion is unsupported")
	}
	if !identifierPattern.MatchString(service.ID) {
		return invalid("id", label, "service ID is invalid")
	}
	if service.Generation == 0 {
		return invalid("generation", label, "service generation must be positive")
	}
	supported, known := engineCapabilities[service.Engine]
	if !known {
		return &ResolverError{Code: CodeUnsupportedEngine, Field: "engine", Resource: label, Reason: "engine is not supported; only postgresql and mysql adapters are defined"}
	}
	switch service.Purpose {
	case PurposeApplication:
	case PurposeControl:
		if service.Engine != EnginePostgreSQL {
			return &ResolverError{Code: CodeUnsupportedEngine, Field: "engine", Resource: label, Reason: "control database services must use postgresql"}
		}
	default:
		return &ResolverError{Code: CodeUnsupportedPurpose, Field: "purpose", Resource: label, Reason: "service purpose is unsupported"}
	}
	if !engineVersionPattern.MatchString(service.EngineVersion) {
		return invalid("engineVersion", label, "engine version is missing or malformed")
	}
	if !referencePattern.MatchString(service.ProviderRef) {
		return invalid("providerRef", label, "provider reference is missing or malformed")
	}
	if !validEndpointHost(service.Endpoint.Host) || service.Endpoint.Port < 1 || service.Endpoint.Port > 65535 {
		return invalid("endpoint", label, "endpoint host or port is missing or malformed")
	}
	switch service.Topology.Mode {
	case TopologyLocalShared, TopologyManaged, TopologySelfManaged:
	default:
		return invalid("topology.mode", label, "topology mode is unsupported")
	}
	switch service.Topology.AvailabilityClass {
	case AvailabilitySingleHost, AvailabilitySingleZone, AvailabilityMultiZone, AvailabilityManaged:
	default:
		return invalid("topology.availabilityClass", label, "availability class is unsupported")
	}
	if service.Topology.Mode == TopologyLocalShared && service.Topology.AvailabilityClass != AvailabilitySingleHost {
		return invalid("topology.availabilityClass", label, "local-shared services must report single-host availability")
	}
	if _, known := tlsRank[service.TLS.MinimumMode]; !known {
		return invalid("tls.minimumMode", label, "TLS minimum mode is unsupported")
	}
	if service.TLS.RequireClientCertificate && service.TLS.MinimumMode == TLSDisabled {
		return invalid("tls.requireClientCertificate", label, "client certificates require a verified TLS minimum mode")
	}
	seen := map[Capability]bool{}
	for _, capability := range service.Recovery.Capabilities {
		if !knownCapabilities[capability] {
			return &ResolverError{Code: CodeUnsupportedCapability, Field: "recovery.capabilities", Resource: label, Reason: "capability is unknown"}
		}
		if !supported[capability] {
			return &ResolverError{Code: CodeUnsupportedCapability, Field: "recovery.capabilities", Resource: label, Reason: "capability is not supported by the service engine adapter"}
		}
		if capability == CapabilityManagedReadiness && service.Topology.Mode != TopologyManaged {
			return &ResolverError{Code: CodeUnsupportedCapability, Field: "recovery.capabilities", Resource: label, Reason: "managed readiness requires a managed topology"}
		}
		if seen[capability] {
			return invalid("recovery.capabilities", label, "capability is duplicated")
		}
		seen[capability] = true
	}
	return nil
}

// validateClientTLS checks that a binding's client configuration satisfies
// the service's policy. Verification modes need an explicit trust anchor.
func validateClientTLS(policy DatabaseTLSPolicy, tls DatabaseTLS, label string) error {
	rank, known := tlsRank[tls.Mode]
	if !known {
		return invalid("tls.mode", label, "TLS mode is unsupported")
	}
	if rank < tlsRank[policy.MinimumMode] {
		return invalid("tls.mode", label, "TLS mode is weaker than the service policy")
	}
	if tls.Mode == TLSDisabled {
		if tls.ServerName != "" || tls.CARef != "" || tls.ClientCertRef != "" || tls.ClientKeyRef != "" {
			return invalid("tls", label, "disabled TLS must not carry verification or client certificate settings")
		}
		return nil
	}
	if !referencePattern.MatchString(tls.CARef) {
		return invalid("tls.caRef", label, "verified TLS requires a well-formed CA reference")
	}
	if tls.Mode == TLSVerifyFull && !serverNamePattern.MatchString(tls.ServerName) {
		return invalid("tls.serverName", label, "verify-full TLS requires a server name")
	}
	if tls.Mode == TLSVerifyCA && tls.ServerName != "" {
		return invalid("tls.serverName", label, "verify-ca TLS does not verify a server name")
	}
	if (tls.ClientCertRef == "") != (tls.ClientKeyRef == "") {
		return invalid("tls.clientCertRef", label, "client certificate and key references must be configured together")
	}
	if tls.ClientCertRef != "" && (!referencePattern.MatchString(tls.ClientCertRef) || !referencePattern.MatchString(tls.ClientKeyRef)) {
		return invalid("tls.clientCertRef", label, "client certificate references are malformed")
	}
	if policy.RequireClientCertificate && tls.ClientCertRef == "" {
		return invalid("tls.clientCertRef", label, "service policy requires a client certificate")
	}
	return nil
}

func validateProfile(profile DeploymentProfile, label string, services map[string]DatabaseService, bindings map[string]DatabaseBinding) error {
	if profile.APIVersion != APIVersion {
		return invalid("apiVersion", label, "profile apiVersion is unsupported")
	}
	if !identifierPattern.MatchString(profile.ID) {
		return invalid("id", label, "profile ID is invalid")
	}
	switch profile.Topology {
	case DeploymentTopologyLocal:
		if profile.AvailabilityClass != AvailabilitySingleHost {
			return invalid("availabilityClass", label, "local deployment profiles must report single-host availability")
		}
	case DeploymentTopologyFleet:
		switch profile.AvailabilityClass {
		case AvailabilitySingleZone, AvailabilityMultiZone, AvailabilityManaged:
		default:
			return invalid("availabilityClass", label, "fleet deployment profiles require a declared multi-node availability class")
		}
	case "development", "production":
		return invalid("topology", label, "deployment topology must not reuse NORN_PROFILE security profile names")
	default:
		return invalid("topology", label, "deployment topology is unsupported")
	}
	for _, logical := range sortedKeys(profile.DatabaseBindings) {
		if !identifierPattern.MatchString(logical) {
			return invalid("databaseBindings", label, "logical resource ID is invalid")
		}
		if _, found := bindings[profile.DatabaseBindings[logical]]; !found {
			return notFound("databaseBindings", label+"/"+logical, "logical resource references an unknown binding")
		}
	}
	if legacy := profile.LegacyPostgres; legacy != nil {
		if !identifierPattern.MatchString(legacy.MappingID) {
			return invalid("legacyPostgres.mappingId", label, "legacy mapping ID is invalid")
		}
		if _, collides := bindings[legacy.MappingID]; collides {
			return invalid("legacyPostgres.mappingId", label, "legacy mapping ID collides with a named binding")
		}
		service, found := services[legacy.ServiceID]
		if !found {
			return notFound("legacyPostgres.serviceId", label, "legacy default references an unknown service")
		}
		if service.Engine != EnginePostgreSQL {
			return &ResolverError{Code: CodeUnsupportedEngine, Field: "legacyPostgres.serviceId", Resource: label, Reason: "legacy postgres declarations require a postgresql service"}
		}
		if service.Purpose != PurposeApplication {
			return &ResolverError{Code: CodePurposeMismatch, Field: "legacyPostgres.serviceId", Resource: label, Reason: "legacy application declarations must not resolve to a control service"}
		}
		if legacy.Generation == 0 {
			return invalid("legacyPostgres.generation", label, "legacy mapping generation must be positive")
		}
		if !validRoleName(EnginePostgreSQL, legacy.Role) {
			return invalid("legacyPostgres.role", label, "legacy role name is invalid")
		}
		if !referencePattern.MatchString(legacy.CredentialRef) {
			return invalid("legacyPostgres.credentialRef", label, "legacy credential reference is missing or malformed")
		}
		if err := validateClientTLS(service.TLS, legacy.TLS, label+"/legacyPostgres"); err != nil {
			return err
		}
	}
	return nil
}

// validatePurposeSeparation keeps application credentials and targets apart
// from control credentials and targets, including when both logical services
// share one physical provider locally.
func validatePurposeSeparation(catalog Catalog, services map[string]DatabaseService) error {
	type controlTarget struct{ providerRef, database, role string }
	controlCredentials := map[string]bool{}
	var controls []controlTarget
	for _, binding := range catalog.Bindings {
		service := services[binding.ServiceID]
		if service.Purpose == PurposeControl {
			controlCredentials[binding.CredentialRef] = true
			controls = append(controls, controlTarget{service.ProviderRef, binding.Database, binding.Role})
		}
	}
	for index, binding := range catalog.Bindings {
		service := services[binding.ServiceID]
		if service.Purpose != PurposeApplication {
			continue
		}
		label := resourceLabel("bindings", index, binding.ID)
		if controlCredentials[binding.CredentialRef] {
			return &ResolverError{Code: CodePurposeMismatch, Field: "credentialRef", Resource: label, Reason: "application binding reuses a control credential reference"}
		}
		for _, control := range controls {
			if control.providerRef == service.ProviderRef && (control.database == binding.Database || control.role == binding.Role) {
				return &ResolverError{Code: CodePurposeMismatch, Field: "database", Resource: label, Reason: "application binding shares a control database or role on the same provider"}
			}
		}
	}
	for index, profile := range catalog.Profiles {
		legacy := profile.LegacyPostgres
		if legacy == nil {
			continue
		}
		label := resourceLabel("profiles", index, profile.ID)
		if controlCredentials[legacy.CredentialRef] {
			return &ResolverError{Code: CodePurposeMismatch, Field: "legacyPostgres.credentialRef", Resource: label, Reason: "legacy default reuses a control credential reference"}
		}
		for _, control := range controls {
			if control.providerRef == services[legacy.ServiceID].ProviderRef && control.role == legacy.Role {
				return &ResolverError{Code: CodePurposeMismatch, Field: "legacyPostgres.role", Resource: label, Reason: "legacy default shares a control role on the same provider"}
			}
		}
	}
	return nil
}

// Resolve returns the single target for a named logical resource or an
// explicit legacy declaration. It never falls back from one to the other.
func (r *Resolver) Resolve(request ResolveRequest) (ResolvedBinding, error) {
	if r == nil {
		return ResolvedBinding{}, &ResolverError{Code: CodeInvalidRequest, Field: "resolver", Reason: "resolver is not initialized"}
	}
	if !identifierPattern.MatchString(request.DeploymentProfileID) {
		return ResolvedBinding{}, &ResolverError{Code: CodeInvalidRequest, Field: "deploymentProfileId", Reason: "deployment profile ID is missing or invalid"}
	}
	profile, found := r.profiles[request.DeploymentProfileID]
	if !found {
		return ResolvedBinding{}, notFound("deploymentProfileId", "profiles/"+request.DeploymentProfileID, "deployment profile is not defined")
	}
	profileLabel := "profiles/" + profile.ID
	if request.Purpose != PurposeApplication && request.Purpose != PurposeControl {
		return ResolvedBinding{}, &ResolverError{Code: CodeUnsupportedPurpose, Field: "purpose", Resource: profileLabel, Reason: "resolution purpose is missing or unsupported"}
	}
	named := request.LogicalResourceID != ""
	legacy := request.LegacyPostgres != nil
	if named && legacy {
		return ResolvedBinding{}, &ResolverError{Code: CodeAmbiguousLegacy, Field: "legacyPostgres", Resource: profileLabel, Reason: "request names both a logical resource and a legacy declaration"}
	}
	if !named && !legacy {
		return ResolvedBinding{}, &ResolverError{Code: CodeInvalidRequest, Field: "logicalResourceId", Resource: profileLabel, Reason: "request names neither a logical resource nor a legacy declaration"}
	}
	for _, capability := range request.RequiredCapabilities {
		if !knownCapabilities[capability] {
			return ResolvedBinding{}, &ResolverError{Code: CodeUnsupportedCapability, Field: "requiredCapabilities", Resource: profileLabel, Reason: "required capability is unknown"}
		}
		if capability == CapabilityRestore && request.Expected == nil {
			return ResolvedBinding{}, &ResolverError{Code: CodeInvalidRequest, Field: "expected", Resource: profileLabel, Reason: "restore requires an explicit expected target identity"}
		}
	}

	var resolved ResolvedBinding
	var err error
	if named {
		resolved, err = r.resolveNamed(profile, request.LogicalResourceID)
	} else {
		resolved, err = r.resolveLegacy(profile, request.LegacyPostgres.Database)
	}
	if err != nil {
		return ResolvedBinding{}, err
	}
	if resolved.Purpose != request.Purpose {
		return ResolvedBinding{}, &ResolverError{Code: CodePurposeMismatch, Field: "purpose", Resource: profileLabel, Reason: "resolved service purpose differs from the requested purpose"}
	}
	declared := map[Capability]bool{}
	for _, capability := range resolved.Capabilities {
		declared[capability] = true
	}
	for _, capability := range request.RequiredCapabilities {
		if !engineCapabilities[resolved.Target.Engine][capability] {
			return ResolvedBinding{}, &ResolverError{Code: CodeUnsupportedCapability, Field: "requiredCapabilities", Resource: profileLabel, Reason: "required capability is not supported by the target engine adapter"}
		}
		if !implementedCapabilities[resolved.Target.Engine][capability] {
			return ResolvedBinding{}, &ResolverError{Code: CodeUnsupportedCapability, Field: "requiredCapabilities", Resource: profileLabel, Reason: "required capability has no implemented target engine path in this build"}
		}
		if resolved.Target.Engine == EngineMySQL && capability == CapabilityRuntime && resolved.TLS.Mode != TLSDisabled {
			// The resolver proves the transport shape. The consuming app must
			// separately prove that its client actually verifies the certificate.
			// The only qualified runtime shape has a CA, the endpoint's exact
			// server name, and no client certificate.
			if resolved.TLS.Mode != TLSVerifyFull || resolved.TLS.ServerName != resolved.Endpoint.Host || resolved.TLS.ClientCertRef != "" || resolved.TLS.ClientKeyRef != "" {
				return ResolvedBinding{}, &ResolverError{Code: CodeUnsupportedCapability, Field: "tls", Resource: profileLabel, Reason: "MySQL runtime requires endpoint-bound verify-full TLS without a client certificate"}
			}
		}
		if !declared[capability] {
			return ResolvedBinding{}, &ResolverError{Code: CodeUnsupportedCapability, Field: "requiredCapabilities", Resource: profileLabel, Reason: "required capability is not declared by the target service"}
		}
	}
	if request.Expected != nil && *request.Expected != resolved.Target {
		return ResolvedBinding{}, &ResolverError{Code: CodeStaleTarget, Field: "expected", Resource: profileLabel, Reason: "resolved target identity or generation differs from the expected target"}
	}
	return resolved, nil
}

func (r *Resolver) resolveNamed(profile DeploymentProfile, logical string) (ResolvedBinding, error) {
	profileLabel := "profiles/" + profile.ID
	if !identifierPattern.MatchString(logical) {
		return ResolvedBinding{}, &ResolverError{Code: CodeInvalidRequest, Field: "logicalResourceId", Resource: profileLabel, Reason: "logical resource ID is invalid"}
	}
	bindingID, found := profile.DatabaseBindings[logical]
	if !found {
		return ResolvedBinding{}, notFound("logicalResourceId", profileLabel+"/"+logical, "logical resource has no binding in this profile")
	}
	binding := r.bindings[bindingID]
	service := r.services[binding.ServiceID]
	return r.resolved(profile, service, logical, binding.ID, binding.Generation, binding.Database, binding.Role, binding.CredentialRef, binding.TLS, false), nil
}

func (r *Resolver) resolveLegacy(profile DeploymentProfile, databaseName string) (ResolvedBinding, error) {
	profileLabel := "profiles/" + profile.ID
	if !model.IsSafePostgresDatabaseName(databaseName) {
		return ResolvedBinding{}, &ResolverError{Code: CodeInvalidRequest, Field: "legacyPostgres.database", Resource: profileLabel, Reason: "legacy database name is invalid"}
	}
	legacy := profile.LegacyPostgres
	if legacy == nil {
		return ResolvedBinding{}, notFound("legacyPostgres", profileLabel, "profile has no explicit legacy postgres default")
	}
	service := r.services[legacy.ServiceID]
	for _, logical := range sortedKeys(profile.DatabaseBindings) {
		binding := r.bindings[profile.DatabaseBindings[logical]]
		if binding.ServiceID == legacy.ServiceID && binding.Database == databaseName {
			return ResolvedBinding{}, &ResolverError{Code: CodeAmbiguousLegacy, Field: "legacyPostgres.database", Resource: profileLabel, Reason: "legacy database is also named by a logical binding; declare the named binding instead"}
		}
	}
	for _, binding := range r.bindings {
		control := r.services[binding.ServiceID]
		if control.Purpose == PurposeControl && control.ProviderRef == service.ProviderRef && binding.Database == databaseName {
			return ResolvedBinding{}, &ResolverError{Code: CodePurposeMismatch, Field: "legacyPostgres.database", Resource: profileLabel, Reason: "legacy application declaration names a control database"}
		}
	}
	return r.resolved(profile, service, "", legacy.MappingID, legacy.Generation, databaseName, legacy.Role, legacy.CredentialRef, legacy.TLS, true), nil
}

func (r *Resolver) resolved(profile DeploymentProfile, service DatabaseService, logical, bindingID string, bindingGeneration uint64, databaseName, role, credentialRef string, tls DatabaseTLS, legacy bool) ResolvedBinding {
	return ResolvedBinding{
		Target: TargetIdentity{
			ServiceID: service.ID, ServiceGeneration: service.Generation, BindingID: bindingID, BindingGeneration: bindingGeneration,
			Engine: service.Engine, Database: databaseName, Role: role,
		},
		Purpose: service.Purpose, ProfileID: profile.ID, LogicalResourceID: logical, ProviderRef: service.ProviderRef, Endpoint: service.Endpoint,
		Topology: service.Topology, Capabilities: append([]Capability(nil), service.Recovery.Capabilities...),
		CredentialRef: credentialRef, TLSPolicy: service.TLS, TLS: tls, Legacy: legacy,
	}
}

// ValidateTransition checks that next may replace previous without silently
// moving a consumer to a different target under an unchanged generation.
//
// Services: purpose is immutable; any other change (provider, engine, version,
// topology, TLS policy, recovery capabilities) requires a generation bump.
// Bindings and legacy defaults: service/database/role/TLS mode/server-name
// changes require a generation bump; credential and certificate reference
// rotation may retain it. Generations never decrease. A logical resource may
// not be re-pointed at another binding, and a service or binding referenced in
// previous may not be removed in the same transition. Every removed service,
// binding or legacy mapping must be recorded as a permanent retired tombstone,
// so an ID (and therefore its generation sequence) is never reused.
func ValidateTransition(previous, next Catalog) error {
	if err := ValidateCatalog(previous); err != nil {
		return err
	}
	if err := ValidateCatalog(next); err != nil {
		return err
	}
	nextServices := map[string]DatabaseService{}
	for _, service := range next.Services {
		nextServices[service.ID] = service
	}
	nextBindings := map[string]DatabaseBinding{}
	for _, binding := range next.Bindings {
		nextBindings[binding.ID] = binding
	}
	nextProfiles := map[string]DeploymentProfile{}
	for _, profile := range next.Profiles {
		nextProfiles[profile.ID] = profile
	}
	referencedServices := map[string]bool{}
	referencedBindings := map[string]bool{}
	for _, profile := range previous.Profiles {
		for _, bindingID := range profile.DatabaseBindings {
			referencedBindings[bindingID] = true
		}
		if profile.LegacyPostgres != nil {
			referencedServices[profile.LegacyPostgres.ServiceID] = true
		}
	}
	for _, binding := range previous.Bindings {
		referencedServices[binding.ServiceID] = true
	}
	nextRetired := map[RetiredResource]bool{}
	for _, tombstone := range next.Retired {
		nextRetired[tombstone] = true
	}
	for _, tombstone := range previous.Retired {
		if !nextRetired[tombstone] {
			return unsafe("retired", "retired/"+tombstone.ID, "retirement tombstones are permanent")
		}
	}
	// Legacy mappings are binding identities: compare them by MappingID across
	// whole catalogs so moving one between profiles cannot skip these checks.
	previousLegacy, nextLegacy := legacyMappings(previous), legacyMappings(next)
	previousMappingIDs := make([]string, 0, len(previousLegacy))
	for mappingID := range previousLegacy {
		previousMappingIDs = append(previousMappingIDs, mappingID)
	}
	sort.Strings(previousMappingIDs)
	for _, mappingID := range previousMappingIDs {
		old := previousLegacy[mappingID]
		label := "legacyMappings/" + mappingID
		updated, kept := nextLegacy[mappingID]
		if !kept {
			if !nextRetired[RetiredResource{Kind: RetiredBinding, ID: mappingID}] {
				return unsafe("legacyPostgres", label, "a removed legacy mapping must be recorded as a retired binding")
			}
			continue
		}
		if err := checkTargetGeneration(label, old.Generation, updated.Generation,
			old.ServiceID == updated.ServiceID && old.Role == updated.Role && sameTLSTarget(old.TLS, updated.TLS)); err != nil {
			return err
		}
	}

	for _, before := range previous.Services {
		label := "services/" + before.ID
		after, kept := nextServices[before.ID]
		if !kept {
			if referencedServices[before.ID] {
				return unsafe("services", label, "a service referenced by bindings or legacy defaults cannot be removed in the same transition")
			}
			if !nextRetired[RetiredResource{Kind: RetiredService, ID: before.ID}] {
				return unsafe("services", label, "a removed service must be recorded as retired")
			}
			continue
		}
		if after.Purpose != before.Purpose {
			return unsafe("purpose", label, "service purpose is immutable")
		}
		if after.Generation < before.Generation {
			return unsafe("generation", label, "service generation decreased")
		}
		if after.Generation == before.Generation && !sameServiceDefinition(before, after) {
			return unsafe("generation", label, "provider, engine, version, topology, TLS policy or recovery changes require a service generation bump")
		}
	}
	for _, before := range previous.Bindings {
		label := "bindings/" + before.ID
		after, kept := nextBindings[before.ID]
		if !kept {
			if referencedBindings[before.ID] {
				return unsafe("bindings", label, "a binding referenced by a profile cannot be removed in the same transition")
			}
			if !nextRetired[RetiredResource{Kind: RetiredBinding, ID: before.ID}] {
				return unsafe("bindings", label, "a removed binding must be recorded as retired")
			}
			continue
		}
		if err := checkTargetGeneration(label, before.Generation, after.Generation,
			before.ServiceID == after.ServiceID && before.Database == after.Database && before.Role == after.Role &&
				before.ConsistencyGroup == after.ConsistencyGroup && sameTLSTarget(before.TLS, after.TLS)); err != nil {
			return err
		}
	}
	for _, before := range previous.Profiles {
		label := "profiles/" + before.ID
		after, kept := nextProfiles[before.ID]
		if !kept {
			continue
		}
		for _, logical := range sortedKeys(before.DatabaseBindings) {
			if bindingID, present := after.DatabaseBindings[logical]; present && bindingID != before.DatabaseBindings[logical] {
				return unsafe("databaseBindings", label+"/"+logical, "a logical resource cannot be re-pointed to another binding; change the binding with a generation bump")
			}
		}
	}
	return nil
}

// legacyMappings indexes legacy defaults by MappingID. ValidateCatalog
// guarantees profiles sharing a MappingID carry identical definitions.
func legacyMappings(catalog Catalog) map[string]LegacyPostgresDefault {
	out := map[string]LegacyPostgresDefault{}
	for _, profile := range catalog.Profiles {
		if profile.LegacyPostgres != nil {
			out[profile.LegacyPostgres.MappingID] = *profile.LegacyPostgres
		}
	}
	return out
}

func checkTargetGeneration(label string, before, after uint64, sameTarget bool) error {
	if after < before {
		return unsafe("generation", label, "binding generation decreased")
	}
	if after == before && !sameTarget {
		return unsafe("generation", label, "service, database, role or TLS verification changes require a binding generation bump")
	}
	return nil
}

var socketDirectoryPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,255}$`)

// validEndpointHost accepts a DNS name or IP literal, or an absolute
// Unix-socket directory without parent references.
func validEndpointHost(host string) bool {
	if strings.HasPrefix(host, "/") {
		return socketDirectoryPattern.MatchString(host) && !strings.Contains(host, "..")
	}
	// IP literals must be canonical and unzoned so identity comparison and
	// URL rendering are unambiguous.
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Zone() == "" && address.String() == host
	}
	// A numeric dotted name that is not a canonical IPv4 literal (such as
	// 010.0.0.7) may be read as octal by inet_aton; it is never a DNS name.
	if strings.Trim(host, "0123456789.") == "" {
		return false
	}
	return serverNamePattern.MatchString(host)
}

func sameServiceDefinition(a, b DatabaseService) bool {
	if a.Engine != b.Engine || a.EngineVersion != b.EngineVersion || a.ProviderRef != b.ProviderRef || a.Endpoint != b.Endpoint || a.Topology != b.Topology || a.TLS != b.TLS ||
		len(a.Recovery.Capabilities) != len(b.Recovery.Capabilities) {
		return false
	}
	left := append([]Capability(nil), a.Recovery.Capabilities...)
	right := append([]Capability(nil), b.Recovery.Capabilities...)
	sort.Slice(left, func(i, j int) bool { return left[i] < left[j] })
	sort.Slice(right, func(i, j int) bool { return right[i] < right[j] })
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// sameTLSTarget ignores rotatable secret references; mode and server name
// define which endpoint is verified and therefore belong to target identity.
func sameTLSTarget(a, b DatabaseTLS) bool {
	return a.Mode == b.Mode && a.ServerName == b.ServerName
}

func validDatabaseName(engine Engine, name string) bool {
	switch engine {
	case EnginePostgreSQL:
		return model.IsSafePostgresDatabaseName(name)
	case EngineMySQL:
		return mysqlDatabasePattern.MatchString(name)
	}
	return false
}

func validRoleName(engine Engine, name string) bool {
	switch engine {
	case EnginePostgreSQL:
		return name != "." && name != ".." && postgresRolePattern.MatchString(name)
	case EngineMySQL:
		return name != "." && name != ".." && mysqlUserPattern.MatchString(name)
	}
	return false
}

// resourceLabel uses a validated ID when possible and otherwise a positional
// label, so rejected raw values never reach an error message.
func resourceLabel(kind string, index int, id string) string {
	if identifierPattern.MatchString(id) {
		return kind + "/" + id
	}
	return fmt.Sprintf("%s[%d]", kind, index)
}

func invalid(field, resource, reason string) error {
	return &ResolverError{Code: CodeInvalidCatalog, Field: field, Resource: resource, Reason: reason}
}

func notFound(field, resource, reason string) error {
	return &ResolverError{Code: CodeResolutionNotFound, Field: field, Resource: resource, Reason: reason}
}

func unsafe(field, resource, reason string) error {
	return &ResolverError{Code: CodeUnsafeTransition, Field: field, Resource: resource, Reason: reason}
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneCatalog(catalog Catalog) Catalog {
	out := Catalog{APIVersion: catalog.APIVersion, Retired: append([]RetiredResource(nil), catalog.Retired...)}
	for _, service := range catalog.Services {
		service.Recovery.Capabilities = append([]Capability(nil), service.Recovery.Capabilities...)
		out.Services = append(out.Services, service)
	}
	out.Bindings = append(out.Bindings, catalog.Bindings...)
	for _, profile := range catalog.Profiles {
		if profile.DatabaseBindings != nil {
			mapping := make(map[string]string, len(profile.DatabaseBindings))
			for key, value := range profile.DatabaseBindings {
				mapping[key] = value
			}
			profile.DatabaseBindings = mapping
		}
		if profile.LegacyPostgres != nil {
			legacy := *profile.LegacyPostgres
			profile.LegacyPostgres = &legacy
		}
		out.Profiles = append(out.Profiles, profile)
	}
	return out
}

func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }
