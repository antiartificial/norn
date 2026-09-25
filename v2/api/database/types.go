package database

import "fmt"

const APIVersion = "norn.database/v1alpha1"

type Engine string

const (
	EnginePostgreSQL Engine = "postgresql"
	EngineMySQL      Engine = "mysql"
)

type ServicePurpose string

const (
	PurposeApplication ServicePurpose = "application"
	PurposeControl     ServicePurpose = "control"
)

type TopologyMode string

const (
	TopologyLocalShared TopologyMode = "local-shared"
	TopologyManaged     TopologyMode = "managed"
	TopologySelfManaged TopologyMode = "self-managed"
)

type AvailabilityClass string

const (
	AvailabilitySingleHost AvailabilityClass = "single-host"
	AvailabilitySingleZone AvailabilityClass = "single-zone"
	AvailabilityMultiZone  AvailabilityClass = "multi-zone"
	AvailabilityManaged    AvailabilityClass = "provider-managed"
)

// DeploymentTopology names the local/Fleet deployment shape. It is
// deliberately independent of NORN_PROFILE (development/production security
// hardening): selecting a local topology never weakens admission policy.
type DeploymentTopology string

const (
	DeploymentTopologyLocal DeploymentTopology = "local"
	DeploymentTopologyFleet DeploymentTopology = "fleet"
)

type TLSMode string

const (
	TLSDisabled   TLSMode = "disabled"
	TLSVerifyCA   TLSMode = "verify-ca"
	TLSVerifyFull TLSMode = "verify-full"
)

type Capability string

const (
	CapabilityRuntime          Capability = "runtime"
	CapabilityMigration        Capability = "migration"
	CapabilitySnapshot         Capability = "snapshot"
	CapabilityRestore          Capability = "restore"
	CapabilityHealth           Capability = "health"
	CapabilityPITR             Capability = "pitr"
	CapabilityManagedReadiness Capability = "managed-readiness"
)

type DatabaseTopology struct {
	Mode              TopologyMode      `json:"mode"`
	AvailabilityClass AvailabilityClass `json:"availabilityClass"`
}

// DatabaseTLSPolicy is the service-side requirement every binding must meet.
type DatabaseTLSPolicy struct {
	MinimumMode              TLSMode `json:"minimumMode"`
	RequireClientCertificate bool    `json:"requireClientCertificate,omitempty"`
}

// DatabaseTLS is a binding's client connection configuration. The *Ref fields
// name secret material; they are never projected into inspection output.
type DatabaseTLS struct {
	Mode          TLSMode `json:"mode"`
	ServerName    string  `json:"serverName,omitempty"`
	CARef         string  `json:"caRef,omitempty"`
	ClientCertRef string  `json:"clientCertRef,omitempty"`
	ClientKeyRef  string  `json:"clientKeyRef,omitempty"`
}

type RecoveryPolicy struct {
	Capabilities []Capability `json:"capabilities"`
}

// DatabaseEndpoint is where the service accepts connections: a DNS name or
// IP, or an absolute Unix-socket directory. It is routing identity, not a
// credential: changing it requires a service generation bump, so accepted
// work can never be repointed by rotating secret material.
type DatabaseEndpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type DatabaseService struct {
	APIVersion    string            `json:"apiVersion"`
	ID            string            `json:"id"`
	Generation    uint64            `json:"generation"`
	Purpose       ServicePurpose    `json:"purpose"`
	Engine        Engine            `json:"engine"`
	EngineVersion string            `json:"engineVersion"`
	ProviderRef   string            `json:"providerRef"`
	Endpoint      DatabaseEndpoint  `json:"endpoint"`
	Topology      DatabaseTopology  `json:"topology"`
	TLS           DatabaseTLSPolicy `json:"tls"`
	Recovery      RecoveryPolicy    `json:"recovery"`
}

type DatabaseBinding struct {
	APIVersion       string      `json:"apiVersion"`
	ID               string      `json:"id"`
	ServiceID        string      `json:"serviceId"`
	Database         string      `json:"database"`
	Role             string      `json:"role"`
	Generation       uint64      `json:"generation"`
	CredentialRef    string      `json:"credentialRef"`
	TLS              DatabaseTLS `json:"tls"`
	ConsistencyGroup string      `json:"consistencyGroup,omitempty"`
	// MySQLMaintenance is private recovery authority for a MySQL application
	// binding. It is optional so ordinary application bindings do not acquire
	// maintenance credentials or capabilities.
	MySQLMaintenance *MySQLMaintenanceCredentials `json:"mysqlMaintenance,omitempty"`
}

// MySQLMaintenanceCredentials names the separately provisioned identities a
// private MySQL restore may use. References are catalog identities, never
// credential values. Generation makes a maintenance-identity change as
// explicit as a runtime target change.
type MySQLMaintenanceCredentials struct {
	Generation           uint64 `json:"generation"`
	RuntimeAccountHost   string `json:"runtimeAccountHost"`
	RestoreRole          string `json:"restoreRole"`
	RestoreAccountHost   string `json:"restoreAccountHost"`
	RestoreCredentialRef string `json:"restoreCredentialRef"`
	FenceRole            string `json:"fenceRole"`
	FenceCredentialRef   string `json:"fenceCredentialRef"`
	FenceAccountHost     string `json:"fenceAccountHost"`
}

// LegacyPostgresDefault is the single explicit service through which existing
// InfraSpec `postgres.database` declarations resolve. MappingID is used as the
// binding identity of every legacy resolution.
type LegacyPostgresDefault struct {
	MappingID     string      `json:"mappingId"`
	ServiceID     string      `json:"serviceId"`
	Role          string      `json:"role"`
	Generation    uint64      `json:"generation"`
	CredentialRef string      `json:"credentialRef"`
	TLS           DatabaseTLS `json:"tls"`
}

type DeploymentProfile struct {
	APIVersion        string                 `json:"apiVersion"`
	ID                string                 `json:"id"`
	Topology          DeploymentTopology     `json:"topology"`
	AvailabilityClass AvailabilityClass      `json:"availabilityClass"`
	DatabaseBindings  map[string]string      `json:"databaseBindings,omitempty"`
	LegacyPostgres    *LegacyPostgresDefault `json:"legacyPostgres,omitempty"`
}

type Catalog struct {
	APIVersion string              `json:"apiVersion"`
	Services   []DatabaseService   `json:"services"`
	Bindings   []DatabaseBinding   `json:"bindings"`
	Profiles   []DeploymentProfile `json:"profiles"`
	Retired    []RetiredResource   `json:"retired,omitempty"`
}

type RetiredKind string

const (
	RetiredService RetiredKind = "service"
	// RetiredBinding covers named binding IDs and legacy mapping IDs, which
	// share the TargetIdentity.BindingID namespace.
	RetiredBinding RetiredKind = "binding"
)

// RetiredResource is a permanent tombstone. A retired ID can never be reused,
// so removing and recreating a resource cannot reset its generation and let a
// stale expected identity match a different target.
type RetiredResource struct {
	Kind RetiredKind `json:"kind"`
	ID   string      `json:"id"`
}

type LegacyPostgresDeclaration struct {
	Database string
}

// TargetIdentity is the complete fencing identity of one resolved writable
// target. Accepted work records it and must present it again as Expected.
type TargetIdentity struct {
	ServiceID         string `json:"serviceId"`
	ServiceGeneration uint64 `json:"serviceGeneration"`
	BindingID         string `json:"bindingId"`
	BindingGeneration uint64 `json:"bindingGeneration"`
	Engine            Engine `json:"engine"`
	Database          string `json:"database"`
	Role              string `json:"role"`
}

type ResolveRequest struct {
	DeploymentProfileID  string
	Purpose              ServicePurpose
	LogicalResourceID    string
	LegacyPostgres       *LegacyPostgresDeclaration
	Expected             *TargetIdentity
	RequiredCapabilities []Capability
}

// ResolvedBinding is private resolver output for an execution adapter. It
// carries secret references, so its JSON and fmt forms are redacted to the
// public inspection projection.
type ResolvedBinding struct {
	Target            TargetIdentity
	Purpose           ServicePurpose
	ProfileID         string
	LogicalResourceID string
	ProviderRef       string
	Endpoint          DatabaseEndpoint
	Topology          DatabaseTopology
	Capabilities      []Capability
	CredentialRef     string
	MySQLMaintenance  *MySQLMaintenanceCredentials
	TLSPolicy         DatabaseTLSPolicy
	TLS               DatabaseTLS
	Legacy            bool
}

type TLSRequirementsInspection struct {
	Mode                        TLSMode `json:"mode"`
	MinimumMode                 TLSMode `json:"minimumMode"`
	ServerNameRequired          bool    `json:"serverNameRequired"`
	CAReferenceConfigured       bool    `json:"caReferenceConfigured"`
	ClientCertificateRequired   bool    `json:"clientCertificateRequired"`
	ClientCertificateConfigured bool    `json:"clientCertificateConfigured"`
}

type BindingInspection struct {
	Target            TargetIdentity            `json:"target"`
	Purpose           ServicePurpose            `json:"purpose"`
	ProfileID         string                    `json:"profileId"`
	LogicalResourceID string                    `json:"logicalResourceId,omitempty"`
	ProviderRef       string                    `json:"providerRef"`
	Topology          DatabaseTopology          `json:"topology"`
	Capabilities      []Capability              `json:"capabilities"`
	TLSRequirements   TLSRequirementsInspection `json:"tlsRequirements"`
	CredentialPresent bool                      `json:"credentialPresent"`
	Legacy            bool                      `json:"legacy"`
}

func (r ResolvedBinding) Inspection() BindingInspection {
	return BindingInspection{
		Target: r.Target, Purpose: r.Purpose, ProfileID: r.ProfileID, LogicalResourceID: r.LogicalResourceID,
		ProviderRef: r.ProviderRef, Topology: r.Topology,
		Capabilities: append([]Capability(nil), r.Capabilities...), Legacy: r.Legacy,
		CredentialPresent: r.CredentialRef != "",
		TLSRequirements: TLSRequirementsInspection{
			Mode: r.TLS.Mode, MinimumMode: r.TLSPolicy.MinimumMode,
			ServerNameRequired:    r.TLS.Mode == TLSVerifyFull,
			CAReferenceConfigured: r.TLS.CARef != "",
			// Requirement comes from the service policy, not from whether a
			// binding happens to carry certificate references.
			ClientCertificateRequired:   r.TLSPolicy.RequireClientCertificate,
			ClientCertificateConfigured: r.TLS.ClientCertRef != "" && r.TLS.ClientKeyRef != "",
		},
	}
}

func (r ResolvedBinding) MarshalJSON() ([]byte, error) {
	return jsonMarshal(r.Inspection())
}

func (r ResolvedBinding) String() string {
	return fmt.Sprintf("database binding %s/%s generation %d on service %s generation %d (%s %s as %s)",
		r.ProfileID, r.Target.BindingID, r.Target.BindingGeneration, r.Target.ServiceID, r.Target.ServiceGeneration,
		r.Target.Engine, r.Target.Database, r.Target.Role)
}

func (r ResolvedBinding) GoString() string { return r.String() }

type ErrorCode string

const (
	CodeInvalidCatalog        ErrorCode = "invalid_catalog"
	CodeInvalidRequest        ErrorCode = "invalid_request"
	CodeResolutionNotFound    ErrorCode = "resolution_not_found"
	CodeAmbiguousLegacy       ErrorCode = "ambiguous_legacy"
	CodeUnsupportedEngine     ErrorCode = "unsupported_engine"
	CodeUnsupportedPurpose    ErrorCode = "unsupported_purpose"
	CodePurposeMismatch       ErrorCode = "purpose_mismatch"
	CodeUnsupportedCapability ErrorCode = "unsupported_capability"
	CodeStaleTarget           ErrorCode = "stale_target"
	CodeUnsafeTransition      ErrorCode = "unsafe_transition"
)

// ResolverError never contains caller-supplied values other than identifiers
// that already passed the identifier grammar; secret references, database
// URLs and rejected raw values are never echoed.
type ResolverError struct {
	Code     ErrorCode
	Field    string
	Resource string
	Reason   string
}

func (e *ResolverError) Error() string {
	if e.Resource != "" {
		return fmt.Sprintf("database resolver %s for %s: %s", e.Field, e.Resource, e.Reason)
	}
	return fmt.Sprintf("database resolver %s: %s", e.Field, e.Reason)
}
