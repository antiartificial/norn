package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

const (
	databaseTargetPayloadKey = "databaseTarget"
	recordedTargetSchema     = "norn.database-target/v1"
	// databaseTargetsPayloadKey carries the named-database set of a
	// norn.app/v2 app; an operation records one key or the other, never both.
	databaseTargetsPayloadKey = "databaseTargets"
	recordedTargetSetSchema   = "norn.database-targets/v1"
	snapshotSidecarSchema     = "norn.database-snapshot/v1"
	defaultSnapshotRoot       = "snapshots"
)

// DatabaseTargets binds database-consuming operations to a catalog target.
// When configured, acceptance records the complete target identity in the
// signed payload; execution re-resolves it as Expected, opens private
// connection material and probes the connected identity before any tool
// runs. When nil, v2 behaviour (ambient libpq by database name) is unchanged.
type DatabaseTargets struct {
	ProfileID string
	Catalog   func(context.Context) (store.DatabaseCatalogRevision, error)
	Secrets   database.SecretSource
	// SnapshotRoot defaults to "snapshots" (the v2 directory).
	SnapshotRoot string
	// dumpScope restricts pg_dump in package tests that run inside shared
	// disposable databases; production dumps the whole target database.
	dumpScope []string
}

// recordedTarget is the signed acceptance-time binding.
type recordedTarget struct {
	Schema          string                  `json:"schema"`
	ProfileID       string                  `json:"profileId"`
	CatalogRevision int64                   `json:"catalogRevision"`
	Target          database.TargetIdentity `json:"target"`
	Legacy          bool                    `json:"legacy"`
}

// recordedTargetSet is the signed acceptance-time binding of a v2 app's
// named databases, all resolved against one catalog revision.
type recordedTargetSet struct {
	Schema          string                `json:"schema"`
	ProfileID       string                `json:"profileId"`
	CatalogRevision int64                 `json:"catalogRevision"`
	Targets         []recordedNamedTarget `json:"targets"`
}

type recordedNamedTarget struct {
	Name   string                  `json:"name"`
	Target database.TargetIdentity `json:"target"`
}

// requireQualifiedMySQLRuntime binds transport capability to the one app
// client whose actual startup path has passed CA and hostname negative
// controls. Resolver capability alone cannot establish that an application
// verifies a delivered CA; stock WordPress does not do so by default.
func requireQualifiedMySQLRuntime(spec *model.InfraSpec, requirement model.DatabaseRequirement, resolved database.ResolvedBinding) error {
	if resolved.Target.Engine != database.EngineMySQL || resolved.TLS.Mode == database.TLSDisabled || !containsDatabaseCapability(requirement.Capabilities, string(database.CapabilityRuntime)) {
		return nil
	}
	if spec == nil || spec.StartupAdapter != model.StartupAdapterWordPressVerifiedTLS || requirement.Name != "primary" ||
		resolved.TLS.Mode != database.TLSVerifyFull || resolved.TLS.ServerName != resolved.Endpoint.Host ||
		resolved.TLS.CARef == "" || resolved.TLS.ClientCertRef != "" || resolved.TLS.ClientKeyRef != "" {
		return &DatabaseTargetError{Reason: "verified MySQL runtime requires the qualified WordPress adapter and endpoint-bound verify-full TLS"}
	}
	for _, finding := range spec.DatabaseDeclarationFindings() {
		if finding.Severity == "error" {
			return &DatabaseTargetError{Reason: fmt.Sprintf("verified MySQL runtime declaration is invalid at %s: %s", finding.Field, finding.Message)}
		}
	}
	return nil
}

func containsDatabaseCapability(capabilities []string, want string) bool {
	for _, capability := range capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

var databaseConsumingKinds = map[string]bool{
	"app.deploy": true, "app.snapshot": true, "app.snapshot-prune": true, "app.snapshot-restore": true, "app.migrate": true, DatabaseBaselineKind: true,
}

// DatabaseTargetError marks a refusal to route database work: no recorded
// target, a stale target, or a target this process cannot serve.
type DatabaseTargetError struct {
	Reason string
	// Ambiguous marks refusals caused by unestablished writer evidence (as
	// opposed to a proven target change); only a recorded baseline may
	// resolve them.
	Ambiguous bool
}

func (e *DatabaseTargetError) Error() string { return "database target: " + e.Reason }

func (t *DatabaseTargets) snapshotRoot() string {
	if t == nil || t.SnapshotRoot == "" {
		return defaultSnapshotRoot
	}
	return t.SnapshotRoot
}

// resolverAt returns a resolver over the active catalog and its revision.
func (t *DatabaseTargets) resolverAt(ctx context.Context) (*database.Resolver, int64, error) {
	revision, err := t.Catalog(ctx)
	if err != nil {
		return nil, 0, &DatabaseTargetError{Reason: "no active database catalog is available"}
	}
	resolver, err := database.NewResolver(revision.Catalog)
	if err != nil {
		return nil, 0, err
	}
	return resolver, revision.Revision, nil
}

func (t *DatabaseTargets) resolve(ctx context.Context, spec *model.InfraSpec, expected *database.TargetIdentity, capabilities []database.Capability) (database.ResolvedBinding, int64, error) {
	resolver, revision, err := t.resolverAt(ctx)
	if err != nil {
		return database.ResolvedBinding{}, 0, err
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{
		DeploymentProfileID: t.ProfileID, Purpose: database.PurposeApplication,
		LegacyPostgres: &database.LegacyPostgresDeclaration{Database: spec.Infrastructure.Postgres.Database},
		Expected:       expected, RequiredCapabilities: capabilities,
	})
	return resolved, revision, err
}

// bindDatabaseTargetForRequest records the operation's target in its payload
// before the fingerprint is computed. A retry of an already accepted request
// reuses the target recorded at original acceptance, so a later catalog
// revision neither changes the retry's fingerprint nor hides the original
// receipt; the store then compares the client's semantics as usual, and
// execution rejects the stale target. Legacy mode, non-database kinds and
// apps without postgres are untouched.
func (p *Pipeline) bindDatabaseTargetForRequest(ctx context.Context, request EnqueueRequest, resource string, operation model.Operation) (model.Operation, error) {
	if !databaseConsumingKinds[operation.Kind] {
		return operation, nil
	}
	if p.DatabaseTargets == nil {
		// Named databases never fall back to ambient routing: without a
		// database profile they cannot be served at all. Apps that cannot be
		// found keep the legacy behaviour (the kind's own validation decides).
		if spec, err := p.findSpec(operation.App); err == nil && spec.NamedDatabases() {
			return operation, &DatabaseTargetError{Reason: "app declares named databases but no database profile (NORN_DATABASE_PROFILE) is configured"}
		}
		return operation, nil
	}
	existing, err := p.ResolveEnqueue(ctx, request, operation.Kind, resource)
	switch {
	case err == nil:
		for _, key := range []string{databaseTargetPayloadKey, databaseTargetsPayloadKey} {
			if recorded, present := existing.Operation.Payload[key]; present {
				operation.Payload = withPayloadValue(operation.Payload, key, recorded)
			}
		}
		return operation, nil
	case !errors.Is(err, store.ErrAcceptanceNotFound):
		return operation, err
	}
	return p.bindDatabaseTarget(ctx, operation)
}

func (p *Pipeline) bindDatabaseTarget(ctx context.Context, operation model.Operation) (model.Operation, error) {
	spec, err := p.findSpec(operation.App)
	if err != nil {
		return operation, err
	}
	if spec.NamedDatabases() {
		return p.bindNamedDatabaseTargets(ctx, spec, operation)
	}
	if spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
		return operation, nil
	}
	resolved, revision, err := p.DatabaseTargets.resolve(ctx, spec, nil, nil)
	if err != nil {
		return operation, err
	}
	recorded := recordedTarget{Schema: recordedTargetSchema, ProfileID: p.DatabaseTargets.ProfileID, CatalogRevision: revision, Target: resolved.Target, Legacy: resolved.Legacy}
	encoded, err := json.Marshal(recorded)
	if err != nil {
		return operation, err
	}
	// Stored as an exact JSON string: uint64 generations and revisions never
	// pass through float64 in payload decoding, signing or execution.
	operation.Payload = withPayloadValue(operation.Payload, databaseTargetPayloadKey, string(encoded))
	return operation, nil
}

// bindNamedDatabaseTargets resolves every logical database the operation
// consumes against one catalog revision, requiring the capabilities the app
// declares for it, and records the set in the signed payload.
func (p *Pipeline) bindNamedDatabaseTargets(ctx context.Context, spec *model.InfraSpec, operation model.Operation) (model.Operation, error) {
	for _, finding := range spec.DatabaseDeclarationFindings() {
		if finding.Severity == "error" {
			return operation, &DatabaseTargetError{Reason: fmt.Sprintf("invalid database declaration at %s: %s", finding.Field, finding.Message)}
		}
	}
	names, err := databasesForOperation(spec, operation.Kind, operation.Payload)
	if err != nil {
		return operation, err
	}
	if operation.Kind == "app.deploy" {
		if err := p.checkSecretConflicts(spec); err != nil {
			return operation, err
		}
	}
	resolver, revision, err := p.DatabaseTargets.resolverAt(ctx)
	if err != nil {
		return operation, err
	}
	set := recordedTargetSet{Schema: recordedTargetSetSchema, ProfileID: p.DatabaseTargets.ProfileID, CatalogRevision: revision, Targets: []recordedNamedTarget{}}
	for _, name := range names {
		requirement, _ := spec.DatabaseByName(name)
		// The resolver only accepts restore as a requirement together with an
		// expected identity (execution supplies it); at acceptance the service
		// must still declare every capability the app requests.
		required := []database.Capability{}
		for _, capability := range declaredCapabilities(requirement) {
			if capability != database.CapabilityRestore {
				required = append(required, capability)
			}
		}
		resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: p.DatabaseTargets.ProfileID, Purpose: database.PurposeApplication,
			LogicalResourceID: name, RequiredCapabilities: required})
		if err != nil {
			return operation, err
		}
		if err := requireQualifiedMySQLRuntime(spec, requirement, resolved); err != nil {
			return operation, err
		}
		if runtime := requirement.Runtime; runtime != nil {
			switch resolved.Target.Engine {
			case database.EngineMySQL:
				if runtime.Components == nil || runtime.Env != "" || runtime.FileEnv != "" {
					return operation, &DatabaseTargetError{Reason: "MySQL runtime requires the four structured connection variables rather than a URL"}
				}
				if resolved.TLS.Mode != database.TLSDisabled {
					if runtime.TLS == nil || runtime.TLS.CAFileEnv == "" || runtime.TLS.ClientCertFileEnv != "" || runtime.TLS.ClientKeyFileEnv != "" {
						return operation, &DatabaseTargetError{Reason: "verified MySQL runtime requires a CA file path variable and no client certificate files"}
					}
				} else if runtime.TLS != nil {
					return operation, &DatabaseTargetError{Reason: "MySQL TLS runtime files require a verified TLS target"}
				}
			case database.EnginePostgreSQL:
				if runtime.Components != nil {
					return operation, &DatabaseTargetError{Reason: "PostgreSQL runtime requires URL delivery"}
				}
				if runtime.TLS != nil {
					return operation, &DatabaseTargetError{Reason: "PostgreSQL runtime TLS files are delivered through its connection URL"}
				}
			}
		}
		if len(required) != len(requirement.Capabilities) {
			if _, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: p.DatabaseTargets.ProfileID, Purpose: database.PurposeApplication,
				LogicalResourceID: name, RequiredCapabilities: []database.Capability{database.CapabilityRestore}, Expected: &resolved.Target}); err != nil {
				return operation, err
			}
		}
		set.Targets = append(set.Targets, recordedNamedTarget{Name: name, Target: resolved.Target})
	}
	switch operation.Kind {
	case "app.deploy":
		// Fail fast; execution re-checks against the targets it opens.
		if err := p.requireRunningTargetsUnchanged(ctx, spec.App, "", set.Targets); err != nil {
			return operation, err
		}
	case DatabaseBaselineKind:
		// A baseline may resolve ambiguous (legacy or unrecorded) history,
		// never contradict recorded targets: re-baselining to different
		// targets would be a target move without the cutover lane.
		var targetErr *DatabaseTargetError
		if err := p.requireRunningTargetsUnchanged(ctx, spec.App, "", set.Targets); err != nil && !(errors.As(err, &targetErr) && targetErr.Ambiguous) {
			return operation, err
		}
	}
	encoded, err := json.Marshal(set)
	if err != nil {
		return operation, err
	}
	operation.Payload = withPayloadValue(operation.Payload, databaseTargetsPayloadKey, string(encoded))
	return operation, nil
}

// databasesForOperation is the set of logical databases a kind consumes:
// every declared database for a deploy (runtime delivery, safety snapshots,
// migration), the migration database for app.migrate, and one selected
// database (payload "database", optional when only one is declared) for
// snapshot, prune and restore.
func databasesForOperation(spec *model.InfraSpec, kind string, payload map[string]interface{}) ([]string, error) {
	switch kind {
	case "app.deploy", DatabaseBaselineKind:
		// A baseline attests the targets of writers: databases delivered to
		// processes or migrated. A deploy consumes every declared database.
		names := make([]string, 0, len(spec.Databases))
		for _, requirement := range spec.Databases {
			if kind == DatabaseBaselineKind && !containsCapability(requirement, "runtime") && !containsCapability(requirement, "migration") {
				continue
			}
			names = append(names, requirement.Name)
		}
		if len(names) == 0 {
			return nil, &DatabaseTargetError{Reason: "app declares no database with writers to baseline"}
		}
		sort.Strings(names)
		return names, nil
	case "app.migrate":
		name := spec.EffectiveMigrationDatabase()
		if name == "" {
			return nil, &DatabaseTargetError{Reason: "app declares no migration database"}
		}
		return []string{name}, requireDeclared(spec, name, "migration", "snapshot")
	case "app.snapshot", "app.snapshot-prune":
		name, err := selectedDatabase(spec, payload)
		if err != nil {
			return nil, err
		}
		return []string{name}, requireDeclared(spec, name, "snapshot")
	case "app.snapshot-restore":
		name, err := selectedDatabase(spec, payload)
		if err != nil {
			return nil, err
		}
		return []string{name}, requireDeclared(spec, name, "restore", "snapshot")
	}
	return nil, &DatabaseTargetError{Reason: "operation kind does not consume a database"}
}

func selectedDatabase(spec *model.InfraSpec, payload map[string]interface{}) (string, error) {
	name, _ := payload["database"].(string)
	if name == "" {
		if len(spec.Databases) != 1 {
			return "", &DatabaseTargetError{Reason: "payload.database must name one of the app's declared databases"}
		}
		return spec.Databases[0].Name, nil
	}
	if _, ok := spec.DatabaseByName(name); !ok {
		return "", &DatabaseTargetError{Reason: fmt.Sprintf("app does not declare database %q", name)}
	}
	return name, nil
}

func requireDeclared(spec *model.InfraSpec, name string, capabilities ...string) error {
	requirement, _ := spec.DatabaseByName(name)
	for _, capability := range capabilities {
		found := false
		for _, declared := range requirement.Capabilities {
			found = found || declared == capability
		}
		if !found {
			return &DatabaseTargetError{Reason: fmt.Sprintf("database %q does not declare the %s capability this operation needs", name, capability)}
		}
	}
	return nil
}

func declaredCapabilities(requirement model.DatabaseRequirement) []database.Capability {
	out := make([]database.Capability, 0, len(requirement.Capabilities))
	for _, capability := range requirement.Capabilities {
		out = append(out, database.Capability(capability))
	}
	return out
}

func withPayloadValue(payload map[string]interface{}, key string, value interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(payload)+1)
	for existingKey, existing := range payload {
		out[existingKey] = existing
	}
	out[key] = value
	return out
}

func (p *Pipeline) findSpec(app string) (*model.InfraSpec, error) {
	specs, err := model.DiscoverAllApps(p.AppsDir)
	if err != nil {
		return nil, fmt.Errorf("discover apps: %w", err)
	}
	for _, spec := range specs {
		if spec.App == app {
			return spec, nil
		}
	}
	return nil, fmt.Errorf("app %s not found", app)
}

func recordedTargetFromPayload(payload map[string]interface{}) (*recordedTarget, error) {
	raw, ok := payload[databaseTargetPayloadKey]
	if !ok {
		return nil, nil
	}
	encoded, isString := raw.(string)
	var recorded recordedTarget
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var trailing json.RawMessage
	if !isString || decoder.Decode(&recorded) != nil || decoder.Decode(&trailing) != io.EOF || recorded.Schema != recordedTargetSchema {
		return nil, &DatabaseTargetError{Reason: "recorded database target is malformed"}
	}
	return &recorded, nil
}

func recordedTargetSetFromPayload(payload map[string]interface{}) (*recordedTargetSet, error) {
	raw, ok := payload[databaseTargetsPayloadKey]
	if !ok {
		return nil, nil
	}
	encoded, isString := raw.(string)
	var recorded recordedTargetSet
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var trailing json.RawMessage
	if !isString || decoder.Decode(&recorded) != nil || decoder.Decode(&trailing) != io.EOF || recorded.Schema != recordedTargetSetSchema {
		return nil, &DatabaseTargetError{Reason: "recorded database targets are malformed"}
	}
	seen := map[string]bool{}
	for _, target := range recorded.Targets {
		if target.Name == "" || seen[target.Name] {
			return nil, &DatabaseTargetError{Reason: "recorded database targets are malformed"}
		}
		seen[target.Name] = true
	}
	return &recorded, nil
}

// boundDatabase is an opened, probed target for one execution.
type boundDatabase struct {
	session  *database.Session
	resolved database.ResolvedBinding
	recorded recordedTarget
	// name is the logical database for named bindings ("" for legacy).
	name string
}

func (b *boundDatabase) Close() error {
	if b == nil {
		return nil
	}
	return b.session.Close()
}

// boundDatabases is every target an execution opened: the one legacy
// mapping for a v1 app, or the recorded named databases of a v2 app.
type boundDatabases struct {
	legacy *boundDatabase
	named  map[string]*boundDatabase
	// revision is the catalog revision the named targets were re-resolved
	// against at execution; it fences runtime delivery.
	revision int64
}

func (b *boundDatabases) Close() error {
	if b == nil {
		return nil
	}
	var first error
	for _, bound := range b.all() {
		if err := bound.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// all returns the opened targets, legacy first, then named by name.
func (b *boundDatabases) all() []*boundDatabase {
	if b == nil {
		return nil
	}
	out := []*boundDatabase{}
	if b.legacy != nil {
		out = append(out, b.legacy)
	}
	names := make([]string, 0, len(b.named))
	for name := range b.named {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, b.named[name])
	}
	return out
}

// forMigration is the target migrations run against: the legacy mapping, or
// the app's migration database.
func (b *boundDatabases) forMigration(spec *model.InfraSpec) *boundDatabase {
	if b == nil {
		return nil
	}
	if spec.NamedDatabases() {
		return b.named[spec.EffectiveMigrationDatabase()]
	}
	return b.legacy
}

// databaseForState opens the recorded targets once per deploy execution.
func (p *Pipeline) databaseForState(ctx context.Context, st *state) (*boundDatabases, error) {
	if st.databaseOpened {
		return st.database, nil
	}
	bound, err := p.openDatabaseTargets(ctx, st.operationPayload, st.spec)
	if err != nil {
		return nil, err
	}
	st.database, st.databaseOpened = bound, true
	return bound, nil
}

func (b *boundDatabase) requireCapabilities(capabilities ...database.Capability) error {
	declared := map[database.Capability]bool{}
	for _, capability := range b.resolved.Capabilities {
		declared[capability] = true
	}
	for _, capability := range capabilities {
		if !declared[capability] {
			return &DatabaseTargetError{Reason: fmt.Sprintf("target does not declare the %s capability", capability)}
		}
	}
	return nil
}

// openDatabaseTarget returns nil in legacy mode. Otherwise it requires the
// target recorded at acceptance, re-resolves it against the current catalog
// with that exact identity, opens private material and probes the
// connection. Any mismatch fails; nothing falls back to ambient routing.
func (p *Pipeline) openDatabaseTarget(ctx context.Context, payload map[string]interface{}, spec *model.InfraSpec) (*boundDatabase, error) {
	set, err := p.openDatabaseTargets(ctx, payload, spec)
	if err != nil || set == nil {
		return nil, err
	}
	if set.legacy == nil {
		_ = set.Close()
		return nil, &DatabaseTargetError{Reason: "operation targets named databases"}
	}
	return set.legacy, nil
}

// openDatabaseTargets opens every target recorded at acceptance. It returns
// nil only in legacy mode for a v1 app. A v2 app's named databases are never
// served without a profile and never fall back to ambient routing.
func (p *Pipeline) openDatabaseTargets(ctx context.Context, payload map[string]interface{}, spec *model.InfraSpec) (*boundDatabases, error) {
	legacy, err := recordedTargetFromPayload(payload)
	if err != nil {
		return nil, err
	}
	named, err := recordedTargetSetFromPayload(payload)
	if err != nil {
		return nil, err
	}
	if legacy != nil && named != nil {
		return nil, &DatabaseTargetError{Reason: "operation records both a legacy and a named database target"}
	}
	if p.DatabaseTargets == nil {
		if legacy != nil || named != nil {
			return nil, &DatabaseTargetError{Reason: "operation was accepted with a database target but no database profile is configured"}
		}
		if spec.NamedDatabases() {
			return nil, &DatabaseTargetError{Reason: "app declares named databases but no database profile (NORN_DATABASE_PROFILE) is configured"}
		}
		return nil, nil
	}
	if spec.NamedDatabases() {
		if legacy != nil {
			return nil, &DatabaseTargetError{Reason: "operation was accepted for a legacy database declaration the app no longer has"}
		}
		if named == nil {
			return nil, &DatabaseTargetError{Reason: "operation was accepted without database targets; resubmit it under the active database catalog"}
		}
		return p.openNamedTargets(ctx, named, spec)
	}
	if named != nil {
		return nil, &DatabaseTargetError{Reason: "operation was accepted for named databases the app no longer declares"}
	}
	if legacy == nil {
		return nil, &DatabaseTargetError{Reason: "operation was accepted without a database target; resubmit it under the active database catalog"}
	}
	if legacy.ProfileID != p.DatabaseTargets.ProfileID {
		return nil, &DatabaseTargetError{Reason: "operation was accepted under a different database profile"}
	}
	if spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
		return nil, &DatabaseTargetError{Reason: "app no longer declares its database"}
	}
	expected := legacy.Target
	resolved, _, err := p.DatabaseTargets.resolve(ctx, spec, &expected, nil)
	if err != nil {
		return nil, fmt.Errorf("re-resolve accepted database target: %w", err)
	}
	bound, err := p.openResolved(ctx, resolved, *legacy, "")
	if err != nil {
		return nil, err
	}
	return &boundDatabases{legacy: bound}, nil
}

func (p *Pipeline) openNamedTargets(ctx context.Context, recorded *recordedTargetSet, spec *model.InfraSpec) (*boundDatabases, error) {
	if recorded.ProfileID != p.DatabaseTargets.ProfileID {
		return nil, &DatabaseTargetError{Reason: "operation was accepted under a different database profile"}
	}
	resolver, revision, err := p.DatabaseTargets.resolverAt(ctx)
	if err != nil {
		return nil, err
	}
	set := &boundDatabases{named: map[string]*boundDatabase{}, revision: revision}
	for _, entry := range recorded.Targets {
		requirement, ok := spec.DatabaseByName(entry.Name)
		if !ok {
			_ = set.Close()
			return nil, &DatabaseTargetError{Reason: fmt.Sprintf("app no longer declares database %q", entry.Name)}
		}
		expected := entry.Target
		resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: p.DatabaseTargets.ProfileID, Purpose: database.PurposeApplication,
			LogicalResourceID: entry.Name, Expected: &expected, RequiredCapabilities: declaredCapabilities(requirement)})
		if err != nil {
			_ = set.Close()
			return nil, fmt.Errorf("re-resolve accepted database %s: %w", entry.Name, err)
		}
		if err := requireQualifiedMySQLRuntime(spec, requirement, resolved); err != nil {
			_ = set.Close()
			return nil, err
		}
		bound, err := p.openResolved(ctx, resolved, recordedTarget{Schema: recordedTargetSetSchema, ProfileID: recorded.ProfileID, CatalogRevision: recorded.CatalogRevision, Target: entry.Target}, entry.Name)
		if err != nil {
			_ = set.Close()
			return nil, err
		}
		set.named[entry.Name] = bound
	}
	return set, nil
}

// openResolved opens private material for a resolved target and proves the
// connection reaches the declared database as the declared role.
func (p *Pipeline) openResolved(ctx context.Context, resolved database.ResolvedBinding, recorded recordedTarget, name string) (*boundDatabase, error) {
	session, err := database.OpenSession(ctx, resolved, p.DatabaseTargets.Secrets)
	if err != nil {
		return nil, err
	}
	if _, err := session.Probe(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	return &boundDatabase{session: session, resolved: resolved, recorded: recorded, name: name}, nil
}

// snapshotLocation is where one target's snapshots live. Legacy mode keeps
// the v2 flat directory keyed by database name. A legacy-mapped target (the
// explicit Mini compatibility default) keeps that flat namespace under a
// unique recorded owner and may restore only the pre-v3 files inventoried
// when that owner adopted it; a named binding gets its own target-keyed
// namespace and requires sidecars.
type snapshotLocation struct {
	dir      string
	database string
	bound    *boundDatabase
	adopted  map[string]legacySnapshot
	// adoptedTarget is the exact target the adopted inventory was recorded
	// for; after any generation or identity change those dumps are foreign.
	adoptedTarget database.TargetIdentity
	dumpScope     []string
}

const (
	legacyOwnerSchema    = "norn.legacy-snapshot-owner/v1"
	legacyOwnerDirectory = ".norn-legacy"
)

// legacySnapshotOwner claims one flat database-name namespace for exactly one
// profile's legacy mapping on one service, with the unbound dumps present at
// adoption. Another owner is refused rather than sharing the namespace. The
// inventory belongs to the exact target identity recorded at adoption.
type legacySnapshotOwner struct {
	Schema    string                  `json:"schema"`
	Database  string                  `json:"database"`
	ProfileID string                  `json:"profileId"`
	MappingID string                  `json:"mappingId"`
	ServiceID string                  `json:"serviceId"`
	Target    database.TargetIdentity `json:"target"`
	Inventory []legacySnapshot        `json:"inventory"`
}

type legacySnapshot struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

var errLegacyNamespaceOwned = errors.New("legacy snapshot namespace is owned by another database mapping")

func (p *Pipeline) prepareSnapshotLocation(databaseName string, bound *boundDatabase) (snapshotLocation, error) {
	root := p.DatabaseTargets.snapshotRoot()
	if bound == nil {
		return snapshotLocation{dir: root, database: databaseName}, nil
	}
	location := snapshotLocation{dir: root, database: bound.resolved.Target.Database, bound: bound, dumpScope: p.DatabaseTargets.dumpScope}
	if !bound.resolved.Legacy {
		location.dir = filepath.Join(root, "targets", targetNamespace(bound.resolved.Target))
		return location, nil
	}
	owner, err := adoptLegacyNamespace(location, legacySnapshotOwner{Schema: legacyOwnerSchema, Database: location.database, ProfileID: p.DatabaseTargets.ProfileID,
		MappingID: bound.resolved.Target.BindingID, ServiceID: bound.resolved.Target.ServiceID, Target: bound.resolved.Target})
	if err != nil {
		return snapshotLocation{}, err
	}
	location.adoptedTarget = owner.Target
	location.adopted = map[string]legacySnapshot{}
	for _, entry := range owner.Inventory {
		location.adopted[entry.Filename] = entry
	}
	return location, nil
}

// adoptLegacyNamespace returns the recorded owner of a flat namespace, or
// records want as its owner with an inventory of the unbound dumps present
// now. The manifest is published by exclusive link, so concurrent first uses
// agree on one owner and one inventory.
func adoptLegacyNamespace(location snapshotLocation, want legacySnapshotOwner) (*legacySnapshotOwner, error) {
	directory := filepath.Join(location.dir, legacyOwnerDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create legacy snapshot ownership directory: %w", err)
	}
	path := filepath.Join(directory, location.database+".json")
	if owner, err := readLegacyOwner(path); err != nil || owner != nil {
		return checkLegacyOwner(owner, want, err)
	}
	unbound, err := listDataSnapshots(snapshotLocation{dir: location.dir, database: location.database})
	if err != nil {
		return nil, err
	}
	want.Inventory = []legacySnapshot{}
	for _, snapshot := range unbound {
		if sidecar, err := readSidecar(location, snapshot.Filename); err != nil || sidecar != nil {
			continue // already bound (or unreadable provenance): never adopted
		}
		digest, err := fileSHA256(filepath.Join(location.dir, snapshot.Filename))
		if err != nil {
			return nil, err
		}
		want.Inventory = append(want.Inventory, legacySnapshot{Filename: snapshot.Filename, SHA256: digest, Size: snapshot.Size})
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp(directory, ".owner-*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return nil, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(temporary.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("record legacy snapshot owner: %w", err)
	}
	owner, err := readLegacyOwner(path)
	if owner == nil && err == nil {
		err = fmt.Errorf("legacy snapshot owner manifest disappeared")
	}
	return checkLegacyOwner(owner, want, err)
}

func checkLegacyOwner(owner *legacySnapshotOwner, want legacySnapshotOwner, err error) (*legacySnapshotOwner, error) {
	if err != nil {
		return nil, err
	}
	if owner.Database != want.Database || owner.ProfileID != want.ProfileID || owner.MappingID != want.MappingID || owner.ServiceID != want.ServiceID {
		return nil, fmt.Errorf("snapshots for %s: %w", want.Database, errLegacyNamespaceOwned)
	}
	return owner, nil
}

func readLegacyOwner(path string) (*legacySnapshotOwner, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read legacy snapshot owner: %w", err)
	}
	defer file.Close()
	var owner legacySnapshotOwner
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var trailing json.RawMessage
	if decoder.Decode(&owner) != nil || decoder.Decode(&trailing) != io.EOF || owner.Schema != legacyOwnerSchema {
		return nil, fmt.Errorf("legacy snapshot owner manifest is malformed")
	}
	return &owner, nil
}

func targetNamespace(target database.TargetIdentity) string {
	digest := sha256.Sum256([]byte(target.ServiceID + "\x00" + target.BindingID + "\x00" + string(target.Engine) + "\x00" + target.Database))
	return hex.EncodeToString(digest[:16])
}

type snapshotSidecar struct {
	Schema          string                  `json:"schema"`
	Target          database.TargetIdentity `json:"target"`
	CatalogRevision int64                   `json:"catalogRevision"`
	SHA256          string                  `json:"sha256"`
	Size            int64                   `json:"size"`
}

var errSnapshotTargetMismatch = errors.New("snapshot belongs to a different database target")

const sidecarSuffix = ".target.json"

func readSidecar(location snapshotLocation, filename string) (*snapshotSidecar, error) {
	data, err := os.ReadFile(filepath.Join(location.dir, filename+sidecarSuffix))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sidecar snapshotSidecar
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var trailing json.RawMessage
	if decoder.Decode(&sidecar) != nil || decoder.Decode(&trailing) != io.EOF || sidecar.Schema != snapshotSidecarSchema {
		return nil, fmt.Errorf("snapshot %s has a malformed target sidecar", filename)
	}
	return &sidecar, nil
}

// verifyBoundDump proves an existing dump belongs to the location's target:
// its sidecar names this target and matches the bytes. A dump without a
// sidecar has no provenance and is refused; it is never adopted.
func verifyBoundDump(location snapshotLocation, filename string, size int64) error {
	sidecar, err := readSidecar(location, filename)
	if err != nil {
		return err
	}
	if sidecar == nil {
		return fmt.Errorf("snapshot %s exists without target provenance; refusing to reuse or overwrite it", filename)
	}
	if sidecar.Target != location.bound.resolved.Target {
		return fmt.Errorf("snapshot %s: %w", filename, errSnapshotTargetMismatch)
	}
	digest, err := fileSHA256(filepath.Join(location.dir, filename))
	if err != nil {
		return err
	}
	if digest != sidecar.SHA256 || size != sidecar.Size {
		return fmt.Errorf("snapshot %s content differs from its target sidecar", filename)
	}
	return nil
}

// writeSidecarExclusive records a dump's provenance before the dump itself is
// published under filename. It fails with fs.ErrExist if any sidecar (for
// example an orphan from an interrupted publication) already holds the name.
func writeSidecarExclusive(location snapshotLocation, filename, digest string, size int64) error {
	encoded, err := json.Marshal(snapshotSidecar{Schema: snapshotSidecarSchema, Target: location.bound.resolved.Target, CatalogRevision: location.bound.recorded.CatalogRevision, SHA256: digest, Size: size})
	if err != nil {
		return err
	}
	path := filepath.Join(location.dir, filename+sidecarSuffix)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return err
	}
	if err != nil {
		return fmt.Errorf("write snapshot target sidecar: %w", err)
	}
	if _, err = file.Write(encoded); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write snapshot target sidecar: %w", err)
	}
	return nil
}

// verifySnapshotContent checks a bound snapshot's bytes against its sidecar,
// or an adopted legacy dump against its ownership inventory, before anything
// is restored from it.
func verifySnapshotContent(location snapshotLocation, snapshot dataSnapshot) error {
	var digest string
	var size int64
	switch {
	case snapshot.sidecar != nil:
		digest, size = snapshot.sidecar.SHA256, snapshot.sidecar.Size
	case snapshot.adopted != nil:
		digest, size = snapshot.adopted.SHA256, snapshot.adopted.Size
	default:
		if location.bound != nil {
			return fmt.Errorf("snapshot %s has no target provenance", snapshot.Filename)
		}
		return nil
	}
	actual, err := fileSHA256(filepath.Join(location.dir, snapshot.Filename))
	if err != nil {
		return err
	}
	if actual != digest || snapshot.Size != size {
		return fmt.Errorf("snapshot %s content differs from its target sidecar", snapshot.Filename)
	}
	return nil
}

func restoreDataSnapshot(ctx context.Context, location snapshotLocation, snapshot dataSnapshot) error {
	path := filepath.Join(location.dir, snapshot.Filename)
	if location.bound == nil {
		cmd := exec.CommandContext(ctx, "pg_restore", "--single-transaction", "--clean", "--if-exists", "-d", location.database, path)
		if output, err := runCaptured(cmd); err != nil {
			return fmt.Errorf("pg_restore: %s", strings.TrimSpace(output.String()))
		}
		return nil
	}
	session := location.bound.session
	if output, err := runCaptured(session.Command(ctx, "pg_restore", "--single-transaction", "--clean", "--if-exists", "-d", session.ServiceArgument(), path)); err != nil {
		return fmt.Errorf("pg_restore: %s", session.RedactCaptured(output))
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Aliases for files whose local variables are named "database".
type dbCapability = database.Capability

const (
	dbSnapshot  = database.CapabilitySnapshot
	dbRestore   = database.CapabilityRestore
	dbMigration = database.CapabilityMigration
)
