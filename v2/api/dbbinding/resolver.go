// Package dbbinding resolves logical database uses to concrete connections for a
// named database service, per ADR 0003 (Accepted). One resolver serves runtime,
// migration, backup, restore and probe consumers so they all reach the same
// target; a binding identifies exactly one writable generation; and credentials
// live behind secret references, never in the resolved topology or status.
//
// This is the backend-neutral binding layer of Norn v3 M2. Provisioning the
// underlying services stays with the protected infrastructure runner; this
// package only resolves references it is given.
package dbbinding

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Engine is a supported database engine. WordPress needs MySQL; control and most
// apps use PostgreSQL. CockroachDB is deliberately not implied by PostgreSQL.
type Engine string

const (
	EnginePostgres Engine = "postgres"
	EngineMySQL    Engine = "mysql"
)

func (e Engine) valid() bool {
	return e == EnginePostgres || e == EngineMySQL
}

// Purpose distinguishes control-plane services from application services.
type Purpose string

const (
	PurposeControl     Purpose = "control"
	PurposeApplication Purpose = "application"
)

func (p Purpose) valid() bool {
	return p == PurposeControl || p == PurposeApplication
}

// DatabaseService describes a provisioned database service. Generation is the
// current authoritative writer generation; a completed cutover advances it so
// consumers can prove they are not writing to a superseded target.
type DatabaseService struct {
	ID           string
	Purpose      Purpose
	Engine       Engine
	Version      string
	Host         string
	Port         int
	ProviderRef  string
	TLS          bool
	BackupPolicy string
	Generation   int64
}

// DatabaseBinding binds a logical use to a database/role on a service. SecretRef
// is a reference into the secret store — never a credential value.
type DatabaseBinding struct {
	ID               string
	ServiceID        string
	Database         string
	Role             string
	SecretRef        string
	ConsistencyGroup string
}

// Secret is credential material fetched from the secret store at resolve time.
type Secret struct {
	Username string
	Password string
}

// SecretResolver fetches credentials for a binding's SecretRef. It is the only
// path credentials enter a resolution; they never live in the service/binding.
type SecretResolver func(ref string) (Secret, error)

// ResolvedConnection is a live connection target. DSN carries credentials and
// must never be logged, exported or placed in API status — use RedactedBinding
// for anything user- or topology-facing.
type ResolvedConnection struct {
	BindingID  string
	ServiceID  string
	Engine     Engine
	Generation int64
	DSN        string
}

// RedactedBinding is the credential-free view safe for exported topology and API
// status.
type RedactedBinding struct {
	BindingID        string  `json:"bindingId"`
	ServiceID        string  `json:"serviceId"`
	Engine           Engine  `json:"engine"`
	Purpose          Purpose `json:"purpose"`
	Database         string  `json:"database"`
	Role             string  `json:"role"`
	Host             string  `json:"host"`
	Port             int     `json:"port"`
	TLS              bool    `json:"tls"`
	Generation       int64   `json:"generation"`
	ConsistencyGroup string  `json:"consistencyGroup,omitempty"`
}

// ErrStaleGeneration is returned when a caller resolves against an authority
// generation older than the service's current one.
type ErrStaleGeneration struct {
	Binding  string
	Expected int64
	Current  int64
}

func (e *ErrStaleGeneration) Error() string {
	return fmt.Sprintf("dbbinding: binding %s expected generation %d but service is at %d", e.Binding, e.Expected, e.Current)
}

// Resolver holds the registered services and bindings and the secret source.
type Resolver struct {
	services map[string]DatabaseService
	bindings map[string]DatabaseBinding
	secrets  SecretResolver
}

// NewResolver returns a resolver backed by the given secret source.
func NewResolver(secrets SecretResolver) *Resolver {
	return &Resolver{
		services: map[string]DatabaseService{},
		bindings: map[string]DatabaseBinding{},
		secrets:  secrets,
	}
}

// RegisterService validates and records a service. Unknown engine or purpose is
// rejected rather than silently accepted.
func (r *Resolver) RegisterService(s DatabaseService) error {
	if s.ID == "" {
		return fmt.Errorf("dbbinding: service id is required")
	}
	if !s.Engine.valid() {
		return fmt.Errorf("dbbinding: service %s has unsupported engine %q", s.ID, s.Engine)
	}
	if !s.Purpose.valid() {
		return fmt.Errorf("dbbinding: service %s has unsupported purpose %q", s.ID, s.Purpose)
	}
	if s.Generation < 1 {
		s.Generation = 1
	}
	r.services[s.ID] = s
	return nil
}

// RegisterBinding validates and records a binding. It requires an existing
// service and a non-empty secret reference (never a literal credential).
func (r *Resolver) RegisterBinding(b DatabaseBinding) error {
	if b.ID == "" {
		return fmt.Errorf("dbbinding: binding id is required")
	}
	if _, ok := r.services[b.ServiceID]; !ok {
		return fmt.Errorf("dbbinding: binding %s references unknown service %q", b.ID, b.ServiceID)
	}
	if b.Database == "" {
		return fmt.Errorf("dbbinding: binding %s requires a database", b.ID)
	}
	if strings.TrimSpace(b.SecretRef) == "" {
		return fmt.Errorf("dbbinding: binding %s requires a secret reference", b.ID)
	}
	r.bindings[b.ID] = b
	return nil
}

func (r *Resolver) lookup(bindingID string) (DatabaseBinding, DatabaseService, error) {
	b, ok := r.bindings[bindingID]
	if !ok {
		return DatabaseBinding{}, DatabaseService{}, fmt.Errorf("dbbinding: unknown binding %q", bindingID)
	}
	s, ok := r.services[b.ServiceID]
	if !ok {
		return DatabaseBinding{}, DatabaseService{}, fmt.Errorf("dbbinding: binding %s references unknown service %q", bindingID, b.ServiceID)
	}
	return b, s, nil
}

// Resolve builds a live connection for the binding, fetching credentials from
// the secret source. The same call serves runtime, migration, backup, restore
// and probe consumers, so they all reach one target and generation.
func (r *Resolver) Resolve(bindingID string) (*ResolvedConnection, error) {
	b, s, err := r.lookup(bindingID)
	if err != nil {
		return nil, err
	}
	if r.secrets == nil {
		return nil, fmt.Errorf("dbbinding: no secret resolver configured")
	}
	secret, err := r.secrets(b.SecretRef)
	if err != nil {
		return nil, fmt.Errorf("dbbinding: resolve secret for binding %s: %w", bindingID, err)
	}
	dsn, err := buildDSN(s, b, secret)
	if err != nil {
		return nil, err
	}
	return &ResolvedConnection{
		BindingID:  b.ID,
		ServiceID:  s.ID,
		Engine:     s.Engine,
		Generation: s.Generation,
		DSN:        dsn,
	}, nil
}

// ResolveAtGeneration resolves only if the caller's expected authority
// generation still matches the service; a superseded target is refused.
func (r *Resolver) ResolveAtGeneration(bindingID string, expected int64) (*ResolvedConnection, error) {
	_, s, err := r.lookup(bindingID)
	if err != nil {
		return nil, err
	}
	if expected != s.Generation {
		return nil, &ErrStaleGeneration{Binding: bindingID, Expected: expected, Current: s.Generation}
	}
	return r.Resolve(bindingID)
}

// Redacted returns the credential-free view for topology/status. It never
// touches the secret source.
func (r *Resolver) Redacted(bindingID string) (*RedactedBinding, error) {
	b, s, err := r.lookup(bindingID)
	if err != nil {
		return nil, err
	}
	return &RedactedBinding{
		BindingID:        b.ID,
		ServiceID:        s.ID,
		Engine:           s.Engine,
		Purpose:          s.Purpose,
		Database:         b.Database,
		Role:             b.Role,
		Host:             s.Host,
		Port:             s.Port,
		TLS:              s.TLS,
		Generation:       s.Generation,
		ConsistencyGroup: b.ConsistencyGroup,
	}, nil
}

// RedactedAll returns every binding's credential-free view, sorted by id, for a
// topology export.
func (r *Resolver) RedactedAll() ([]RedactedBinding, error) {
	out := make([]RedactedBinding, 0, len(r.bindings))
	for id := range r.bindings {
		red, err := r.Redacted(id)
		if err != nil {
			return nil, err
		}
		out = append(out, *red)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BindingID < out[j].BindingID })
	return out, nil
}

func buildDSN(s DatabaseService, b DatabaseBinding, secret Secret) (string, error) {
	switch s.Engine {
	case EnginePostgres:
		sslmode := "disable"
		if s.TLS {
			sslmode = "require"
		}
		u := url.URL{
			Scheme:   "postgres",
			User:     url.UserPassword(secret.Username, secret.Password),
			Host:     fmt.Sprintf("%s:%d", s.Host, s.Port),
			Path:     "/" + b.Database,
			RawQuery: "sslmode=" + sslmode,
		}
		return u.String(), nil
	case EngineMySQL:
		// go-sql-driver DSN: user:pass@tcp(host:port)/db?tls=...
		tls := "false"
		if s.TLS {
			tls = "true"
		}
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?tls=%s",
			secret.Username, secret.Password, s.Host, s.Port, b.Database, tls), nil
	default:
		return "", fmt.Errorf("dbbinding: unsupported engine %q", s.Engine)
	}
}
