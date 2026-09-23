package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"norn/v2/api/capture"
	"norn/v2/api/model"
	"norn/v2/api/saga"
)

func (p *Pipeline) migrate(ctx context.Context, st *state, sg *saga.Saga) error {
	if st.spec.Migrations == "" {
		return nil // skip
	}
	if err := p.checkSecretConflicts(st.spec); err != nil {
		return err
	}
	var bound *boundDatabase
	if st.spec.DeclaresDatabase() {
		set, err := p.databaseForState(ctx, st)
		if err != nil {
			return err
		}
		bound = set.forMigration(st.spec)
		if st.spec.NamedDatabases() && bound == nil {
			return &DatabaseTargetError{Reason: "the migration database was not bound at acceptance"}
		}
		if st.spec.NamedDatabases() && st.deploymentID != "" {
			if err := p.guardDeployTargets(ctx, st); err != nil {
				return err
			}
		}
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", st.spec.Migrations)
	if st.workDir != "" {
		cmd.Dir = st.workDir
	}
	if bound != nil {
		if err := bound.requireCapabilities(dbMigration); err != nil {
			return err
		}
		// The migration sees only the recorded target, as a connection URL
		// value and/or a private URL file under the app's declared names
		// (plus the private libpq service), never the API environment or
		// the control DSN.
		valueEnv, fileEnv := migrationEnvNames(st.spec)
		cmd.Env = bound.session.MigrationEnvironment(valueEnv, fileEnv)
		if out, err := runCaptured(cmd); err != nil {
			return fmt.Errorf("migration failed: %s", bound.session.RedactCaptured(out))
		}
		return nil
	}
	// Legacy (no database profile): the command no longer inherits the API
	// process's environment, which is control-plane environment (control
	// DSN, PG* routing and password, API/Nomad/Consul tokens, SOPS key,
	// cloud keys). See legacyMigrationEnvironment.
	declared := ""
	if st.spec.Infrastructure != nil && st.spec.Infrastructure.Postgres != nil {
		declared = st.spec.Infrastructure.Postgres.Database
	}
	cmd.Env = legacyMigrationEnvironment(os.Environ(), declared)
	if out, err := runCaptured(cmd); err != nil {
		return fmt.Errorf("migration failed: %s", out.String())
	}
	return nil
}

// Command output reported in errors is bounded while the command runs, so a
// chatty migration or pg_dump cannot grow API memory without limit.
const commandOutputHead, commandOutputTail = 8 << 10, 24 << 10

func runCaptured(cmd *exec.Cmd) (*capture.Buffer, error) {
	output := capture.New(commandOutputHead, commandOutputTail)
	cmd.Stdout, cmd.Stderr = output, output
	return output, cmd.Run()
}

// legacyMigrationEnvironment is the allowlisted environment for legacy
// migrations: process basics only, plus PGDATABASE naming the database the
// app declares (the v1 name-based selection). Everything the API process
// carries is control-plane environment and is dropped: all PG* routing and
// credentials (PGHOST, PGUSER, PGPASSWORD, PGSERVICE, ...), DATABASE_URL,
// NORN_*, NOMAD_*, CONSUL_*, VAULT_*, SOPS_*, AWS_* and the like. This is an
// intentional compatibility change: a legacy migration that relied on
// inherited host, user, password or URL must carry its own connection
// configuration or move to a named binding (which delivers one).
func legacyMigrationEnvironment(inherited []string, declaredDatabase string) []string {
	basics := map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "LANG": true, "TMPDIR": true, "TZ": true, "TERM": true}
	out := make([]string, 0, len(basics)+1)
	for _, entry := range inherited {
		name, _, found := strings.Cut(entry, "=")
		if found && (basics[name] || strings.HasPrefix(name, "LC_")) {
			out = append(out, entry)
		}
	}
	if declaredDatabase != "" {
		out = append(out, "PGDATABASE="+declaredDatabase)
	}
	return out
}

// migrationEnvNames are the variables a bound migration receives: the
// migration database's declared runtime names, or DATABASE_URL (a real
// connection URL) for legacy-mapped apps and named databases without a
// runtime block.
func migrationEnvNames(spec *model.InfraSpec) (valueEnv, fileEnv string) {
	if spec.NamedDatabases() {
		requirement, _ := spec.DatabaseByName(spec.EffectiveMigrationDatabase())
		if requirement.Runtime != nil {
			return requirement.Runtime.Env, requirement.Runtime.FileEnv
		}
	}
	return "DATABASE_URL", ""
}
