package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"norn/v2/api/auth"
	"norn/v2/api/beacon"
	"norn/v2/api/cloudflared"
	"norn/v2/api/config"
	"norn/v2/api/consul"
	"norn/v2/api/contract"
	"norn/v2/api/handler"
	"norn/v2/api/hub"
	"norn/v2/api/nomad"
	"norn/v2/api/observe"
	"norn/v2/api/pipeline"
	"norn/v2/api/redpanda"
	"norn/v2/api/saga"
	"norn/v2/api/secrets"
	"norn/v2/api/storage"
	"norn/v2/api/store"
	"norn/v2/api/watch"
	"norn/v2/api/worker"
)

func main() {
	cfg := config.Load()
	if err := validateControlSecurity(cfg); err != nil {
		log.Fatalf("security configuration: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownOTEL, err := observe.Setup(ctx, observe.ConfigFromEnv("norn-api"))
	cancel()
	if err != nil {
		log.Printf("WARNING: otel setup: %v", err)
	}
	observe.ConfigureLogging("norn-api")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownOTEL != nil {
			_ = shutdownOTEL(ctx)
		}
	}()

	cloudflared.SetConfigPath(cfg.CloudflaredConfig)

	// Database
	db, err := store.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	if err := store.Migrate(db); err != nil {
		log.Fatalf("migration: %v", err)
	}

	if os.Getenv("NORN_SKIP_DEPLOYMENT_RECOVERY") == "true" {
		log.Println("deployment recovery skipped")
	} else if err := db.RecoverInFlightDeployments(context.Background()); err != nil {
		log.Printf("WARNING: deployment recovery: %v", err)
	}
	if os.Getenv("NORN_SKIP_OPERATION_RECOVERY") == "true" {
		log.Println("operation recovery skipped")
	} else if err := db.RecoverInFlightOperations(context.Background()); err != nil {
		log.Printf("WARNING: operation recovery: %v", err)
	}

	// Nomad
	nomadClient, err := nomad.NewClient(cfg.NomadAddr)
	if err != nil {
		log.Printf("WARNING: nomad unavailable (%v)", err)
	} else {
		if err := nomadClient.Healthy(); err != nil {
			log.Printf("WARNING: nomad not healthy (%v)", err)
		} else {
			log.Println("nomad connected at " + cfg.NomadAddr)
		}
	}

	// Consul
	consulClient, err := consul.NewClient(cfg.ConsulAddr)
	if err != nil {
		log.Printf("WARNING: consul unavailable (%v)", err)
	} else {
		if err := consulClient.Healthy(); err != nil {
			log.Printf("WARNING: consul not healthy (%v)", err)
		} else {
			log.Println("consul connected at " + cfg.ConsulAddr)
		}
	}

	// S3
	var s3Client *storage.Client
	if cfg.S3Endpoint != "" {
		s3Client, err = storage.NewClient(storage.Config{
			Endpoint:            cfg.S3Endpoint,
			AccessKey:           cfg.S3AccessKey,
			SecretKey:           cfg.S3SecretKey,
			Region:              cfg.S3Region,
			UseSSL:              cfg.S3UseSSL,
			Provider:            cfg.S3Provider,
			ForcePathStyle:      cfg.S3ForcePath,
			GarageAdminEndpoint: cfg.GarageAdminEndpoint,
			GarageAdminToken:    cfg.GarageAdminToken,
		})
		if err != nil {
			log.Printf("WARNING: S3 storage unavailable (%v)", err)
		} else {
			log.Println("S3 storage connected at " + cfg.S3Endpoint)
		}
	}

	// Redpanda / Kafka
	var redpandaClient *redpanda.Client
	if len(cfg.RedpandaBrokers) > 0 {
		redpandaClient, err = redpanda.NewClient(redpanda.Config{
			Brokers: cfg.RedpandaBrokers,
			RPKPath: cfg.RedpandaRPKPath,
		})
		if err != nil {
			log.Printf("WARNING: redpanda unavailable (%v)", err)
		} else if err := redpandaClient.Healthy(context.Background()); err != nil {
			log.Printf("WARNING: redpanda not healthy (%v)", err)
		} else {
			log.Println("redpanda connected at " + strings.Join(cfg.RedpandaBrokers, ","))
		}
	}

	// WebSocket hub
	allowedOrigins := []string{"http://localhost:5173", "http://localhost:3000"}
	if cfg.AllowedOrigins != "" {
		for _, o := range strings.Split(cfg.AllowedOrigins, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				allowedOrigins = append(allowedOrigins, o)
			}
		}
	}
	ws := hub.New(allowedOrigins)
	ws.SetStore(db)
	go ws.Run()

	beaconSvc := beacon.New(db, ws, beacon.Config{
		Environment: cfg.BeaconEnvironment,
		SinkURL:     cfg.BeaconSinkURL,
		SinkKeyID:   cfg.BeaconSinkKeyID,
		SinkSecret:  cfg.BeaconSinkSecret,
	})
	if cfg.BeaconSinkURL != "" {
		log.Println("beacon sink configured")
	}

	notifier := beacon.NewNotifier(db)
	beaconSvc.SetNotifier(notifier)

	// Saga store
	sagaStore := saga.NewPostgresStore(db.Pool)

	// Secrets manager
	sec := secrets.NewManager(cfg.AppsDir)

	// Deploy pipeline
	pipe := &pipeline.Pipeline{
		DB:                       db,
		Nomad:                    nomadClient,
		Consul:                   consulClient,
		WS:                       ws,
		SagaStore:                sagaStore,
		Secrets:                  sec,
		AppsDir:                  cfg.AppsDir,
		GitToken:                 cfg.GitToken,
		GitSSHKey:                cfg.GitSSHKey,
		RegistryURL:              cfg.RegistryURL,
		NetworkMode:              cfg.NetworkMode,
		IngressURL:               cfg.IngressURL,
		ExternalIngress:          cfg.ExternalIngress,
		Production:               cfg.Production(),
		StrictSecrets:            cfg.StrictSecrets,
		ArtifactSigningPublicKey: cfg.ArtifactSigningPublicKey,
		ArtifactDenySeverities:   cfg.ArtifactDenySeverities,
		CosignPath:               cfg.CosignPath,
		TrivyPath:                cfg.TrivyPath,
		Beacon:                   beaconSvc,
		Storage:                  s3Client,
		Redpanda:                 redpandaClient,
	}

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	if os.Getenv("NORN_SKIP_OPERATION_WORKER") == "true" {
		log.Println("operation worker skipped")
	} else {
		opWorker := worker.NewOperationWorker(db, pipe)
		go opWorker.Run(workerCtx)
	}
	if os.Getenv("NORN_SKIP_NOMAD_WATCHER") == "true" {
		log.Println("nomad allocation watcher skipped")
	} else {
		nomadWatcher := watch.NewNomadAllocationWatcher(nomadClient, consulClient, beaconSvc, cfg.AppsDir)
		go nomadWatcher.Run(workerCtx)
	}

	// Handler
	h := handler.New(db, nomadClient, consulClient, ws, cfg, pipe, beaconSvc, sec, sagaStore, s3Client, redpandaClient)

	// Router
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "Cf-Access-Jwt-Assertion", "Idempotency-Key", "X-Norn-Step-Up"},
		AllowCredentials: true,
	}))

	// CF Access auth
	if cfg.CFAccessTeamDomain != "" && cfg.CFAccessAUD != "" {
		cfValidator := auth.NewCFAccessValidator(cfg.CFAccessTeamDomain, cfg.CFAccessAUD)
		r.Use(cfValidator.Middleware)
		log.Println("CF Access auth enabled")
	}
	r.Use(h.WakeGatewayHostMiddleware)

	// Control authorization also enforces Cloudflare-only explicit-auth
	// configurations. The CF validator authenticates assertions when present;
	// this layer rejects their absence on protected routes.
	if cfg.APIToken != "" || cfg.RequireExplicitAuth {
		r.Use(bearerAuth(cfg.APIToken, h, cfg.RequireExplicitAuth))
		log.Println("control authorization enabled")
	}
	r.Use(h.MutationAuditMiddleware)
	r.Use(h.ProductionMutationAdmissionMiddleware)
	r.Use(h.AccessMiddleware)
	r.Get("/metrics", h.Metrics)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", h.Health)
		r.Get("/metrics", h.Metrics)
		r.Get("/version", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"version": Version})
		})

		r.Post("/webhooks/{provider}", h.Webhook)
		r.Get("/webhooks/deliveries", h.ListWebhookDeliveries)
		r.Post("/webhooks/deliveries/{id}/replay", h.ReplayWebhookDelivery)

		r.Get("/stats", h.Stats)
		r.Get("/observability/bundle", h.ObservabilityBundle)
		r.Get("/observability/alerts.yml", h.PrometheusAlerts)
		r.Get("/observability/prometheus.yml", h.PrometheusConfig)
		r.Post("/observability/services/install", h.ObservabilityServicesInstall)
		r.Get("/services/manifest", h.ServiceManifest)
		r.Get("/ops/platform", h.PlatformOps)
		r.Get("/operator/inbox", h.OperatorInbox)
		r.Get("/operator/cron", h.OperatorCronOverview)
		r.Get("/operator/wake-targets", h.OperatorWakeTargets)
		r.Get("/operator/deploy-confidence", h.OperatorDeployConfidence)
		r.Get("/operator/snapshot-readiness", h.OperatorSnapshotReadiness)
		r.Get("/operator/auth-hints", h.OperatorAuthHints)
		r.Get("/operator/actions", h.OperatorActions)
		r.Get("/platform/releases", h.PlatformReleases)
		r.Post("/platform/releases/{sha}/rollback", h.PlatformRollbackRelease)
		r.Get("/ops/contextdb", h.ContextDBOps)
		r.Post("/ops/contextdb/feedback/{eventID}/rollback", h.ContextDBRollbackFeedback)
		r.Get("/apps", h.ListApps)
		r.Get("/deployments", h.ListDeployments)
		r.Get("/deployments/{id}/steps", h.ListDeploymentSteps)
		r.Get("/operations", h.ListOperations)
		r.Get("/operations/active", h.ActiveOperations)
		r.Get("/operations/{id}", h.GetOperation)
		r.Get("/alerts/rules", h.AlertRules)
		r.Get("/resources/suggestions", h.ResourceSuggestions)
		r.Get("/tuning/recommendations", h.TuningRecommendations)
		r.Get("/events", h.ListEvents)
		r.Post("/events", h.CreateEvent)
		r.Get("/events/active", h.ActiveIncidents)
		r.Get("/events/correlated", h.CorrelatedEvents)
		r.Post("/events/reconcile", h.ReconcileEvents)
		r.Get("/events/{id}", h.GetEvent)
		r.Post("/events/{id}/ack", h.AcknowledgeEvent)
		r.Post("/events/{id}/snooze", h.SnoozeEvent)
		r.Post("/events/{id}/open", h.OpenEvent)
		r.Get("/events/sinks", h.EventSinks)
		r.Post("/events/test", h.TestEvent)
		r.Post("/incidents/action", h.IncidentAction)
		r.Get("/validate", h.ValidateAll)
		r.Get("/validate/{id}", h.ValidateApp)
		r.Get("/secrets/status", h.SecretsStatusAll)
		r.Get("/secrets/migration-plan", h.SecretsMigrationPlan)
		r.Get("/saga", h.ListRecentSaga)
		r.Get("/saga/{sagaId}", h.GetSagaEvents)
		r.Get("/cloudflared/ingress", h.CloudflaredIngress)
		r.Get("/access/events", h.AccessEvents)
		r.Get("/access/patterns", h.AccessPatterns)
		r.Post("/access/observations", h.RecordAccessObservations)
		r.Get("/access/cloudflare/status", h.CloudflareAccessStatus)
		r.Post("/access/cloudflare/sync", h.CloudflareAccessSync)
		r.Post("/access/cloudflare/logpush", h.CloudflareLogpush)
		r.HandleFunc("/a/{app}", h.WakeGatewayAppAlias)
		r.HandleFunc("/a/{app}/*", h.WakeGatewayAppAlias)
		r.HandleFunc("/wake-gateway/{host}", h.WakeGateway)
		r.HandleFunc("/wake-gateway/{host}/*", h.WakeGateway)

		r.Get("/notifications/channels", h.ListNotificationChannels)
		r.Post("/notifications/channels", h.CreateNotificationChannel)
		r.Post("/notifications/channels/bootstrap", h.BootstrapNotificationChannels)
		r.Post("/notifications/channels/{id}/test", h.TestNotificationChannel)
		r.Delete("/notifications/channels/{id}", h.DeleteNotificationChannel)
		r.Get("/deploy-groups", h.ListDeployGroups)
		r.Post("/deploy-groups/{name}/deploy", h.DeployGroup)

		r.Get("/access/grants", h.ListAccessGrants)
		r.Post("/access/grants", h.CreateAccessGrant)
		r.Delete("/access/grants/{id}", h.DeleteAccessGrant)
		r.Post("/access/tokens", h.CreateAccessToken)

		r.Get("/ops/contextdb/evaluator-readiness", h.EvaluatorReadiness)

		r.Get("/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
			writeControlCapabilities(w)
		})
		r.Get("/v1/openapi.yaml", contract.ServeOpenAPI)
		r.Post("/v1/enrollments", h.StartDeviceEnrollment)
		r.Get("/v1/enrollments", h.ListDeviceEnrollments)
		r.Post("/v1/enrollments/approve", h.ApproveDeviceEnrollment)
		r.Post("/v1/enrollments/{id}/exchange", h.ExchangeDeviceEnrollment)
		r.Get("/v1/devices", h.ListDevices)
		r.Delete("/v1/devices/{id}", h.RevokeDevice)
		r.Post("/v1/auth/rotate", h.RotateCurrentToken)
		r.Post("/v1/auth/revoke", h.RevokeCurrentToken)
		r.Post("/v1/auth/step-up/challenges", h.CreateStepUpChallenge)
		r.Post("/v1/auth/step-up/challenges/{id}/verify", h.VerifyStepUpChallenge)
		r.Get("/v1/apps", h.ListApps)
		r.Post("/v1/apps", h.CreateApp)
		r.With(handler.ValidateAppID).Get("/v1/apps/{id}", h.GetApp)
		r.With(handler.ValidateAppID).Put("/v1/apps/{id}/deployment", h.UpdateAppDeployment)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/exec-sessions", h.CreateExecSession)
		r.With(handler.ValidateAppID).Get("/v1/apps/{id}/snapshots", h.ListAppSnapshotsV1)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/snapshots", h.QueueAppSnapshot)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/snapshots/retention", h.QueueAppSnapshotRetention)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/snapshots/{ts}/restore", h.QueueAppSnapshotRestore)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/migrations", h.QueueAppMigration)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/rollbacks", h.QueueAppRollback)
		r.Post("/v1/validate/infraspec", h.ValidateInfraSpecDocument)
		r.Post("/v1/fleet/validate", h.ValidateFleetDocument)
		r.Get("/v1/fleet/node-pools", h.FleetInventory)
		r.Get("/v1/fleet/plans", h.ListFleetPlans)
		r.Get("/v1/fleet/github", h.FleetGitHubStatus)
		r.Get("/v1/fleet/plans/{planID}/reconciliations", h.ListFleetReconciliations)
		r.Post("/v1/fleet/plans/{planID}/reconciliations", h.RecordFleetReconciliation)
		r.Post("/v1/fleet/plans/{planID}/github/pull-request", h.CreateFleetGitHubPullRequest)
		r.Post("/v1/fleet/plans/{planID}/github/dispatch", h.DispatchFleetGitHubApply)
		r.Post("/v1/fleet/node-pools/{pool}/plan", h.PlanFleetCapacity)
		r.Get("/v1/exec-sessions", h.ListExecSessions)
		r.Get("/v1/exec-sessions/{id}", h.GetExecSession)
		r.Delete("/v1/exec-sessions/{id}", h.CancelExecSession)
		r.Get("/v1/exec-sessions/{id}/stream", h.ExecSessionStream)
		r.Get("/v1/releases", h.PlatformReleases)
		r.Get("/v1/host/status", h.HostStatus)
		r.Get("/v1/host/metrics", h.HostMetrics)
		r.Get("/v1/production/readiness", h.ProductionReadiness)
		r.Get("/v1/production/drills", h.ListRecoveryDrills)
		r.Post("/v1/production/drills", h.StartRecoveryDrill)
		r.Post("/v1/production/drills/{id}/complete", h.CompleteRecoveryDrill)
		r.Get("/v1/audit/mutations", h.MutationAuditEvents)
		r.Post("/v1/audit/mutations/{id}/incident", h.AcknowledgeMutationAuditIncident)
		r.Get("/v1/events/info", ws.HandleInfo)
		r.Get("/v1/events", ws.HandleConnect)
		r.Get("/v1/operations/{id}", h.GetOperation)
		r.Post("/v1/operations/{id}/cancel", h.CancelOperation)
		r.Post("/v1/platform/preflights", h.QueuePlatformPreflight)
		r.Post("/v1/platform/upgrades", h.QueuePlatformUpgrade)
		r.Post("/v1/platform/rollbacks", h.QueuePlatformRollback)
		r.Post("/v1/platform/smoke", h.QueuePlatformSmoke)
		r.Post("/v1/host/assurances", h.QueueHostAssurance)

		r.Route("/apps/{id}", func(r chi.Router) {
			r.Use(handler.ValidateAppID)
			r.Get("/", h.GetApp)
			r.Post("/preflight", h.Preflight)
			r.Post("/deploy", h.Deploy)
			r.Get("/logs", h.StreamLogs)
			r.Post("/restart", h.RestartApp)
			r.Post("/scale", h.ScaleApp)
			r.Post("/rollback", h.Rollback)
			r.Get("/secrets", h.ListSecrets)
			r.Get("/secrets/status", h.SecretsStatusApp)
			r.Put("/secrets", h.UpdateSecrets)
			r.Delete("/secrets/{key}", h.DeleteSecret)
			r.Get("/snapshots", h.ListSnapshots)
			r.Post("/snapshots/retention", h.ApplySnapshotRetention)
			r.Post("/snapshots/{ts}/restore", h.RestoreSnapshot)
			r.Get("/cron/history", h.CronHistory)
			r.Post("/cron/trigger", h.CronTrigger)
			r.Post("/cron/pause", h.CronPause)
			r.Post("/cron/resume", h.CronResume)
			r.Put("/cron/schedule", h.CronUpdateSchedule)
			r.Post("/invoke", h.InvokeFunction)
			r.Get("/function/history", h.FunctionHistory)
			r.Get("/canary", h.CanaryStatus)
			r.Post("/promote", h.PromoteCanary)
			r.Post("/snapshots/export", h.ExportSnapshot)
			r.Get("/snapshots/remote", h.ListRemoteSnapshots)
			r.Post("/snapshots/import", h.ImportSnapshot)
			r.Post("/forge", h.Forge)
			r.Post("/teardown", h.Teardown)
			r.Post("/endpoints/toggle", h.ToggleEndpoint)
			r.Get("/exec", h.ExecAlloc)
		})
	})

	r.Get("/ws", ws.HandleConnect)

	// Serve UI static files
	if cfg.UIDir != "" {
		fileServer(r, cfg.UIDir)
	}

	srv := &http.Server{
		Addr:              cfg.BindAddr + ":" + cfg.Port,
		Handler:           otelhttp.NewHandler(r, "norn.api"),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		log.Printf("norn v2 %s listening on %s:%s", Version, cfg.BindAddr, cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	workerCancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)
}

func validateControlSecurity(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("configuration is required")
	}
	if cfg.APIToken != "" && len(cfg.APIToken) < 32 {
		return fmt.Errorf("NORN_API_TOKEN must contain at least 32 bytes")
	}
	profile := strings.ToLower(strings.TrimSpace(cfg.Profile))
	if profile == "" {
		profile = "development"
	}
	if profile != "development" && profile != "production" {
		return fmt.Errorf("NORN_PROFILE must be development or production")
	}
	cfDomainConfigured := strings.TrimSpace(cfg.CFAccessTeamDomain) != ""
	cfAudienceConfigured := strings.TrimSpace(cfg.CFAccessAUD) != ""
	if cfDomainConfigured != cfAudienceConfigured {
		return fmt.Errorf("NORN_CF_ACCESS_TEAM_DOMAIN and NORN_CF_ACCESS_AUD must be configured together")
	}
	if cfg.RequireExplicitAuth && cfg.APIToken == "" && !cfDomainConfigured {
		return fmt.Errorf("NORN_REQUIRE_EXPLICIT_AUTH requires NORN_API_TOKEN or Cloudflare Access")
	}
	if cfg.APIToken == "" {
		bind := strings.TrimSpace(cfg.BindAddr)
		ip := net.ParseIP(bind)
		if bind != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("NORN_API_TOKEN is required when binding beyond loopback")
		}
	}
	if profile == "production" {
		if !cfg.RequireExplicitAuth {
			return fmt.Errorf("NORN_PROFILE=production requires NORN_REQUIRE_EXPLICIT_AUTH=true")
		}
		if !cfg.StrictSecrets {
			return fmt.Errorf("NORN_PROFILE=production requires strict secret validation")
		}
		if !secureEndpoint(cfg.NomadAddr) {
			return fmt.Errorf("NORN_PROFILE=production requires an https NORN_NOMAD_ADDR")
		}
		if cfg.NomadTLSSkipVerify {
			return fmt.Errorf("NORN_PROFILE=production forbids NOMAD_SKIP_VERIFY")
		}
		if !secureEndpoint(cfg.ConsulAddr) {
			return fmt.Errorf("NORN_PROFILE=production requires an https NORN_CONSUL_ADDR")
		}
		if cfg.ConsulTLSSkipVerify {
			return fmt.Errorf("NORN_PROFILE=production requires CONSUL_HTTP_SSL_VERIFY=true")
		}
		if !secureDatabaseDSN(cfg.DatabaseURL) {
			return fmt.Errorf("NORN_PROFILE=production requires PostgreSQL sslmode=verify-full")
		}
		if strings.TrimSpace(cfg.RegistryURL) == "" {
			return fmt.Errorf("NORN_PROFILE=production requires NORN_REGISTRY_URL")
		}
		if strings.TrimSpace(cfg.ArtifactSigningPublicKey) == "" {
			return fmt.Errorf("NORN_PROFILE=production requires NORN_ARTIFACT_SIGNING_PUBLIC_KEY")
		}
		if len(cfg.ArtifactDenySeverities) == 0 {
			return fmt.Errorf("NORN_PROFILE=production requires NORN_ARTIFACT_DENY_SEVERITIES")
		}
		if len(cfg.AuditSigningKey) < 32 {
			return fmt.Errorf("NORN_PROFILE=production requires NORN_AUDIT_SIGNING_KEY with at least 32 bytes")
		}
		for _, key := range cfg.AuditPreviousSigningKeys {
			if len(key) < 32 {
				return fmt.Errorf("NORN_AUDIT_PREVIOUS_SIGNING_KEYS entries must contain at least 32 bytes")
			}
		}
		if cfg.AuditRetentionDays < 90 {
			return fmt.Errorf("NORN_PROFILE=production requires NORN_AUDIT_RETENTION_DAYS of at least 90")
		}
		if time.Now().UTC().Before(cfg.LegacyTokenSigningUntil) {
			return fmt.Errorf("NORN_PROFILE=production requires legacy token signing to be retired")
		}
	}
	return nil
}

func secureEndpoint(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(parsed.Scheme, "https")
}

func secureDatabaseDSN(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Query().Get("sslmode"), "verify-full")
}

func bearerAuth(token string, h *handler.Handler, requireExplicit bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if publicControlPath(r.URL.Path) || publicEnrollmentRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			requiredScope := controlScopeForRequest(r)
			if claims, ok := auth.CFAccessClaimsFromRequest(r); ok {
				subject := strings.TrimSpace(claims.Email)
				if subject == "" {
					subject = strings.TrimSpace(claims.Subject)
				}
				next.ServeHTTP(w, handler.WithAccessPrincipal(r, &handler.AccessPrincipal{
					Subject: subject, Scopes: []string{handler.ScopeAdmin},
				}))
				return
			}
			authorization := r.Header.Get("Authorization")
			if token != "" && strings.HasPrefix(authorization, "Bearer ") && subtle.ConstantTimeCompare([]byte(authorization[7:]), []byte(token)) == 1 {
				next.ServeHTTP(w, handler.WithAccessPrincipal(r, &handler.AccessPrincipal{
					Subject: "control-plane", Scopes: []string{handler.ScopeAdmin}, Legacy: true,
				}))
				return
			}
			if strings.HasPrefix(authorization, "Bearer ") && h != nil {
				if principal, ok := h.VerifyAccessToken(authorization[7:]); ok {
					if !principal.Allows(requiredScope) {
						handler.WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "token lacks required scope "+requiredScope)
						return
					}
					next.ServeHTTP(w, handler.WithAccessPrincipal(r, principal))
					return
				}
			}
			if !requireExplicit {
				directIP := directClientIP(r)
				if directIP != nil && directIP.IsLoopback() && !hasForwardedClient(r) {
					next.ServeHTTP(w, r)
					return
				}
				ip := clientIPFromRequest(r)
				if h != nil && h.HasActiveGrant(ip) {
					next.ServeHTTP(w, r)
					return
				}
			}
			handler.WriteControlProblem(w, r, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
		})
	}
}

func publicEnrollmentRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	if r.URL.Path == "/api/v1/enrollments" {
		return true
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "enrollments" && parts[4] == "exchange"
}

func publicControlPath(path string) bool {
	if path == "/metrics" || path == "/api/metrics" || path == "/api/health" || path == "/api/version" || path == "/api/services/manifest" || path == "/api/v1/openapi.yaml" || path == "/api/v1/capabilities" {
		return true
	}
	return path == "/api/webhooks/github" || path == "/api/webhooks/gitea" ||
		strings.HasPrefix(path, "/api/a/") || strings.HasPrefix(path, "/api/wake-gateway/") ||
		path == "/api/access/cloudflare/logpush" || (!strings.HasPrefix(path, "/api/") && path != "/ws")
}

func controlScopeForRequest(r *http.Request) string {
	path := r.URL.Path
	switch {
	case path == "/ws" || path == "/api/v1/events":
		return handler.ScopeEventsRead
	case path == "/api/v1/events/info":
		return handler.ScopeEventsRead
	case strings.HasSuffix(path, "/cancel") && strings.HasPrefix(path, "/api/v1/operations/"):
		return ""
	case path == "/api/v1/auth/rotate" || path == "/api/v1/auth/revoke":
		return ""
	case strings.HasPrefix(path, "/api/v1/auth/step-up/") || strings.HasPrefix(path, "/api/v1/exec-sessions") || strings.HasSuffix(path, "/exec-sessions"):
		return handler.ScopeAppsExec
	case path == "/api/v1/enrollments" || path == "/api/v1/enrollments/approve" || strings.HasPrefix(path, "/api/v1/devices"):
		return handler.ScopeAdmin
	case path == "/api/v1/validate/infraspec" || path == "/api/v1/fleet/validate":
		return handler.ScopeAPIRead
	case strings.HasSuffix(path, "/exec"):
		return handler.ScopeAppsExec
	case path == "/api/access/tokens":
		return handler.ScopeAdmin
	case strings.HasPrefix(path, "/api/v1/platform/"):
		return handler.ScopePlatformOperate
	case strings.HasPrefix(path, "/api/v1/host/") && r.Method != http.MethodGet && r.Method != http.MethodHead:
		return handler.ScopeHostOperate
	case strings.HasPrefix(path, "/api/platform/") && r.Method != http.MethodGet && r.Method != http.MethodHead:
		return handler.ScopePlatformOperate
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		return handler.ScopeAPIRead
	default:
		return handler.ScopeAPIWrite
	}
}

func writeControlCapabilities(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"protocolVersion": 1,
		"serverVersion":   Version,
		"features": []string{
			"durable-operations", "event-cursor-replay", "platform-preflight", "platform-upgrade",
			"platform-rollback", "platform-smoke", "host-assurance", "scoped-access-tokens",
			"openapi-3.1", "standard-problems", "event-stream-info", "event-gap-detection",
			"event-heartbeat", "event-subscriptions", "operation-cancellation", "typed-operation-receipts",
			"versioned-resources", "device-enrollment", "token-rotation", "token-revocation", "device-listing",
			"device-key-step-up", "exec-sessions", "exec-audit", "exec-session-expiry", "exec-protocol-v1", "host-metrics", "app-creation", "durable-app-recovery-v1", "durable-snapshots", "standalone-migrations", "regional-deployments", "consul-traefik-ingress", "production-readiness", "durable-mutation-audit", "production-mutation-admission", "recovery-drill-receipts", "document-validation", "fleet-v1", "fleet-inventory", "durable-fleet-capacity-plans", "fleet-reconciliation-v1", "fleet-github-app-v1",
		},
		"auth": map[string]interface{}{
			"scopes":                handler.AccessTokenScopeNames(),
			"websocketBearerHeader": true,
			"websocketQueryToken":   false,
			"deviceEnrollment":      true,
			"stepUp": map[string]interface{}{
				"purposes": []string{"exec"}, "algorithm": "ES256", "publicKeyFormat": "P-256-X9.63",
				"challengeTTLSeconds": 120, "header": "X-Norn-Step-Up",
			},
			"tokens": map[string]interface{}{
				"deviceTTLSeconds": 2592000, "rotation": "atomic", "revocation": "registry",
			},
		},
		"endpoints": map[string]string{
			"events": "/api/v1/events", "operations": "/api/v1/operations/{id}",
			"platformPreflights": "/api/v1/platform/preflights", "platformUpgrades": "/api/v1/platform/upgrades",
			"platformRollbacks": "/api/v1/platform/rollbacks", "platformSmoke": "/api/v1/platform/smoke", "hostAssurances": "/api/v1/host/assurances",
			"openapi": "/api/v1/openapi.yaml", "eventInfo": "/api/v1/events/info", "apps": "/api/v1/apps", "appCreation": "/api/v1/apps", "appDeployment": "/api/v1/apps/{id}/deployment",
			"appSnapshots": "/api/v1/apps/{id}/snapshots", "appSnapshotRetention": "/api/v1/apps/{id}/snapshots/retention", "appSnapshotRestore": "/api/v1/apps/{id}/snapshots/{ts}/restore", "appMigrations": "/api/v1/apps/{id}/migrations", "appRollbacks": "/api/v1/apps/{id}/rollbacks",
			"releases": "/api/v1/releases", "hostStatus": "/api/v1/host/status", "hostMetrics": "/api/v1/host/metrics", "productionReadiness": "/api/v1/production/readiness", "recoveryDrills": "/api/v1/production/drills", "mutationAudit": "/api/v1/audit/mutations", "enrollments": "/api/v1/enrollments",
			"devices": "/api/v1/devices", "tokenRotate": "/api/v1/auth/rotate", "tokenRevoke": "/api/v1/auth/revoke",
			"stepUpChallenges": "/api/v1/auth/step-up/challenges", "execSessions": "/api/v1/exec-sessions",
			"infraSpecValidation": "/api/v1/validate/infraspec", "fleetValidation": "/api/v1/fleet/validate", "fleetNodePools": "/api/v1/fleet/node-pools", "fleetPlans": "/api/v1/fleet/plans", "fleetReconciliations": "/api/v1/fleet/plans/{planID}/reconciliations", "fleetGitHub": "/api/v1/fleet/github", "fleetGitHubPullRequest": "/api/v1/fleet/plans/{planID}/github/pull-request", "fleetGitHubDispatch": "/api/v1/fleet/plans/{planID}/github/dispatch",
		},
	})
}

func clientIPFromRequest(r *http.Request) string {
	directIP := directClientIP(r)
	if directIP != nil && directIP.IsLoopback() {
		if cfIP := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); cfIP != nil {
			return cfIP.String()
		}
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			if forwardedIP := net.ParseIP(strings.TrimSpace(parts[0])); forwardedIP != nil {
				return forwardedIP.String()
			}
		}
	}
	if directIP != nil {
		return directIP.String()
	}
	return r.RemoteAddr
}

func directClientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(strings.TrimSpace(host))
}

func hasForwardedClient(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("CF-Connecting-IP")) != "" || strings.TrimSpace(r.Header.Get("X-Forwarded-For")) != ""
}

func fileServer(r chi.Router, dir string) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		log.Printf("UI file server unavailable: %v", err)
		return
	}
	rootFS := root.FS()
	fileHandler := http.FileServerFS(rootFS)
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "."
		}
		if _, err := fs.Stat(rootFS, name); errors.Is(err, fs.ErrNotExist) {
			http.ServeFileFS(w, r, rootFS, "index.html")
			return
		}
		fileHandler.ServeHTTP(w, r)
	})
}
