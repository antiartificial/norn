package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// #nosec G101 -- this is a compatibility expiry timestamp, not a credential.
const defaultLegacyTokenSigningUntil = "2026-08-15T00:00:00Z"

type Config struct {
	Profile string
	// Environment identifies this control plane's release lane. It is
	// intentionally independent from Profile: a small staging fleet may still
	// exercise production-grade admission, while Profile controls local
	// hardening and substrate checks.
	Environment string
	Port        string
	BindAddr    string
	DatabaseURL string
	UIDir       string
	AppsDir     string
	FleetConfig string // checked-out norn-fleet Cluster document; read-only to Norn
	// FleetGitHubApp configures an installation-scoped GitHub App used only to
	// open reviewed fleet pull requests and dispatch the protected apply
	// workflow. Provider and Terraform state credentials remain in GitHub.
	FleetGitHubAppID          string
	FleetGitHubInstallationID int64
	FleetGitHubPrivateKeyFile string
	FleetGitHubRepository     string
	FleetGitHubDefaultBranch  string
	FleetGitHubConfigPath     string
	FleetGitHubPlanWorkflow   string
	FleetGitHubApplyWorkflow  string
	FleetGitHubAPIBaseURL     string
	GitToken                  string
	GitSSHKey                 string
	APIToken                  string
	// RequireExplicitAuth disables compatibility access based only on a direct
	// loopback peer or a temporary IP grant.
	RequireExplicitAuth      bool
	StrictSecrets            bool
	AuditSigningKey          string
	AuditPreviousSigningKeys []string
	AuditRetentionDays       int
	// ControlAuthority optionally pins this process to the durable authority
	// UUID stored in the control database. It never creates or substitutes for
	// that database identity; a configured mismatch fails operation acceptance.
	ControlAuthority string
	// QualificationSigningKey signs portable staging release receipts. Keep it
	// distinct from mutation-audit integrity material.
	QualificationSigningKey string
	// TrustedQualificationSigningKeys contains the staging receipt keys accepted
	// by a production control plane. It must be distinct from the local audit
	// signing key in normal deployments.
	TrustedQualificationSigningKeys []string
	// GitHubActionsOIDC configures short lived, workload-bound exchange tokens
	// for the protected release and fleet workflows. Every allowlist entry is
	// explicit; the API never infers trust from a repository name alone.
	GitHubActionsOIDCAudience        string
	GitHubActionsOIDCJWKSURL         string
	GitHubActionsAllowedRepositories []string
	GitHubActionsAllowedWorkflowRefs []string
	GitHubActionsAllowedRefs         []string
	GitHubActionsAllowedEvents       []string
	GitHubActionsAllowedApps         []string
	GitHubActionsAllowedEnvironments []string
	// GitHubActionsDefaultBranch is the protected caller branch name used by
	// staging, qualification, and rollback lanes (for example "main"). It is
	// deliberately a branch name, not a ref, so the API owns the refs/heads/
	// prefix used when comparing verified GitHub claims.
	GitHubActionsDefaultBranch            string
	GitHubActionsTokenTTL                 time.Duration
	AllowDevelopmentGitHubActionsOIDC     bool
	GitHubActionsFleetAllowedRepository   string
	GitHubActionsFleetAllowedWorkflowRefs []string
	GitHubActionsFleetAllowedEnvironments []string
	GitHubActionsFleetAllowedIntents      []string
	// ReleaseAdmissionMode is "keyless" for production. "keyed" exists only as
	// deliberate development compatibility while environments migrate.
	ReleaseAdmissionMode     string
	ReleaseAttestationIssuer string
	// ReleaseAttestationTrustMode selects the trust root for keyless release
	// attestations. github-public is the existing transparent-log path;
	// github-private requires the independent read-only GitHub App below.
	ReleaseAttestationTrustMode    string
	ReleaseAttestationRepositories []string
	ReleaseAttestationWorkflowRefs []string
	ReleaseRequireSBOM             bool
	// ReleaseAttestationGitHubApp is intentionally separate from the Fleet
	// bridge. It can mint only repository-scoped Attestations:read tokens.
	ReleaseAttestationGitHubAppID          string
	ReleaseAttestationGitHubInstallationID int64
	ReleaseAttestationGitHubPrivateKeyFile string
	ReleaseAttestationGitHubAPIBaseURL     string
	ReleaseAttestationGHPath               string
	LegacyTokenSigningUntil                time.Time
	RegistryURL                            string // GHCR registry (e.g. ghcr.io/username)
	ArtifactSigningPublicKey               string
	ArtifactDenySeverities                 []string
	CosignPath                             string
	TrivyPath                              string
	NetworkMode                            string // local, tailnet, public

	NomadAddr  string // Nomad API address
	ConsulAddr string // Consul API address
	IngressURL string // Regional Traefik origin used by Cloudflared
	// ExternalIngress disables local cloudflared mutation when DNS/global edge
	// routing is owned outside this Norn process.
	ExternalIngress bool
	// These mirror the HashiCorp client environment switches so production
	// startup can reject encrypted-but-unverified control-plane transport.
	NomadTLSSkipVerify  bool
	ConsulTLSSkipVerify bool

	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Region    string
	S3UseSSL    bool
	S3Provider  string
	S3ForcePath bool

	GarageAdminEndpoint string
	GarageAdminToken    string

	RedpandaBrokers []string
	RedpandaRPKPath string

	BeaconEnvironment string
	BeaconSinkURL     string
	BeaconSinkKeyID   string
	BeaconSinkSecret  string

	AllowedOrigins     string
	CFAccessTeamDomain string
	CFAccessAUD        string

	WebhookSecret          string // NORN_WEBHOOK_SECRET
	CloudflaredConfig      string // NORN_CLOUDFLARED_CONFIG
	CloudflareAPIToken     string // NORN_CLOUDFLARE_API_TOKEN
	CloudflareZoneID       string // NORN_CLOUDFLARE_ZONE_ID
	CloudflareLogpushToken string // NORN_CLOUDFLARE_LOGPUSH_TOKEN
	CloudflareAPIBaseURL   string // NORN_CLOUDFLARE_API_BASE_URL

	// BuildTestExecution selects how build.test commands run: "legacy-unfenced"
	// (default, v2 behaviour: direct exec, no effect fencing) or "supervised"
	// (fenced effect executor with a cgroup-v2 runner). Supervised mode has no
	// fallback; incomplete configuration fails startup.
	BuildTestExecution  string        // NORN_BUILD_TEST_EXECUTION
	BuildTestTimeout    time.Duration // NORN_BUILD_TEST_TIMEOUT
	BuildTestPath       string        // NORN_BUILD_TEST_PATH (command PATH; nothing else is inherited)
	EffectSupervisorDir string        // NORN_EFFECT_SUPERVISOR_ROOT
	EffectSigningKey    string        // NORN_EFFECT_SIGNING_KEY
	EffectRunnerBinary  string        // NORN_EFFECT_RUNNER_BINARY (default: norn-effect-runner beside the API binary)
	EffectRunnerSHA256  string        // NORN_EFFECT_RUNNER_SHA256 (optional pin)
	EffectCgroupRoot    string        // NORN_EFFECT_CGROUP_ROOT (delegated cgroup-v2 directory)

	// DatabaseProfile selects a deployment profile in the durable database
	// catalog. It is independent of Profile (security hardening). Unset keeps
	// v2 database routing (ambient libpq by database name).
	DatabaseProfile   string // NORN_DATABASE_PROFILE
	DatabaseSecretDir string // NORN_DATABASE_SECRET_DIR (owner-only; resolves secret: references)

	// Evidence archive (ADR 0001). Unset directory disables archiving; the
	// default mode is shadow (archive and verify, never delete).
	EvidenceArchiveDir      string // NORN_EVIDENCE_ARCHIVE_DIR (absolute, owner-only)
	EvidenceArchiveMode     string // NORN_EVIDENCE_ARCHIVE_MODE: shadow | prune
	EvidenceArchiveMaxBytes int64  // NORN_EVIDENCE_ARCHIVE_MAX_BYTES (hard capacity)
	EvidenceMinAge          string // NORN_EVIDENCE_MIN_AGE (retention floor before pruning, e.g. 720h)
	// EvidenceArchiveBackend selects the archive explicitly: "local" (the
	// consolidated local/Mini profile: NORN_EVIDENCE_ARCHIVE_DIR) or
	// "object" (the Fleet profile: an S3-compatible bucket with credentials
	// dedicated to the archive, read from owner-only files).
	EvidenceArchiveBackend       string // NORN_EVIDENCE_ARCHIVE_BACKEND
	EvidenceArchiveEndpoint      string // NORN_EVIDENCE_ARCHIVE_S3_ENDPOINT (host[:port])
	EvidenceArchiveBucket        string // NORN_EVIDENCE_ARCHIVE_S3_BUCKET
	EvidenceArchivePrefix        string // NORN_EVIDENCE_ARCHIVE_S3_PREFIX
	EvidenceArchiveRegion        string // NORN_EVIDENCE_ARCHIVE_S3_REGION
	EvidenceArchiveAccessKeyFile string // NORN_EVIDENCE_ARCHIVE_S3_ACCESS_KEY_FILE
	EvidenceArchiveSecretKeyFile string // NORN_EVIDENCE_ARCHIVE_S3_SECRET_KEY_FILE
	EvidenceArchiveCAFile        string // NORN_EVIDENCE_ARCHIVE_S3_CA_FILE (optional PEM bundle)
	// Evidence reserve admission: with an archive configured the durable
	// policy is enforced unless NORN_EVIDENCE_RESERVE=disabled is set
	// explicitly (removing archive configuration never disables it).
	EvidenceReserve              string        // NORN_EVIDENCE_RESERVE: enforce | disabled
	EvidenceReserveMaxPending    int           // NORN_EVIDENCE_RESERVE_MAX_PENDING
	EvidenceReserveMaxPendingAge time.Duration // NORN_EVIDENCE_RESERVE_MAX_PENDING_AGE
	EvidenceReserveSignedBytes   int64         // NORN_EVIDENCE_RESERVE_MAX_SIGNED_ACCEPTANCE_BYTES (0 disables this narrow gate)
	EvidenceReserveMinFreeBytes  int64         // NORN_EVIDENCE_RESERVE_MIN_FREE_BYTES (local archive headroom)
	// Diagnostic log collection (separate from evidence): unset directory
	// disables it. Limits bound the spool; the oldest output is dropped and
	// counted first.
	LogSpoolDir            string        // NORN_LOG_SPOOL_DIR (absolute, owner-only)
	LogSpoolMaxBytes       int64         // NORN_LOG_SPOOL_MAX_BYTES
	LogSpoolStreamMaxBytes int64         // NORN_LOG_SPOOL_STREAM_MAX_BYTES
	LogSpoolSegmentBytes   int64         // NORN_LOG_SPOOL_SEGMENT_BYTES
	LogCollectInterval     time.Duration // NORN_LOG_COLLECT_INTERVAL
}

func Load() *Config {
	return &Config{
		Profile:                                strings.ToLower(envOr("NORN_PROFILE", "development")),
		Environment:                            strings.ToLower(envOr("NORN_ENVIRONMENT", "development")),
		Port:                                   envOr("NORN_PORT", "8800"),
		BindAddr:                               envOr("NORN_BIND_ADDR", "127.0.0.1"),
		DatabaseURL:                            envOr("NORN_DATABASE_URL", "postgres://norn:norn@localhost:5432/norn_v2?sslmode=disable"),
		UIDir:                                  uiDir(),
		AppsDir:                                envOr("NORN_APPS_DIR", os.Getenv("HOME")+"/projects"),
		FleetConfig:                            os.Getenv("NORN_FLEET_CONFIG"),
		FleetGitHubAppID:                       strings.TrimSpace(os.Getenv("NORN_FLEET_GITHUB_APP_ID")),
		FleetGitHubInstallationID:              envInt64Or("NORN_FLEET_GITHUB_INSTALLATION_ID", 0),
		FleetGitHubPrivateKeyFile:              strings.TrimSpace(os.Getenv("NORN_FLEET_GITHUB_PRIVATE_KEY_FILE")),
		FleetGitHubRepository:                  strings.TrimSpace(os.Getenv("NORN_FLEET_GITHUB_REPOSITORY")),
		FleetGitHubDefaultBranch:               envOr("NORN_FLEET_GITHUB_DEFAULT_BRANCH", "main"),
		FleetGitHubConfigPath:                  strings.TrimSpace(os.Getenv("NORN_FLEET_GITHUB_CONFIG_PATH")),
		FleetGitHubPlanWorkflow:                envOr("NORN_FLEET_GITHUB_PLAN_WORKFLOW", "plan.yml"),
		FleetGitHubApplyWorkflow:               envOr("NORN_FLEET_GITHUB_APPLY_WORKFLOW", "apply.yml"),
		FleetGitHubAPIBaseURL:                  envOr("NORN_FLEET_GITHUB_API_BASE_URL", "https://api.github.com"),
		GitToken:                               os.Getenv("NORN_GIT_TOKEN"),
		GitSSHKey:                              os.Getenv("NORN_GIT_SSH_KEY"),
		APIToken:                               os.Getenv("NORN_API_TOKEN"),
		RequireExplicitAuth:                    os.Getenv("NORN_REQUIRE_EXPLICIT_AUTH") == "true",
		StrictSecrets:                          os.Getenv("NORN_STRICT_SECRETS") == "true" || strings.EqualFold(os.Getenv("NORN_PROFILE"), "production"),
		AuditSigningKey:                        os.Getenv("NORN_AUDIT_SIGNING_KEY"),
		AuditPreviousSigningKeys:               splitNonEmpty(os.Getenv("NORN_AUDIT_PREVIOUS_SIGNING_KEYS")),
		ControlAuthority:                       strings.TrimSpace(os.Getenv("NORN_CONTROL_AUTHORITY")),
		AuditRetentionDays:                     envIntOr("NORN_AUDIT_RETENTION_DAYS", 365),
		QualificationSigningKey:                os.Getenv("NORN_QUALIFICATION_SIGNING_KEY"),
		TrustedQualificationSigningKeys:        splitNonEmpty(os.Getenv("NORN_TRUSTED_QUALIFICATION_SIGNING_KEYS")),
		GitHubActionsOIDCAudience:              strings.TrimSpace(os.Getenv("NORN_GITHUB_ACTIONS_OIDC_AUDIENCE")),
		GitHubActionsOIDCJWKSURL:               envOr("NORN_GITHUB_ACTIONS_OIDC_JWKS_URL", "https://token.actions.githubusercontent.com/.well-known/jwks"),
		GitHubActionsAllowedRepositories:       splitNonEmpty(os.Getenv("NORN_GITHUB_ACTIONS_ALLOWED_REPOSITORIES")),
		GitHubActionsAllowedWorkflowRefs:       splitNonEmpty(os.Getenv("NORN_GITHUB_ACTIONS_ALLOWED_WORKFLOW_REFS")),
		GitHubActionsAllowedRefs:               splitNonEmpty(os.Getenv("NORN_GITHUB_ACTIONS_ALLOWED_REFS")),
		GitHubActionsAllowedEvents:             splitNonEmpty(os.Getenv("NORN_GITHUB_ACTIONS_ALLOWED_EVENTS")),
		GitHubActionsAllowedApps:               splitNonEmpty(os.Getenv("NORN_GITHUB_ACTIONS_ALLOWED_APPS")),
		GitHubActionsAllowedEnvironments:       splitNonEmpty(os.Getenv("NORN_GITHUB_ACTIONS_ALLOWED_ENVIRONMENTS")),
		GitHubActionsDefaultBranch:             strings.TrimSpace(os.Getenv("NORN_GITHUB_ACTIONS_DEFAULT_BRANCH")),
		GitHubActionsTokenTTL:                  envDurationOr("NORN_GITHUB_ACTIONS_TOKEN_TTL", 15*time.Minute),
		AllowDevelopmentGitHubActionsOIDC:      envBoolOr("NORN_ALLOW_DEVELOPMENT_GITHUB_ACTIONS_OIDC", false),
		GitHubActionsFleetAllowedRepository:    strings.TrimSpace(os.Getenv("NORN_GITHUB_ACTIONS_FLEET_ALLOWED_REPOSITORY")),
		GitHubActionsFleetAllowedWorkflowRefs:  splitNonEmpty(os.Getenv("NORN_GITHUB_ACTIONS_FLEET_ALLOWED_WORKFLOW_REFS")),
		GitHubActionsFleetAllowedEnvironments:  splitNonEmpty(os.Getenv("NORN_GITHUB_ACTIONS_FLEET_ALLOWED_ENVIRONMENTS")),
		GitHubActionsFleetAllowedIntents:       splitNonEmpty(os.Getenv("NORN_GITHUB_ACTIONS_FLEET_ALLOWED_INTENTS")),
		ReleaseAdmissionMode:                   strings.ToLower(envOr("NORN_RELEASE_ADMISSION_MODE", "keyed")),
		ReleaseAttestationIssuer:               strings.TrimSpace(os.Getenv("NORN_RELEASE_ATTESTATION_ISSUER")),
		ReleaseAttestationTrustMode:            strings.ToLower(envOr("NORN_RELEASE_ATTESTATION_TRUST_MODE", "github-public")),
		ReleaseAttestationRepositories:         splitNonEmpty(os.Getenv("NORN_RELEASE_ATTESTATION_ALLOWED_REPOSITORIES")),
		ReleaseAttestationWorkflowRefs:         splitNonEmpty(os.Getenv("NORN_RELEASE_ATTESTATION_ALLOWED_WORKFLOW_REFS")),
		ReleaseRequireSBOM:                     envBoolOr("NORN_RELEASE_REQUIRE_SBOM", false),
		ReleaseAttestationGitHubAppID:          strings.TrimSpace(os.Getenv("NORN_RELEASE_ATTESTATION_GITHUB_APP_ID")),
		ReleaseAttestationGitHubInstallationID: envInt64Or("NORN_RELEASE_ATTESTATION_GITHUB_INSTALLATION_ID", 0),
		ReleaseAttestationGitHubPrivateKeyFile: strings.TrimSpace(os.Getenv("NORN_RELEASE_ATTESTATION_GITHUB_PRIVATE_KEY_FILE")),
		ReleaseAttestationGitHubAPIBaseURL:     envOr("NORN_RELEASE_ATTESTATION_GITHUB_API_BASE_URL", "https://api.github.com"),
		ReleaseAttestationGHPath:               strings.TrimSpace(os.Getenv("NORN_RELEASE_ATTESTATION_GH_PATH")),
		LegacyTokenSigningUntil:                envTimeOr("NORN_LEGACY_TOKEN_SIGNING_UNTIL", defaultLegacyTokenSigningUntil),
		RegistryURL:                            os.Getenv("NORN_REGISTRY_URL"),
		ArtifactSigningPublicKey:               os.Getenv("NORN_ARTIFACT_SIGNING_PUBLIC_KEY"),
		ArtifactDenySeverities:                 splitCSV(envOr("NORN_ARTIFACT_DENY_SEVERITIES", "HIGH,CRITICAL")),
		CosignPath:                             envOr("NORN_COSIGN_PATH", "cosign"),
		TrivyPath:                              envOr("NORN_TRIVY_PATH", "trivy"),
		NetworkMode:                            networkMode(envOr("NORN_NETWORK_MODE", "local")),

		NomadAddr:           envOr("NORN_NOMAD_ADDR", "http://localhost:4646"),
		ConsulAddr:          envOr("NORN_CONSUL_ADDR", "http://localhost:8500"),
		IngressURL:          strings.TrimRight(os.Getenv("NORN_INGRESS_URL"), "/"),
		ExternalIngress:     envBoolOr("NORN_EXTERNAL_INGRESS", false),
		NomadTLSSkipVerify:  envBoolOr("NOMAD_SKIP_VERIFY", false),
		ConsulTLSSkipVerify: !envBoolOr("CONSUL_HTTP_SSL_VERIFY", true),

		S3Endpoint:          os.Getenv("NORN_S3_ENDPOINT"),
		S3AccessKey:         os.Getenv("NORN_S3_ACCESS_KEY"),
		S3SecretKey:         os.Getenv("NORN_S3_SECRET_KEY"),
		S3Region:            envOr("NORN_S3_REGION", "auto"),
		S3UseSSL:            os.Getenv("NORN_S3_USE_SSL") != "false",
		S3Provider:          envOr("NORN_S3_PROVIDER", "s3"),
		S3ForcePath:         os.Getenv("NORN_S3_FORCE_PATH_STYLE") == "true" || envOr("NORN_S3_PROVIDER", "s3") == "garage",
		GarageAdminEndpoint: os.Getenv("NORN_GARAGE_ADMIN_ENDPOINT"),
		GarageAdminToken:    os.Getenv("NORN_GARAGE_ADMIN_TOKEN"),

		RedpandaBrokers: splitCSV(os.Getenv("NORN_REDPANDA_BROKERS")),
		RedpandaRPKPath: envOr("NORN_RPK_PATH", "rpk"),

		BeaconEnvironment: envOr("NORN_BEACON_ENVIRONMENT", "mini"),
		BeaconSinkURL:     os.Getenv("NORN_BEACON_SINK_URL"),
		BeaconSinkKeyID:   os.Getenv("NORN_BEACON_SINK_KEY_ID"),
		BeaconSinkSecret:  os.Getenv("NORN_BEACON_SINK_SECRET"),

		AllowedOrigins:     os.Getenv("NORN_ALLOWED_ORIGINS"),
		CFAccessTeamDomain: os.Getenv("NORN_CF_ACCESS_TEAM_DOMAIN"),
		CFAccessAUD:        os.Getenv("NORN_CF_ACCESS_AUD"),

		WebhookSecret:          os.Getenv("NORN_WEBHOOK_SECRET"),
		CloudflaredConfig:      envOr("NORN_CLOUDFLARED_CONFIG", os.Getenv("HOME")+"/.cloudflared/config.yml"),
		CloudflareAPIToken:     firstEnv("NORN_CLOUDFLARE_API_TOKEN", "CLOUDFLARE_API_TOKEN"),
		CloudflareZoneID:       firstEnv("NORN_CLOUDFLARE_ZONE_ID", "CLOUDFLARE_ZONE_ID"),
		CloudflareLogpushToken: os.Getenv("NORN_CLOUDFLARE_LOGPUSH_TOKEN"),
		CloudflareAPIBaseURL:   envOr("NORN_CLOUDFLARE_API_BASE_URL", "https://api.cloudflare.com/client/v4"),

		BuildTestExecution:           strings.ToLower(strings.TrimSpace(envOr("NORN_BUILD_TEST_EXECUTION", "legacy-unfenced"))),
		BuildTestTimeout:             envDurationOr("NORN_BUILD_TEST_TIMEOUT", 30*time.Minute),
		BuildTestPath:                envOr("NORN_BUILD_TEST_PATH", "/usr/local/bin:/usr/bin:/bin"),
		EffectSupervisorDir:          strings.TrimSpace(os.Getenv("NORN_EFFECT_SUPERVISOR_ROOT")),
		EffectSigningKey:             os.Getenv("NORN_EFFECT_SIGNING_KEY"),
		EffectRunnerBinary:           strings.TrimSpace(os.Getenv("NORN_EFFECT_RUNNER_BINARY")),
		EffectRunnerSHA256:           strings.ToLower(strings.TrimSpace(os.Getenv("NORN_EFFECT_RUNNER_SHA256"))),
		EffectCgroupRoot:             strings.TrimSpace(os.Getenv("NORN_EFFECT_CGROUP_ROOT")),
		DatabaseProfile:              strings.TrimSpace(os.Getenv("NORN_DATABASE_PROFILE")),
		DatabaseSecretDir:            strings.TrimSpace(os.Getenv("NORN_DATABASE_SECRET_DIR")),
		EvidenceArchiveDir:           strings.TrimSpace(os.Getenv("NORN_EVIDENCE_ARCHIVE_DIR")),
		EvidenceArchiveMode:          strings.ToLower(envOr("NORN_EVIDENCE_ARCHIVE_MODE", "shadow")),
		EvidenceMinAge:               envOr("NORN_EVIDENCE_MIN_AGE", "720h"),
		EvidenceArchiveBackend:       strings.ToLower(strings.TrimSpace(os.Getenv("NORN_EVIDENCE_ARCHIVE_BACKEND"))),
		EvidenceArchiveEndpoint:      strings.TrimSpace(os.Getenv("NORN_EVIDENCE_ARCHIVE_S3_ENDPOINT")),
		EvidenceArchiveBucket:        strings.TrimSpace(os.Getenv("NORN_EVIDENCE_ARCHIVE_S3_BUCKET")),
		EvidenceArchivePrefix:        strings.TrimSpace(os.Getenv("NORN_EVIDENCE_ARCHIVE_S3_PREFIX")),
		EvidenceArchiveRegion:        strings.TrimSpace(os.Getenv("NORN_EVIDENCE_ARCHIVE_S3_REGION")),
		EvidenceArchiveAccessKeyFile: strings.TrimSpace(os.Getenv("NORN_EVIDENCE_ARCHIVE_S3_ACCESS_KEY_FILE")),
		EvidenceArchiveSecretKeyFile: strings.TrimSpace(os.Getenv("NORN_EVIDENCE_ARCHIVE_S3_SECRET_KEY_FILE")),
		EvidenceArchiveCAFile:        strings.TrimSpace(os.Getenv("NORN_EVIDENCE_ARCHIVE_S3_CA_FILE")),
		EvidenceReserve:              strings.ToLower(envOr("NORN_EVIDENCE_RESERVE", "enforce")),
		EvidenceReserveMaxPending:    envIntOr("NORN_EVIDENCE_RESERVE_MAX_PENDING", 10000),
		EvidenceReserveMaxPendingAge: envDurationOr("NORN_EVIDENCE_RESERVE_MAX_PENDING_AGE", 24*time.Hour),
		EvidenceReserveSignedBytes:   envInt64Or("NORN_EVIDENCE_RESERVE_MAX_SIGNED_ACCEPTANCE_BYTES", 0),
		EvidenceReserveMinFreeBytes:  envInt64Or("NORN_EVIDENCE_RESERVE_MIN_FREE_BYTES", 1<<30),
		LogSpoolDir:                  strings.TrimSpace(os.Getenv("NORN_LOG_SPOOL_DIR")),
		LogSpoolMaxBytes:             envInt64Or("NORN_LOG_SPOOL_MAX_BYTES", 1<<30),
		LogSpoolStreamMaxBytes:       envInt64Or("NORN_LOG_SPOOL_STREAM_MAX_BYTES", 64<<20),
		LogSpoolSegmentBytes:         envInt64Or("NORN_LOG_SPOOL_SEGMENT_BYTES", 4<<20),
		LogCollectInterval:           envDurationOr("NORN_LOG_COLLECT_INTERVAL", 30*time.Second),
		EvidenceArchiveMaxBytes: func() int64 {
			value, err := strconv.ParseInt(envOr("NORN_EVIDENCE_ARCHIVE_MAX_BYTES", "10737418240"), 10, 64)
			if err != nil {
				return 0
			}
			return value
		}(),
	}
}

func (c *Config) Production() bool {
	return c != nil && strings.EqualFold(strings.TrimSpace(c.Profile), "production")
}

func (c *Config) EnvironmentID() string {
	if c == nil || strings.TrimSpace(c.Environment) == "" {
		return "development"
	}
	return strings.ToLower(strings.TrimSpace(c.Environment))
}

func (c *Config) ProfileID() string {
	if c == nil || strings.TrimSpace(c.Profile) == "" {
		return "development"
	}
	return strings.ToLower(strings.TrimSpace(c.Profile))
}

func uiDir() string {
	if explicit := strings.TrimSpace(os.Getenv("NORN_UI_DIR")); explicit != "" {
		if validUIDir(explicit) {
			return explicit
		}
	}
	return defaultUIDir()
}

func defaultUIDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	candidate := home + "/norn/current/ui"
	if validUIDir(candidate) {
		return candidate
	}
	return ""
}

func validUIDir(dir string) bool {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() {
		return false
	}
	index, err := root.Stat("index.html")
	return err == nil && !index.IsDir()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt64Or(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func envTimeOr(key, fallback string) time.Time {
	raw := envOr(key, fallback)
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func envBoolOr(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

func envIntOr(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func splitNonEmpty(raw string) []string {
	values := []string{}
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func networkMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "tailnet", "tailscale":
		return "tailnet"
	case "public":
		return "public"
	default:
		return "local"
	}
}
