package handler

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"norn/v2/api/model"
)

const productionReadinessSchema = "norn.production-readiness/v1"

type ProductionReadinessReport struct {
	Schema      string                     `json:"schema"`
	GeneratedAt string                     `json:"generatedAt"`
	Status      string                     `json:"status"`
	Summary     ProductionReadinessSummary `json:"summary"`
	Checks      []ProductionReadinessCheck `json:"checks"`
}

type ProductionReadinessSummary struct {
	Passed   int `json:"passed"`
	Warnings int `json:"warnings"`
	Failed   int `json:"failed"`
	Total    int `json:"total"`
}

type ProductionReadinessCheck struct {
	ID          string         `json:"id"`
	Category    string         `json:"category"`
	Status      string         `json:"status"`
	Title       string         `json:"title"`
	Detail      string         `json:"detail"`
	Remediation string         `json:"remediation,omitempty"`
	Evidence    map[string]any `json:"evidence,omitempty"`
}

type productionReadinessInput struct {
	ProductionProfile        bool
	ExplicitAuth             bool
	StrongControlToken       bool
	CloudflareAccess         bool
	LegacySigningRetired     bool
	ControlLoopback          bool
	NomadReachable           bool
	NomadACL                 bool
	NomadTLS                 bool
	NomadServers             int
	NomadClients             int
	ConsulReachable          bool
	ConsulACL                bool
	ConsulTLS                bool
	ConsulServers            int
	DatabaseTLS              bool
	DatabaseExternal         bool
	DatabasePITR             bool
	DatabaseReplicas         int
	SecretProblems           int
	SecretMigrationItems     int
	ConfiguredApps           []string
	UntrustedDeployments     []string
	StatefulApps             int
	MissingSnapshots         []string
	StaleSnapshots           []string
	MissingOffsiteBackups    []string
	ObservabilityReady       bool
	AuditDurable             bool
	AuditStalePending        int
	AuditInvalidRecent       int
	AuditAcknowledgedInvalid int
	AuditRetentionDays       int
	MissingRecoveryDrills    []string
	StaleRecoveryDrills      []string
	ProbeErrors              []string
}

func (h *Handler) ProductionReadiness(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	report := h.buildProductionReadiness(r.Context())
	preventSensitiveResponseCaching(w)
	writeJSON(w, report)
}

func (h *Handler) buildProductionReadiness(ctx context.Context) ProductionReadinessReport {
	input := productionReadinessInput{}
	if h.cfg != nil {
		input.ProductionProfile = h.cfg.Production()
		input.ExplicitAuth = h.cfg.RequireExplicitAuth
		input.StrongControlToken = len(h.cfg.APIToken) >= 32
		input.CloudflareAccess = strings.TrimSpace(h.cfg.CFAccessTeamDomain) != "" && strings.TrimSpace(h.cfg.CFAccessAUD) != ""
		input.LegacySigningRetired = !time.Now().UTC().Before(h.cfg.LegacyTokenSigningUntil)
		input.ControlLoopback = isLoopbackBind(h.cfg.BindAddr)
		input.NomadTLS = strings.EqualFold(urlScheme(h.cfg.NomadAddr), "https") && !h.cfg.NomadTLSSkipVerify
		input.ConsulTLS = strings.EqualFold(urlScheme(h.cfg.ConsulAddr), "https") && !h.cfg.ConsulTLSSkipVerify
		input.DatabaseTLS = databaseUsesTLS(h.cfg.DatabaseURL)
		input.DatabaseExternal = databaseHostIsExternal(h.cfg.DatabaseURL)
		input.AuditRetentionDays = h.cfg.AuditRetentionDays
	}

	if h.nomad == nil {
		input.ProbeErrors = append(input.ProbeErrors, "Nomad client is unavailable")
	} else {
		self, err := h.nomad.API().Agent().Self()
		if err != nil {
			input.ProbeErrors = append(input.ProbeErrors, "Nomad agent inspection failed")
		} else {
			input.NomadReachable = true
			input.NomadACL = nestedBool(self.Config, "ACL", "Enabled")
			input.NomadTLS = input.NomadTLS && nestedBool(self.Config, "TLSConfig", "EnableHTTP") && nestedBool(self.Config, "TLSConfig", "EnableRPC")
		}
		if members, err := h.nomad.API().Agent().Members(); err != nil {
			input.ProbeErrors = append(input.ProbeErrors, "Nomad server membership inspection failed")
		} else {
			for _, member := range members.Members {
				if member != nil && strings.EqualFold(member.Status, "alive") {
					input.NomadServers++
				}
			}
		}
		if nodes, _, err := h.nomad.API().Nodes().List(nil); err != nil {
			input.ProbeErrors = append(input.ProbeErrors, "Nomad client membership inspection failed")
		} else {
			for _, node := range nodes {
				if node != nil && strings.EqualFold(node.Status, "ready") {
					input.NomadClients++
				}
			}
		}
	}

	if h.consul == nil {
		input.ProbeErrors = append(input.ProbeErrors, "Consul client is unavailable")
	} else {
		self, err := h.consul.API().Agent().Self()
		if err != nil {
			input.ProbeErrors = append(input.ProbeErrors, "Consul agent inspection failed")
		} else {
			input.ConsulReachable = true
			input.ConsulACL = nestedBool(self["DebugConfig"], "ACLsEnabled")
			input.ConsulTLS = input.ConsulTLS &&
				nestedBool(self["DebugConfig"], "TLS", "HTTPS", "VerifyIncoming") &&
				nestedBool(self["DebugConfig"], "TLS", "InternalRPC", "VerifyOutgoing")
		}
		if members, err := h.consul.API().Agent().Members(false); err != nil {
			input.ProbeErrors = append(input.ProbeErrors, "Consul membership inspection failed")
		} else {
			for _, member := range members {
				if member != nil && member.Status == 1 && member.IsConsulServer() {
					input.ConsulServers++
				}
			}
		}
	}

	if h.cfg != nil {
		specs, err := model.DiscoverApps(h.cfg.AppsDir)
		if err != nil {
			input.ProbeErrors = append(input.ProbeErrors, "application discovery failed")
		} else {
			for _, spec := range specs {
				input.ConfiguredApps = append(input.ConfiguredApps, spec.App)
				status := h.secretStatus(spec)
				if !status.OK {
					input.SecretProblems++
				}
				input.SecretMigrationItems += len(h.secretMigrationItems(spec))
				if spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
					continue
				}
				input.StatefulApps++
				snapshots := listSnapshotsForSpec(spec)
				if len(snapshots) == 0 {
					input.MissingSnapshots = append(input.MissingSnapshots, spec.App)
				} else if createdAt, err := time.Parse(time.RFC3339, snapshots[0].CreatedAt); err != nil || time.Since(createdAt) > 7*24*time.Hour {
					input.StaleSnapshots = append(input.StaleSnapshots, spec.App)
				}
				if spec.Snapshots == nil || strings.TrimSpace(spec.Snapshots.ExportBucket) == "" {
					input.MissingOffsiteBackups = append(input.MissingOffsiteBackups, spec.App)
				}
			}
		}
	}

	if h.db == nil || h.db.Pool == nil {
		input.ProbeErrors = append(input.ProbeErrors, "deployment provenance inspection failed")
		input.UntrustedDeployments = append(input.UntrustedDeployments, input.ConfiguredApps...)
	} else {
		deployments, err := h.db.ListDeployments(ctx, "", 1000)
		if err != nil {
			input.ProbeErrors = append(input.ProbeErrors, "deployment provenance inspection failed")
		} else {
			seen := map[string]bool{}
			for _, deployment := range deployments {
				if seen[deployment.App] || deployment.Status != model.StatusDeployed {
					continue
				}
				seen[deployment.App] = true
				if deployment.SourceDirty || strings.HasSuffix(deployment.ImageTag, "-dirty") || !model.IsContentAddressedImage(deployment.ImageTag) ||
					(deployment.SourceKind != "git_clone" && deployment.SourceKind != "rollback") {
					input.UntrustedDeployments = append(input.UntrustedDeployments, deployment.App)
				}
			}
			for _, app := range input.ConfiguredApps {
				if !seen[app] {
					input.UntrustedDeployments = append(input.UntrustedDeployments, app)
				}
			}
		}
	}

	if h.db != nil && h.db.Pool != nil {
		recovery, err := h.db.InspectDatabaseRecovery(ctx)
		if err != nil {
			input.ProbeErrors = append(input.ProbeErrors, "PostgreSQL recovery configuration inspection failed")
		} else {
			input.DatabasePITR = recovery.PITREnabled()
			input.DatabaseReplicas = recovery.StreamingReplicas
		}
	}

	if h.db == nil || h.db.Pool == nil || h.cfg == nil || len(h.cfg.AuditSigningKey) < 32 {
		input.ProbeErrors = append(input.ProbeErrors, "durable mutation audit inspection failed")
	} else {
		stale, err := h.db.CountStaleMutationAudits(ctx, time.Now().UTC().Add(-5*time.Minute))
		if err != nil {
			input.ProbeErrors = append(input.ProbeErrors, "durable mutation audit inspection failed")
		} else {
			input.AuditStalePending = stale
			events, listErr := h.db.ListMutationAudits(ctx, 500)
			if listErr != nil {
				input.ProbeErrors = append(input.ProbeErrors, "mutation audit integrity inspection failed")
			} else {
				keys := append([]string{h.cfg.AuditSigningKey}, h.cfg.AuditPreviousSigningKeys...)
				eventIDs := make([]string, 0, len(events))
				for _, event := range events {
					eventIDs = append(eventIDs, event.ID)
				}
				incidents, incidentErr := h.db.ListMutationAuditIncidents(ctx, eventIDs)
				if incidentErr != nil {
					input.ProbeErrors = append(input.ProbeErrors, "mutation audit incident inspection failed")
				} else {
					for _, event := range events {
						if event.RecordDigest == "" || mutationAuditIntegrityWithKeys(keys, event) != "invalid" {
							continue
						}
						if incident, ok := incidents[event.ID]; ok && mutationAuditIncidentIntegrityWithKeys(keys, incident) == "verified" {
							input.AuditAcknowledgedInvalid++
						} else {
							input.AuditInvalidRecent++
						}
					}
				}
				input.AuditDurable = incidentErr == nil && stale == 0 && input.AuditInvalidRecent == 0 && input.AuditRetentionDays >= 90
			}
		}
	}

	if h.db == nil || h.db.Pool == nil {
		input.MissingRecoveryDrills = append(input.MissingRecoveryDrills, requiredRecoveryDrillKinds...)
	} else if latest, err := h.db.LatestPassedRecoveryDrills(ctx); err != nil {
		input.ProbeErrors = append(input.ProbeErrors, "recovery drill inspection failed")
		input.MissingRecoveryDrills = append(input.MissingRecoveryDrills, requiredRecoveryDrillKinds...)
	} else {
		cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour)
		for _, kind := range requiredRecoveryDrillKinds {
			finished, ok := latest[kind]
			if !ok {
				input.MissingRecoveryDrills = append(input.MissingRecoveryDrills, kind)
			} else if finished.Before(cutoff) {
				input.StaleRecoveryDrills = append(input.StaleRecoveryDrills, kind)
			}
		}
	}

	if h.consul == nil {
		input.ProbeErrors = append(input.ProbeErrors, "observability service inspection failed")
	} else {
		input.ObservabilityReady = true
		for _, serviceName := range []string{"norn-prometheus-web", "norn-grafana-web", "norn-cadvisor-web"} {
			health, err := h.consul.ServiceHealthChecks(serviceName)
			if err != nil {
				input.ProbeErrors = append(input.ProbeErrors, "observability service inspection failed")
				input.ObservabilityReady = false
				break
			}
			passing := false
			for _, instance := range health {
				if instance.Status == "passing" {
					passing = true
					break
				}
			}
			input.ObservabilityReady = input.ObservabilityReady && passing
		}
	}

	sort.Strings(input.UntrustedDeployments)
	sort.Strings(input.MissingSnapshots)
	sort.Strings(input.StaleSnapshots)
	sort.Strings(input.MissingOffsiteBackups)
	sort.Strings(input.MissingRecoveryDrills)
	sort.Strings(input.StaleRecoveryDrills)
	return evaluateProductionReadiness(input, time.Now().UTC())
}

func evaluateProductionReadiness(in productionReadinessInput, now time.Time) ProductionReadinessReport {
	report := ProductionReadinessReport{Schema: productionReadinessSchema, GeneratedAt: now.Format(time.RFC3339), Checks: []ProductionReadinessCheck{}}
	add := func(check ProductionReadinessCheck) { report.Checks = append(report.Checks, check) }
	status := func(ok bool) string {
		if ok {
			return "pass"
		}
		return "fail"
	}

	add(ProductionReadinessCheck{ID: "profile.production", Category: "control", Status: status(in.ProductionProfile), Title: "Production profile enforcement", Detail: boolDetail(in.ProductionProfile, "Production startup and deploy admission gates are enabled.", "The API is running in the compatibility development profile."), Remediation: "Satisfy the readiness blockers, then set NORN_PROFILE=production."})
	add(ProductionReadinessCheck{ID: "control.explicit_auth", Category: "control", Status: status(in.ExplicitAuth), Title: "Explicit control-plane authentication", Detail: boolDetail(in.ExplicitAuth, "Every protected route requires an authenticated principal.", "Loopback and temporary grant compatibility access is still enabled."), Remediation: "Set NORN_REQUIRE_EXPLICIT_AUTH=true and restart the Norn API."})
	add(ProductionReadinessCheck{ID: "control.credentials", Category: "control", Status: status(in.StrongControlToken || in.CloudflareAccess), Title: "Production control credential", Detail: boolDetail(in.StrongControlToken || in.CloudflareAccess, "A strong bearer root or validated Cloudflare Access configuration is present.", "No production-strength control credential is configured."), Remediation: "Configure a 32-byte or longer NORN_API_TOKEN or a complete Cloudflare Access application."})
	add(ProductionReadinessCheck{ID: "control.legacy_signing", Category: "control", Status: status(in.LegacySigningRetired), Title: "Legacy token signing retired", Detail: boolDetail(in.LegacySigningRetired, "The legacy raw-key token signature acceptance window has ended.", "Legacy raw-key token signatures are still accepted."), Remediation: "Expire NORN_LEGACY_TOKEN_SIGNING_UNTIL before production admission."})
	add(ProductionReadinessCheck{ID: "control.bind", Category: "control", Status: status(in.ControlLoopback || in.ExplicitAuth), Title: "Control API exposure", Detail: boolDetail(in.ControlLoopback || in.ExplicitAuth, "The API is loopback-bound or explicit authentication is enforced.", "The API is exposed beyond loopback without explicit-auth mode."), Remediation: "Bind to loopback behind trusted TLS ingress or enable explicit authentication."})

	add(ProductionReadinessCheck{ID: "nomad.reachable", Category: "scheduler", Status: status(in.NomadReachable), Title: "Nomad reachable", Detail: boolDetail(in.NomadReachable, "Nomad agent inspection succeeded.", "Nomad agent inspection failed."), Remediation: "Restore authenticated Nomad API connectivity."})
	add(ProductionReadinessCheck{ID: "nomad.acl", Category: "scheduler", Status: status(in.NomadACL), Title: "Nomad ACLs", Detail: boolDetail(in.NomadACL, "Nomad ACL enforcement is enabled.", "Nomad ACL enforcement is disabled."), Remediation: "Enable Nomad ACLs, bootstrap management access, and issue a least-privilege Norn policy token."})
	add(ProductionReadinessCheck{ID: "nomad.tls", Category: "scheduler", Status: status(in.NomadTLS), Title: "Nomad TLS", Detail: boolDetail(in.NomadTLS, "Nomad HTTP and RPC traffic is protected by TLS.", "Nomad HTTP or RPC TLS is not fully enabled."), Remediation: "Enable Nomad HTTP/RPC TLS and configure Norn with a verified HTTPS client."})
	add(countCheck("nomad.quorum", "scheduler", "Nomad server quorum", in.NomadServers, 3, "Run at least three healthy Nomad servers."))
	add(countCheck("nomad.clients", "scheduler", "Nomad client capacity", in.NomadClients, 2, "Run at least two ready Nomad clients across failure domains."))

	add(ProductionReadinessCheck{ID: "consul.reachable", Category: "discovery", Status: status(in.ConsulReachable), Title: "Consul reachable", Detail: boolDetail(in.ConsulReachable, "Consul agent inspection succeeded.", "Consul agent inspection failed."), Remediation: "Restore authenticated Consul API connectivity."})
	add(ProductionReadinessCheck{ID: "consul.acl", Category: "discovery", Status: status(in.ConsulACL), Title: "Consul ACLs", Detail: boolDetail(in.ConsulACL, "Consul ACL enforcement is enabled.", "Consul ACL enforcement is disabled."), Remediation: "Enable Consul ACLs with default-deny and issue scoped agent and Norn tokens."})
	add(ProductionReadinessCheck{ID: "consul.tls", Category: "discovery", Status: status(in.ConsulTLS), Title: "Consul TLS", Detail: boolDetail(in.ConsulTLS, "Consul HTTPS and internal RPC verification are enabled.", "Consul HTTPS or internal RPC verification is incomplete."), Remediation: "Enable verified Consul TLS for HTTP and internal RPC traffic."})
	add(countCheck("consul.quorum", "discovery", "Consul server quorum", in.ConsulServers, 3, "Run at least three healthy Consul servers."))

	add(ProductionReadinessCheck{ID: "database.tls", Category: "data", Status: status(in.DatabaseTLS), Title: "Control database transport", Detail: boolDetail(in.DatabaseTLS, "The PostgreSQL connection verifies the server identity with TLS.", "The PostgreSQL connection does not require verified server identity."), Remediation: "Use a PostgreSQL DSN with sslmode=verify-full and a trusted root certificate."})
	add(ProductionReadinessCheck{ID: "database.external", Category: "data", Status: status(in.DatabaseExternal), Title: "External control database", Detail: boolDetail(in.DatabaseExternal, "The control database is outside the Norn process host failure domain.", "The control database resolves to loopback or a local socket."), Remediation: "Use a separately supervised PostgreSQL endpoint outside the Norn node failure domain."})
	add(ProductionReadinessCheck{ID: "database.pitr", Category: "data", Status: status(in.DatabasePITR), Title: "PostgreSQL point-in-time recovery", Detail: boolDetail(in.DatabasePITR, "WAL archiving is configured for point-in-time recovery.", "PostgreSQL WAL archiving is not configured for point-in-time recovery."), Remediation: "Enable archive_mode with a tested archive_command or archive_library and retain base backups."})
	add(countCheck("database.replicas", "data", "PostgreSQL streaming replicas", in.DatabaseReplicas, 1, "Run at least one streaming standby in a separate failure domain."))
	add(ProductionReadinessCheck{ID: "secrets.strict", Category: "supply-chain", Status: status(in.SecretProblems == 0 && in.SecretMigrationItems == 0), Title: "Strict secret posture", Detail: fmt.Sprintf("%d app(s) need secret attention; %d plaintext migration item(s) remain.", in.SecretProblems, in.SecretMigrationItems), Remediation: "Run norn secrets migrate-plan, remove plaintext values, and enforce strict-secret validation.", Evidence: map[string]any{"appsNeedingAttention": in.SecretProblems, "migrationItems": in.SecretMigrationItems}})
	add(listCheck("deployments.provenance", "supply-chain", "Content-addressed deployment provenance", in.UntrustedDeployments, "Every latest deployment is clean, content-addressed by digest, and originates from git or rollback.", "Redeploy from a clean git source and pin the submitted registry artifact by OCI digest."))

	snapshotProblems := append(append([]string{}, in.MissingSnapshots...), in.StaleSnapshots...)
	add(listCheck("snapshots.fresh", "recovery", "Fresh database snapshots", snapshotProblems, fmt.Sprintf("All %d stateful app(s) have a snapshot newer than seven days.", in.StatefulApps), "Create current snapshots and schedule recurring backup verification."))
	add(listCheck("snapshots.offsite", "recovery", "Off-site snapshot export", in.MissingOffsiteBackups, "Every stateful app declares an off-site export bucket.", "Declare snapshots.exportBucket and verify export/import with an isolated restore drill."))
	add(recoveryDrillCheck(in.MissingRecoveryDrills, in.StaleRecoveryDrills))
	add(ProductionReadinessCheck{ID: "observability.core", Category: "operations", Status: status(in.ObservabilityReady), Title: "Core observability services", Detail: boolDetail(in.ObservabilityReady, "Prometheus, Grafana, and cAdvisor are passing.", "The core observability bundle is not fully passing."), Remediation: "Deploy and assure the Norn observability bundle before admitting production workloads."})
	add(ProductionReadinessCheck{ID: "audit.durable", Category: "operations", Status: status(in.AuditDurable), Title: "Durable mutation audit", Detail: boolDetail(in.AuditDurable, "Principal-aware mutation receipts are persisted, integrity-verified, and retained for at least 90 days.", "Mutation audit persistence, integrity, or retention is not production-ready."), Remediation: "Configure NORN_AUDIT_SIGNING_KEY and at least 90 retention days, repair audit persistence, and investigate stale or invalid receipts.", Evidence: map[string]any{"stalePending": in.AuditStalePending, "invalidRecent": in.AuditInvalidRecent, "acknowledgedInvalid": in.AuditAcknowledgedInvalid, "retentionDays": in.AuditRetentionDays}})
	add(ProductionReadinessCheck{ID: "topology.public_metadata", Category: "control", Status: "warn", Title: "Public discovery metadata", Detail: "The service manifest, metrics, capabilities, and OpenAPI routes are intentionally authentication-exempt.", Remediation: "Review and minimize production-safe public fields, especially private topology and operational metadata."})
	if len(in.ProbeErrors) > 0 {
		add(ProductionReadinessCheck{ID: "inspection.complete", Category: "control", Status: "fail", Title: "Readiness inspection completeness", Detail: fmt.Sprintf("%d readiness probe(s) could not complete.", len(in.ProbeErrors)), Remediation: "Restore probe access and rerun the readiness check.", Evidence: map[string]any{"errors": in.ProbeErrors}})
	}

	for _, check := range report.Checks {
		switch check.Status {
		case "pass":
			report.Summary.Passed++
		case "warn":
			report.Summary.Warnings++
		default:
			report.Summary.Failed++
		}
	}
	report.Summary.Total = len(report.Checks)
	if report.Summary.Failed == 0 {
		report.Status = "ready"
	} else {
		report.Status = "blocked"
	}
	return report
}

func boolDetail(ok bool, pass, fail string) string {
	if ok {
		return pass
	}
	return fail
}

func countCheck(id, category, title string, actual, minimum int, remediation string) ProductionReadinessCheck {
	check := ProductionReadinessCheck{ID: id, Category: category, Status: "pass", Title: title, Detail: fmt.Sprintf("%d healthy member(s); minimum is %d.", actual, minimum), Evidence: map[string]any{"actual": actual, "minimum": minimum}}
	if actual < minimum {
		check.Status = "fail"
		check.Remediation = remediation
	}
	return check
}

func listCheck(id, category, title string, items []string, pass, remediation string) ProductionReadinessCheck {
	check := ProductionReadinessCheck{ID: id, Category: category, Status: "pass", Title: title, Detail: pass, Evidence: map[string]any{"count": 0}}
	if len(items) > 0 {
		check.Status = "fail"
		check.Detail = fmt.Sprintf("%d app(s) do not satisfy this gate.", len(items))
		check.Remediation = remediation
		check.Evidence = map[string]any{"count": len(items), "apps": items}
	}
	return check
}

func recoveryDrillCheck(missing, stale []string) ProductionReadinessCheck {
	check := ProductionReadinessCheck{
		ID: "recovery.drills", Category: "recovery", Status: "pass", Title: "Recent recovery drills",
		Detail:   "Database restore, artifact rollback, and node failover have passed within 90 days.",
		Evidence: map[string]any{"missing": []string{}, "stale": []string{}},
	}
	if len(missing) > 0 || len(stale) > 0 {
		check.Status = "fail"
		check.Detail = fmt.Sprintf("%d required drill(s) are missing; %d are older than 90 days.", len(missing), len(stale))
		check.Remediation = "Run isolated database restore, digest rollback, and node failover drills; record bounded evidence with norn production drill."
		check.Evidence = map[string]any{"missing": missing, "stale": stale}
	}
	return check
}

func isLoopbackBind(bind string) bool {
	bind = strings.TrimSpace(strings.ToLower(bind))
	return bind == "localhost" || bind == "127.0.0.1" || bind == "::1"
}

func urlScheme(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Scheme
}

func databaseUsesTLS(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Query().Get("sslmode"), "verify-full")
}

func databaseHostIsExternal(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.TrimSpace(strings.ToLower(parsed.Hostname()))
	if host == "" || host == "localhost" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback()
	}
	return true
}

func nestedBool(root map[string]any, path ...string) bool {
	var current any = root
	for _, wanted := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return false
		}
		found := false
		for key, value := range object {
			if strings.EqualFold(key, wanted) {
				current = value
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	value, ok := current.(bool)
	return ok && value
}
