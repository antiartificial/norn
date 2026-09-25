package model

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// AppSchemaV2 is the only InfraSpec schemaVersion besides the implicit v1.
// It is required to declare named databases; Fleet documents are unaffected.
const AppSchemaV2 = "norn.app/v2"

const (
	// StartupAdapterWordPressVerifiedTLS installs Norn's version-pinned wpdb
	// drop-in before WordPress's normal Apache entrypoint when an exact OCI
	// manifest has been independently qualified.
	StartupAdapterWordPressVerifiedTLS = "wordpress-verified-tls/v1"
	// QualifiedWordPressVerifiedTLSImage is the Docker Hub OCI index verified
	// by `docker buildx imagetools inspect` for the allocation qualification.
	QualifiedWordPressVerifiedTLSImage = "docker.io/library/wordpress:6.8.2-php8.3-apache@sha256:09ac1315368f234db7559e4f9dcca3178a5efc6f2193b88289252abe18551522"
)

// DatabaseRequirement names one logical database the app consumes. The name
// is the key of the deployment profile's databaseBindings; the server, role,
// database name and credentials come only from the database catalog.
type DatabaseRequirement struct {
	Name         string           `yaml:"name" json:"name"`
	Purpose      string           `yaml:"purpose" json:"purpose"`
	Capabilities []string         `yaml:"capabilities" json:"capabilities"`
	Runtime      *DatabaseRuntime `yaml:"runtime,omitempty" json:"runtime,omitempty"`
}

// DatabaseRuntime names the variables that carry the connection into every
// process and migration. Env receives the connection value itself (a
// standard URL any ordinary client accepts). FileEnv receives only the path
// of a private file containing that value. They are never interchangeable.
type DatabaseRuntime struct {
	Env        string                     `yaml:"env,omitempty" json:"env,omitempty"`
	FileEnv    string                     `yaml:"fileEnv,omitempty" json:"fileEnv,omitempty"`
	Components *DatabaseRuntimeComponents `yaml:"components,omitempty" json:"components,omitempty"`
	TLS        *DatabaseRuntimeTLS        `yaml:"tls,omitempty" json:"tls,omitempty"`
}

// DatabaseRuntimeComponents delivers the values ordinary database clients
// expect as separate variables. WordPress, for example, consumes four
// WORDPRESS_DB_* variables instead of a connection URL.
type DatabaseRuntimeComponents struct {
	Host     string `yaml:"host" json:"host"`
	User     string `yaml:"user" json:"user"`
	Password string `yaml:"password" json:"password"`
	Name     string `yaml:"name" json:"name"`
}

// DatabaseRuntimeTLS names environment variables that carry paths to
// allocation-private PEM files. The files are rendered by Nomad, never put in
// a job specification or environment value. An application must explicitly
// configure its database client to use these paths.
//
// This declares delivery shape only. Database adapters remain responsible for
// deciding when verified TLS runtime delivery is qualified and available.
type DatabaseRuntimeTLS struct {
	CAFileEnv         string `yaml:"caFileEnv" json:"caFileEnv"`
	ClientCertFileEnv string `yaml:"clientCertFileEnv,omitempty" json:"clientCertFileEnv,omitempty"`
	ClientKeyFileEnv  string `yaml:"clientKeyFileEnv,omitempty" json:"clientKeyFileEnv,omitempty"`
}

func (r *DatabaseRuntime) envNames() map[string]string {
	if r == nil {
		return nil
	}
	names := map[string]string{"env": r.Env, "fileEnv": r.FileEnv}
	if r.Components != nil {
		names["components.host"] = r.Components.Host
		names["components.user"] = r.Components.User
		names["components.password"] = r.Components.Password
		names["components.name"] = r.Components.Name
	}
	if r.TLS != nil {
		names["tls.caFileEnv"] = r.TLS.CAFileEnv
		names["tls.clientCertFileEnv"] = r.TLS.ClientCertFileEnv
		names["tls.clientKeyFileEnv"] = r.TLS.ClientKeyFileEnv
	}
	return names
}

var databaseLogicalNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

var knownDatabaseCapabilities = map[string]bool{"runtime": true, "migration": true, "snapshot": true, "restore": true, "health": true}

// NamedDatabases reports whether the spec uses v2 named declarations.
func (s *InfraSpec) NamedDatabases() bool {
	return s != nil && len(s.Databases) > 0
}

// DeclaresDatabase reports whether the app consumes any application database,
// named or legacy.
func (s *InfraSpec) DeclaresDatabase() bool {
	return s.NamedDatabases() || (s != nil && s.Infrastructure != nil && s.Infrastructure.Postgres != nil)
}

// DatabaseByName returns a declared named database.
func (s *InfraSpec) DatabaseByName(name string) (DatabaseRequirement, bool) {
	for _, requirement := range s.Databases {
		if requirement.Name == name {
			return requirement, true
		}
	}
	return DatabaseRequirement{}, false
}

// EffectiveMigrationDatabase is the named database migrations run against:
// migrationDatabase, or the only declared database.
func (s *InfraSpec) EffectiveMigrationDatabase() string {
	if s.MigrationDatabase != "" {
		return s.MigrationDatabase
	}
	if len(s.Databases) == 1 {
		return s.Databases[0].Name
	}
	return ""
}

// DatabaseEnvNames returns every variable Norn owns for database delivery,
// mapped to the logical database it belongs to.
func (s *InfraSpec) DatabaseEnvNames() map[string]string {
	names := map[string]string{}
	for _, requirement := range s.Databases {
		if requirement.Runtime == nil {
			continue
		}
		for _, name := range requirement.Runtime.envNames() {
			if name != "" {
				names[name] = requirement.Name
			}
		}
	}
	return names
}

// DatabaseEnvConflicts lists keys from other environment sources (app
// secrets, request context) that would collide with Norn-owned database
// variables. Any conflict must be refused before side effects.
func (s *InfraSpec) DatabaseEnvConflicts(keys ...map[string]string) []string {
	owned := s.DatabaseEnvNames()
	conflicts := map[string]bool{}
	for _, source := range keys {
		for key := range source {
			if _, ok := owned[key]; ok {
				conflicts[key] = true
			}
		}
	}
	out := make([]string, 0, len(conflicts))
	for key := range conflicts {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// DatabaseDeclarationFindings validates schemaVersion and named database
// declarations. It is shared by spec validation and by acceptance, so an
// invalid declaration can never be resolved or delivered.
func (s *InfraSpec) DatabaseDeclarationFindings() []ValidationFinding {
	r := &ValidationResult{Valid: true}
	validateDatabaseDeclarations(r, s)
	return r.Findings
}

func validateDatabaseDeclarations(r *ValidationResult, spec *InfraSpec) {
	defer validateStartupAdapter(r, spec)
	switch spec.SchemaVersion {
	case "", AppSchemaV2:
	default:
		r.add("error", "schemaVersion", fmt.Sprintf("schemaVersion must be omitted (v1) or %q", AppSchemaV2))
		return
	}
	if spec.SchemaVersion != AppSchemaV2 {
		if len(spec.Databases) > 0 {
			r.add("error", "databases", "databases requires schemaVersion: "+AppSchemaV2)
		}
		if spec.MigrationDatabase != "" {
			r.add("error", "migrationDatabase", "migrationDatabase requires schemaVersion: "+AppSchemaV2)
		}
		return
	}
	if spec.Infrastructure != nil && spec.Infrastructure.Postgres != nil {
		r.add("error", "infrastructure.postgres", "a "+AppSchemaV2+" spec declares databases by name; infrastructure.postgres is v1 only")
	}
	owned := map[string]string{}
	names := map[string]bool{}
	for index, requirement := range spec.Databases {
		field := fmt.Sprintf("databases[%d]", index)
		if !databaseLogicalNameRe.MatchString(requirement.Name) {
			r.add("error", field+".name", "database name must match ^[a-z][a-z0-9-]{0,62}$")
		} else if names[requirement.Name] {
			r.add("error", field+".name", fmt.Sprintf("database %q is declared twice", requirement.Name))
		}
		names[requirement.Name] = true
		if requirement.Purpose != "application" {
			r.add("error", field+".purpose", "purpose must be application")
		}
		capabilities := map[string]bool{}
		for _, capability := range requirement.Capabilities {
			if !knownDatabaseCapabilities[capability] {
				r.add("error", field+".capabilities", fmt.Sprintf("unknown capability %q", capability))
			} else if capabilities[capability] {
				r.add("error", field+".capabilities", fmt.Sprintf("capability %q is duplicated", capability))
			}
			capabilities[capability] = true
		}
		if capabilities["restore"] && !capabilities["snapshot"] {
			r.add("error", field+".capabilities", "restore requires snapshot (restore takes a safety snapshot first)")
		}
		runtime := requirement.Runtime
		if (runtime != nil) != capabilities["runtime"] {
			r.add("error", field+".runtime", "a runtime block and the runtime capability must be declared together")
		}
		if runtime == nil {
			continue
		}
		if runtime.Env == "" && runtime.FileEnv == "" && runtime.Components == nil {
			r.add("error", field+".runtime", "runtime must name env, fileEnv or components")
		}
		if runtime.Components != nil && (runtime.Components.Host == "" || runtime.Components.User == "" || runtime.Components.Password == "" || runtime.Components.Name == "") {
			r.add("error", field+".runtime.components", "host, user, password and name variables are required together")
		}
		if runtime.TLS != nil {
			if runtime.TLS.CAFileEnv == "" {
				r.add("error", field+".runtime.tls.caFileEnv", "a CA file variable is required when TLS runtime files are declared")
			}
			if (runtime.TLS.ClientCertFileEnv == "") != (runtime.TLS.ClientKeyFileEnv == "") {
				r.add("error", field+".runtime.tls", "client certificate and key file variables are required together")
			}
		}
		for key, name := range runtime.envNames() {
			if name == "" {
				continue
			}
			subfield := field + ".runtime." + key
			switch {
			case !envNameRe.MatchString(name):
				r.add("error", subfield, "variable name must match ^[A-Za-z][A-Za-z0-9_]*$")
			case strings.HasPrefix(strings.ToUpper(name), "NORN_"), strings.HasPrefix(strings.ToUpper(name), "NOMAD_"):
				r.add("error", subfield, "NORN_ and NOMAD_ variables are reserved")
			case owned[name] != "":
				r.add("error", subfield, fmt.Sprintf("variable %s is already delivered for another database value", name))
			}
			owned[name] = requirement.Name + "." + key
		}
	}
	if len(spec.Databases) == 0 {
		r.add("error", "databases", AppSchemaV2+" requires at least one database; omit schemaVersion for an app without databases")
	}
	// Norn-owned variables must have exactly one source in every job type.
	ownedNames := make([]string, 0, len(owned))
	for name := range owned {
		ownedNames = append(ownedNames, name)
	}
	sort.Strings(ownedNames)
	for _, name := range ownedNames {
		if _, ok := spec.Env[name]; ok {
			r.add("error", "env."+name, fmt.Sprintf("%s is delivered by the database binding and cannot also be set in env", name))
		}
		for _, secret := range spec.Secrets {
			if strings.EqualFold(strings.TrimSpace(secret), name) {
				r.add("error", "secrets", fmt.Sprintf("%s is delivered by the database binding and cannot also be an app secret", name))
			}
		}
	}
	processNames := make([]string, 0, len(spec.Processes))
	for processName := range spec.Processes {
		processNames = append(processNames, processName)
	}
	sort.Strings(processNames)
	for _, processName := range processNames {
		for _, name := range ownedNames {
			if _, ok := spec.Processes[processName].Env[name]; ok {
				r.add("error", fmt.Sprintf("processes.%s.env.%s", processName, name), fmt.Sprintf("%s is delivered by the database binding and cannot also be set per process", name))
			}
		}
	}
	migration := spec.EffectiveMigrationDatabase()
	if spec.MigrationDatabase != "" && !names[spec.MigrationDatabase] {
		r.add("error", "migrationDatabase", fmt.Sprintf("migrationDatabase %q is not declared", spec.MigrationDatabase))
	} else if strings.TrimSpace(spec.Migrations) != "" {
		if migration == "" {
			r.add("error", "migrationDatabase", "migrationDatabase is required when migrations run and several databases are declared")
		} else if requirement, _ := spec.DatabaseByName(migration); !containsString(requirement.Capabilities, "migration") {
			r.add("error", "migrationDatabase", fmt.Sprintf("database %q must declare the migration capability", migration))
		}
	}
}

// validateStartupAdapter keeps the one supported WordPress verified-TLS
// startup shape deliberately small. The adapter relies on the official image
// entrypoint and its db.php extension contract. It is bound to the exact
// qualified OCI index rather than a mutable WordPress tag.
func validateStartupAdapter(r *ValidationResult, spec *InfraSpec) {
	if spec.StartupAdapter == "" {
		return
	}
	if spec.StartupAdapter != StartupAdapterWordPressVerifiedTLS {
		r.add("error", "startupAdapter", "startupAdapter is unsupported")
		return
	}
	if spec.SchemaVersion != AppSchemaV2 {
		r.add("error", "startupAdapter", "wordpress verified TLS requires schemaVersion: "+AppSchemaV2)
	}
	if spec.Build == nil || spec.Build.Image != QualifiedWordPressVerifiedTLSImage {
		r.add("error", "build.image", "wordpress verified TLS requires the qualified WordPress OCI image digest")
	}
	if len(spec.Processes) != 1 {
		r.add("error", "processes", "wordpress verified TLS supports exactly one web process")
	}
	web, ok := spec.Processes["web"]
	if !ok {
		r.add("error", "processes.web", "wordpress verified TLS requires a web process")
	} else if web.Command != "" || web.Schedule != "" || web.Function != nil {
		r.add("error", "processes.web", "wordpress verified TLS requires the official image entrypoint without a command, schedule, or function")
	}
	if len(spec.Databases) != 1 {
		r.add("error", "databases", "wordpress verified TLS requires exactly one primary database runtime")
		return
	}
	runtime := spec.Databases[0].Runtime
	if spec.Databases[0].Name != "primary" || runtime == nil || runtime.Components == nil ||
		runtime.Components.Host != "WORDPRESS_DB_HOST" || runtime.Components.User != "WORDPRESS_DB_USER" ||
		runtime.Components.Password != "WORDPRESS_DB_PASSWORD" || runtime.Components.Name != "WORDPRESS_DB_NAME" {
		r.add("error", "databases[0].runtime.components", "wordpress verified TLS requires primary WORDPRESS_DB_* components")
	}
	if runtime == nil || runtime.TLS == nil || runtime.TLS.CAFileEnv != "MYSQL_SSL_CA" || runtime.TLS.ClientCertFileEnv != "" || runtime.TLS.ClientKeyFileEnv != "" {
		r.add("error", "databases[0].runtime.tls", "wordpress verified TLS requires only the MYSQL_SSL_CA file variable")
	}
	contentMounted := false
	for _, volume := range spec.Volumes {
		if volume.Mount == "/var/www/html/wp-content" && !volume.ReadOnly {
			contentMounted = true
		}
	}
	if !contentMounted {
		r.add("error", "volumes", "wordpress verified TLS requires a writable persistent wp-content volume")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
