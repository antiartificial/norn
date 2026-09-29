package startup

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	SchemaModeEnv    = "NORN_SCHEMA_MODE"
	StartupModeEnv   = "NORN_STARTUP_MODE"
	SchemaTimeoutEnv = "NORN_SCHEMA_TIMEOUT"

	ContractProbeArgument = "--norn-startup-contract"
	ContractName          = "norn.startup/v2"
)

type SchemaMode string

const (
	SchemaModeAuto        SchemaMode = "auto"
	SchemaModeCheck       SchemaMode = "check"
	SchemaModeMigrateOnly SchemaMode = "migrate-only"
)

type Mode string

const (
	ModeActive  Mode = "active"
	ModePassive Mode = "passive"
)

type Config struct {
	SchemaMode    SchemaMode
	StartupMode   Mode
	SchemaTimeout time.Duration
}

// Parse reads the shared startup contract used by the API and host agent.
// Invalid values are rejected before either process connects to PostgreSQL or
// initializes runtime clients.
func Parse(getenv func(string) string) (Config, error) {
	schemaMode := SchemaMode(strings.ToLower(strings.TrimSpace(getenv(SchemaModeEnv))))
	if schemaMode == "" {
		schemaMode = SchemaModeAuto
	}
	switch schemaMode {
	case SchemaModeAuto, SchemaModeCheck, SchemaModeMigrateOnly:
	default:
		return Config{}, fmt.Errorf("%s must be auto, check, or migrate-only (got %q)", SchemaModeEnv, schemaMode)
	}

	startupMode := Mode(strings.ToLower(strings.TrimSpace(getenv(StartupModeEnv))))
	if startupMode == "" {
		startupMode = ModeActive
	}
	switch startupMode {
	case ModeActive, ModePassive:
	default:
		return Config{}, fmt.Errorf("%s must be active or passive (got %q)", StartupModeEnv, startupMode)
	}
	if startupMode == ModePassive && schemaMode != SchemaModeCheck {
		return Config{}, fmt.Errorf("%s=passive requires %s=check", StartupModeEnv, SchemaModeEnv)
	}

	schemaTimeout := 5 * time.Minute
	if raw := strings.TrimSpace(getenv(SchemaTimeoutEnv)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration (got %q)", SchemaTimeoutEnv, raw)
		}
		schemaTimeout = parsed
	}

	return Config{SchemaMode: schemaMode, StartupMode: startupMode, SchemaTimeout: schemaTimeout}, nil
}

type Contract struct {
	Name           string         `json:"name"`
	SchemaModes    []string       `json:"schemaModes"`
	StartupModes   []string       `json:"startupModes"`
	PassiveRoutes  []string       `json:"passiveRoutes"`
	SchemaContract SchemaContract `json:"schemaContract"`
}

func CurrentContract() Contract {
	return Contract{
		Name:           ContractName,
		SchemaModes:    []string{string(SchemaModeAuto), string(SchemaModeCheck), string(SchemaModeMigrateOnly)},
		StartupModes:   []string{string(ModeActive), string(ModePassive)},
		PassiveRoutes:  []string{"/api/health", "/api/version", "/api/schema"},
		SchemaContract: CurrentSchemaContract(),
	}
}

// WriteContractProbe handles the side-effect-free binary capability probe. It
// returns false for an ordinary invocation. The exact argument shape is
// deliberate: a release script must not mistake an unrelated argument for
// support of this startup contract.
func WriteContractProbe(args []string, w io.Writer) (bool, error) {
	if len(args) != 1 || args[0] != ContractProbeArgument {
		return false, nil
	}
	if err := json.NewEncoder(w).Encode(CurrentContract()); err != nil {
		return true, fmt.Errorf("write startup contract: %w", err)
	}
	return true, nil
}
