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
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/handler"
	"norn/v2/api/hub"
	"norn/v2/api/nomad"
	"norn/v2/api/observe"
	"norn/v2/api/pipeline"
	"norn/v2/api/redpanda"
	"norn/v2/api/saga"
	"norn/v2/api/secrets"
	"norn/v2/api/startup"
	"norn/v2/api/storage"
	"norn/v2/api/store"
	"norn/v2/api/watch"
	"norn/v2/api/worker"
)

func main() {
	if handled, err := startup.WriteControlBackendProbe(os.Args[1:], os.Getenv, os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if handled, err := startup.WriteContractProbe(os.Args[1:], os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	startupCfg, err := startup.Parse(os.Getenv)
	if err != nil {
		log.Fatalf("startup configuration: %v", err)
	}
	backendCfg, err := startup.ParseControlBackend(os.Getenv)
	if err != nil {
		log.Fatalf("control backend: %v", err)
	}
	cfg := config.Load()
	if err := validateControlSecurityForBackend(cfg, backendCfg); err != nil {
		log.Fatalf("security configuration: %v", err)
	}
	if handled, err := runEtcdManagedCredentialBootstrap(os.Args[1:], cfg, backendCfg); handled {
		if err != nil {
			log.Fatalf("etcd managed credential bootstrap: %v", err)
		}
		return
	}
	if backendCfg.Backend == startup.BackendEtcd && backendCfg.SourceValidation {
		if err := startup.RequireEtcdSourceValidationStartup(startupCfg); err != nil {
			log.Fatalf("etcd source validation: %v", err)
		}
		if err := runEtcdSourceValidation(cfg, backendCfg); err != nil {
			log.Fatalf("etcd source validation: %v", err)
		}
		return
	}
	if backendCfg.Backend == startup.BackendEtcd {
		if startupCfg.SchemaMode == startup.SchemaModeMigrateOnly {
			if err := runEtcdMigrateOnly(backendCfg); err != nil {
				log.Fatalf("etcd migrate-only: %v", err)
			}
			return
		}
		if startupCfg.StartupMode == startup.ModePassive {
			if err := validatePassiveBind(cfg.BindAddr); err != nil {
				log.Fatalf("startup configuration: %v", err)
			}
			if err := runEtcdPassiveRuntime(cfg, backendCfg); err != nil {
				log.Fatalf("etcd passive runtime: %v", err)
			}
			return
		}
		if err := runEtcdFleetRuntime(cfg, backendCfg); err != nil {
			log.Fatalf("etcd fleet runtime: %v", err)
		}
		return
	}
	if err := startup.RequireRuntimeCapabilities(backendCfg); err != nil {
		log.Fatalf("control backend: %v", err)
	}
	databaseID := databaseIdentity(cfg.DatabaseURL, cfg.AuditSigningKey)
	if startupCfg.StartupMode == startup.ModePassive {
		if err := validatePassiveBind(cfg.BindAddr); err != nil {
			log.Fatalf("startup configuration: %v", err)
		}
	}

	// Establish schema compatibility before telemetry, runtime clients,
	// recovery, watchers, or workers. Passive mode must remain a read-only
	// database check and exposes only its three loopback status routes.
	db, err := store.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()
	migrator, err := store.NewControlSchemaMigrator(db)
	if err != nil {
		log.Fatalf("schema migration catalog: %v", err)
	}
	schemaStatus, err := startup.ApplySchemaMode(context.Background(), migrator, startupCfg)
	if err != nil {
		log.Fatalf("schema: %v", err)
	}
	if startupCfg.SchemaMode == startup.SchemaModeMigrateOnly {
		log.Printf("schema migration complete at version %d", schemaStatus.CurrentMigrationVersion)
		return
	}
	if startupCfg.StartupMode == startup.ModePassive {
		srv := newPassiveServer(cfg.BindAddr, cfg.Port, Version, databaseID, schemaStatus, startupCfg)
		go func() {
			log.Printf("norn %s passive schema status listening on %s", Version, srv.Addr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("passive server: %v", err)
			}
		}()
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
		return
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

	if err := db.ReconcileExecSessions(context.Background()); err != nil {
		log.Printf("WARNING: exec session lease recovery: %v", err)
	}

	if os.Getenv("NORN_SKIP_OPERATION_RECOVERY") == "true" {
		log.Println("operation recovery skipped")
	} else if err := db.RecoverExpiredOperations(context.Background()); err != nil {
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

	// Saga store (archive-aware reads when an evidence archive is configured)
	historyStore, evidenceArchiver, err := configureEvidenceArchive(cfg, db, saga.NewPostgresStore(db.Pool))
	if err != nil {
		log.Fatalf("evidence archive: %v", err)
	}
	if err := applyEvidenceReservePolicy(context.Background(), cfg, db, evidenceArchiver != nil); err != nil {
		log.Fatalf("evidence reserve: %v", err)
	}
	sagaStore := historyStore

	// Secrets manager
	sec := secrets.NewManager(cfg.AppsDir)

	databaseTargets, err := configureDatabaseTargets(context.Background(), cfg, db)
	if err != nil {
		log.Fatalf("database targets: %v", err)
	}
	if databaseTargets == nil {
		log.Println("application databases use v2 routing (NORN_DATABASE_PROFILE unset)")
	} else {
		log.Printf("application database work binds catalog targets for profile %s", databaseTargets.ProfileID)
	}
	buildTestEffects, err := configureBuildTestEffects(cfg, db, supervisor.NewCgroupBackend)
	if err != nil {
		log.Fatalf("build.test execution: %v", err)
	}
	snapshotEffects, err := configureSnapshotEffects(cfg, db, supervisor.NewCgroupBackend)
	if err != nil {
		log.Fatalf("snapshot execution: %v", err)
	}
	if buildTestEffects == nil {
		log.Println("build.test runs in legacy unfenced mode (NORN_BUILD_TEST_EXECUTION=legacy-unfenced)")
	} else {
		log.Println("build.test runs through the supervised external-effect executor")
	}

	// Deploy pipeline
	pipe := &pipeline.Pipeline{
		DB:                             db,
		Nomad:                          nomadClient,
		Consul:                         consulClient,
		WS:                             ws,
		SagaStore:                      sagaStore,
		Secrets:                        sec,
		AppsDir:                        cfg.AppsDir,
		GitToken:                       cfg.GitToken,
		GitSSHKey:                      cfg.GitSSHKey,
		RegistryURL:                    cfg.RegistryURL,
		NetworkMode:                    cfg.NetworkMode,
		IngressURL:                     cfg.IngressURL,
		ExternalIngress:                cfg.ExternalIngress,
		Production:                     cfg.Production(),
		StrictSecrets:                  cfg.StrictSecrets,
		ArtifactSigningPublicKey:       cfg.ArtifactSigningPublicKey,
		ArtifactDenySeverities:         cfg.ArtifactDenySeverities,
		CosignPath:                     cfg.CosignPath,
		TrivyPath:                      cfg.TrivyPath,
		ReleaseAdmissionMode:           cfg.ReleaseAdmissionMode,
		ReleaseAttestationIssuer:       cfg.ReleaseAttestationIssuer,
		ReleaseAttestationRepositories: cfg.ReleaseAttestationRepositories,
		ReleaseAttestationWorkflowRefs: cfg.ReleaseAttestationWorkflowRefs,
		ReleaseRequireSBOM:             cfg.ReleaseRequireSBOM,
		Beacon:                         beaconSvc,
		Storage:                        s3Client,
		Redpanda:                       redpandaClient,
		BuildTestEffects:               buildTestEffects,
		SnapshotEffects:                snapshotEffects,
		DatabaseTargets:                databaseTargets,
	}
	scaleEffects, err := pipeline.NewNomadScaleEffects(db, nomadClient)
	if err != nil {
		log.Fatalf("configure durable app.scale effects: %v", err)
	}
	pipe.ScaleEffects = scaleEffects
	restartEffects, err := pipeline.NewNomadRestartEffects(db, nomadClient)
	if err != nil {
		log.Fatalf("configure durable app.restart effects: %v", err)
	}
	pipe.RestartEffects = restartEffects
	cronPauseEffects, err := pipeline.NewNomadCronPauseEffects(db, nomadClient)
	if err != nil {
		log.Fatalf("configure durable app.cron-pause effects: %v", err)
	}
	pipe.CronPauseEffects = cronPauseEffects
	cronResumeEffects, err := pipeline.NewNomadCronResumeEffects(db, nomadClient, pipe)
	if err != nil {
		log.Fatalf("configure durable app.cron-resume effects: %v", err)
	}
	pipe.CronResumeEffects = cronResumeEffects
	cronScheduleEffects, err := pipeline.NewNomadCronScheduleEffects(db, nomadClient, pipe)
	if err != nil {
		log.Fatalf("configure durable app.cron-schedule effects: %v", err)
	}
	pipe.CronScheduleEffects = cronScheduleEffects
	if nomadClient != nil {
		cronTriggerEffects, triggerErr := pipeline.NewCronTriggerEffects(db, nomadClient)
		if triggerErr != nil {
			log.Printf("WARNING: cron trigger effects unavailable: %v", triggerErr)
		} else {
			pipe.CronTriggerEffects = cronTriggerEffects
		}
	}
	canaryPromotionEffects, err := pipeline.NewNomadCanaryPromotionEffects(db, nomadClient)
	if err != nil {
		log.Fatalf("configure durable app.canary-promote effects: %v", err)
	}
	pipe.CanaryPromotionEffects = canaryPromotionEffects
	cloudflaredEffects, err := pipeline.NewCloudflaredEffects(db)
	if err != nil {
		log.Fatalf("configure durable cloudflared effects: %v", err)
	}
	pipe.CloudflaredEffects = cloudflaredEffects

	// Construct the acceptance boundary and verify any retained private
	// invocation envelopes before a worker can claim operations.
	h := handler.New(db, nomadClient, consulClient, ws, cfg, pipe, beaconSvc, sec, sagaStore, s3Client, redpandaClient)
	if err := h.OperationStoreError(); err != nil {
		log.Fatalf("operation acceptance: %v", err)
	}
	pipe.SetOperationStore(h.OperationStore())
	if err := preflightConfiguredPrivateInvocationKeys(context.Background(), cfg, h.OperationStore()); err != nil {
		log.Fatalf("private invocation startup preflight: %v", err)
	}
	functionV3Admission, functionV3Worker, err := configureFunctionV3(cfg, db, pipe, nomadClient, sec, h.OperationStore())
	if err != nil {
		log.Fatalf("function v3 startup: %v", err)
	}
	if functionV3Worker != nil && os.Getenv("NORN_SKIP_OPERATION_WORKER") == "true" {
		log.Fatal("function v3 requires the claimed operation worker")
	}
	if functionV3Worker != nil && evidenceArchiver == nil {
		log.Fatal("function v3 requires an evidence archiver")
	}

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	if os.Getenv("NORN_SKIP_OPERATION_WORKER") == "true" {
		log.Println("operation worker skipped")
	} else {
		opWorker := worker.NewOperationWorker(db, pipe)
		go opWorker.Run(workerCtx)
		if functionV3Worker != nil {
			go functionV3Worker.Run(workerCtx, 2*time.Second)
			go (&worker.FunctionInvocationCleanupConsumer{Store: db, Remote: nomadClient}).Run(workerCtx, 5*time.Second)
		}
	}
	if evidenceArchiver != nil {
		log.Printf("evidence archive enabled in %s mode", evidenceArchiver.Mode)
		go runEvidenceArchiver(workerCtx, evidenceArchiver, time.Minute)
	}
	if os.Getenv("NORN_SKIP_NOMAD_WATCHER") == "true" {
		log.Println("nomad allocation watcher skipped")
	} else {
		nomadWatcher := watch.NewNomadAllocationWatcher(nomadClient, consulClient, beaconSvc, cfg.AppsDir)
		go nomadWatcher.Run(workerCtx)
	}

	logSpool, logCollector, err := configureLogCollection(cfg, nomadClient)
	if err != nil {
		log.Fatalf("log collection: %v", err)
	}
	if logSpool != nil {
		defer logSpool.Close()
		h.SetLogSpool(logSpool)
	}
	if logCollector != nil {
		log.Printf("log collection enabled in %s", cfg.LogSpoolDir)
		go logCollector.Run(workerCtx, cfg.LogCollectInterval)
	}

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
	r.Use(h.EvidenceReserveAdmissionMiddleware)
	r.Use(h.AccessMiddleware)
	r.Get("/metrics", h.Metrics)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", h.Health)
		r.Get("/metrics", h.Metrics)
		r.Get("/version", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"version": Version})
		})
		r.Get("/schema", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(schemaPayload(Version, databaseID, schemaStatus, startupCfg))
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
			writeControlCapabilities(w, cfg)
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
		r.Post("/v1/auth/github-actions/exchange", h.ExchangeGitHubActionsOIDC)
		r.Post("/v1/auth/step-up/challenges", h.CreateStepUpChallenge)
		r.Post("/v1/auth/step-up/challenges/{id}/verify", h.VerifyStepUpChallenge)
		r.Get("/v1/apps", h.ListApps)
		r.Post("/v1/apps", h.CreateApp)
		r.With(handler.ValidateAppID).Get("/v1/apps/{id}", h.GetApp)
		r.With(handler.ValidateAppID).Put("/v1/apps/{id}/deployment", h.UpdateAppDeployment)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/releases/preflight", h.QueueReleasePreflight)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/releases/deployments", h.QueueReleaseDeployment)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/releases/rollbacks", h.QueueReleaseRollback)
		r.With(handler.ValidateAppID).Get("/v1/apps/{id}/qualifications", h.ListReleaseQualifications)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/qualifications", h.CreateReleaseQualification)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/promotions", h.QueueReleasePromotion)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/exec-sessions", h.CreateExecSession)
		r.With(handler.ValidateAppID).Get("/v1/apps/{id}/snapshots", h.ListAppSnapshotsV1)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/snapshots", h.QueueAppSnapshot)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/snapshots/retention", h.QueueAppSnapshotRetention)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/snapshots/{snapshot}/restore", h.QueueAppSnapshotRestore)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/migrations", h.QueueAppMigration)
		r.With(handler.ValidateAppID).Get("/v1/apps/{id}/databases/health", h.AppDatabaseHealth)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/databases/baseline", h.RecordDatabaseBaseline)
		r.With(handler.ValidateAppID).Get("/v1/apps/{id}/sagas/{sagaId}", h.GetAppSagaHistory)
		r.With(handler.ValidateAppID).Get("/v1/apps/{id}/logs/history", h.GetAppLogHistory)
		r.Get("/v1/evidence/archive", h.GetEvidenceArchiveHealth)
		r.Get("/v1/database/catalog", h.GetDatabaseCatalog)
		r.Post("/v1/database/catalog/activations", h.ActivateDatabaseCatalog)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/rollbacks", h.QueueAppRollback)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/operations/{operationID}/deployment-reconciliation", h.QueueDeploymentReconciliation)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/operations/{operationID}/cron-trigger-reconciliation", h.QueueCronTriggerReconciliation)
		r.Post("/v1/validate/infraspec", h.ValidateInfraSpecDocument)
		r.Post("/v1/fleet/validate", h.ValidateFleetDocument)
		r.Get("/v1/fleet/node-pools", h.FleetInventory)
		r.Get("/v1/fleet/plans", h.ListFleetPlans)
		r.Get("/v1/fleet/github", h.FleetGitHubStatus)
		r.Get("/v1/fleet/plans/{planID}/reconciliations", h.ListFleetReconciliations)
		r.Post("/v1/fleet/plans/{planID}/reconciliations", h.RecordFleetReconciliation)
		r.Get("/v1/fleet/plans/{planID}/attempts", h.ListFleetRunnerAttempts)
		r.Post("/v1/fleet/plans/{planID}/attempts", h.CreateFleetRunnerAttempt)
		r.Get("/v1/fleet/plans/{planID}/attempts/{attemptID}", h.GetFleetRunnerAttempt)
		r.Post("/v1/fleet/plans/{planID}/attempts/{attemptID}/heartbeat", h.HeartbeatFleetRunnerAttempt)
		r.Post("/v1/fleet/plans/{planID}/attempts/{attemptID}/advance", h.AdvanceFleetRunnerAttempt)
		r.Post("/v1/fleet/plans/{planID}/attempts/{attemptID}/cancel", h.CancelFleetRunnerAttempt)
		r.Post("/v1/fleet/plans/{planID}/github/pull-request", h.CreateFleetGitHubPullRequest)
		r.Post("/v1/fleet/plans/{planID}/github/dispatch", h.DispatchFleetGitHubApply)
		r.Post("/v1/fleet/plans/{planID}/github/reconcile", h.ReconcileFleetGitHubReservation)
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
			// Function invocation has one execution boundary: signed acceptance
			// followed by the claimed worker. An incomplete capability is an
			// explicit unavailable endpoint; it must never fall back to the
			// legacy HTTP-to-Nomad submission path.
			r.Post("/invoke", functionInvocationRoute(functionV3Admission))
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
	return validateControlSecurityForBackend(cfg, startup.ControlBackendConfig{Backend: startup.BackendPostgres})
}

// preflightConfiguredPrivateInvocationKeys leaves the existing runtime fully
// dormant unless its capability is explicitly enabled. When enabled, the
// control store is read before workers or HTTP serving begin so a restored
// record cannot become unreadable after the process accepts traffic.
func preflightConfiguredPrivateInvocationKeys(ctx context.Context, cfg *config.Config, operations store.OperationStore) error {
	if cfg == nil || !cfg.PrivateInvocationEnabled {
		return nil
	}
	ring, err := startup.PrivateInvocationKeyRingFromRuntimeConfig(true, cfg.PrivateInvocationCurrentKeyID, cfg.PrivateInvocationKeys)
	if err != nil {
		return err
	}
	invocationStore, ok := operations.(store.PrivateInvocationStore)
	if !ok {
		return fmt.Errorf("private invocation store is unavailable")
	}
	return startup.PreflightPrivateInvocationKeys(ctx, invocationStore, ring)
}

func validateControlSecurityForBackend(cfg *config.Config, backend startup.ControlBackendConfig) error {
	if cfg == nil {
		return fmt.Errorf("configuration is required")
	}
	if cfg.FunctionV3PreviewEnabled && (!cfg.PrivateInvocationEnabled || backend.Backend != startup.BackendPostgres) {
		return fmt.Errorf("NORN_FUNCTION_V3_PREVIEW_ENABLED requires private invocation acceptance on PostgreSQL")
	}
	if cfg.OperationReplayTTL < 0 {
		return fmt.Errorf("NORN_OPERATION_REPLAY_TTL must be zero or a positive Go duration")
	}
	if _, err := startup.PrivateInvocationKeyRingFromRuntimeConfig(cfg.PrivateInvocationEnabled, cfg.PrivateInvocationCurrentKeyID, cfg.PrivateInvocationKeys); err != nil {
		return err
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
	environment := cfg.EnvironmentID()
	if environment != "development" && environment != "staging" && environment != "production" {
		return fmt.Errorf("NORN_ENVIRONMENT must be development, staging, or production")
	}
	if environment == "production" && profile != "production" {
		return fmt.Errorf("NORN_ENVIRONMENT=production requires NORN_PROFILE=production")
	}
	if environment == "staging" {
		if err := handler.ValidateQualificationSigningConfiguration(cfg.QualificationSigningKey, nil, true); err != nil {
			return fmt.Errorf("NORN_ENVIRONMENT=staging requires a valid Ed25519 NORN_QUALIFICATION_SIGNING_KEY: %w", err)
		}
		if qualificationKeyOverlapsAuditKeys(cfg, cfg.QualificationSigningKey) {
			return fmt.Errorf("NORN_QUALIFICATION_SIGNING_KEY must be distinct from NORN_AUDIT_SIGNING_KEY")
		}
	}
	if environment == "production" {
		if len(cfg.TrustedQualificationSigningKeys) == 0 {
			return fmt.Errorf("NORN_ENVIRONMENT=production requires NORN_TRUSTED_QUALIFICATION_SIGNING_KEYS")
		}
		for _, key := range cfg.TrustedQualificationSigningKeys {
			if err := handler.ValidateQualificationSigningConfiguration("", []string{key}, false); err != nil {
				return fmt.Errorf("every NORN_TRUSTED_QUALIFICATION_SIGNING_KEYS entry must be an Ed25519 public key: %w", err)
			}
			if qualificationKeyOverlapsAuditKeys(cfg, key) {
				return fmt.Errorf("trusted qualification signing keys must be distinct from NORN_AUDIT_SIGNING_KEY")
			}
		}
	}
	if environment == "staging" || environment == "production" {
		if strings.TrimSpace(cfg.GitHubActionsOIDCAudience) == "" || len(cfg.GitHubActionsAllowedRepositories) == 0 || len(cfg.GitHubActionsAllowedWorkflowRefs) == 0 || len(cfg.GitHubActionsAllowedRefs) == 0 || len(cfg.GitHubActionsAllowedEvents) == 0 || len(cfg.GitHubActionsAllowedApps) == 0 || len(cfg.GitHubActionsAllowedEnvironments) == 0 || !validGitHubActionsDefaultBranch(cfg.GitHubActionsDefaultBranch) {
			return fmt.Errorf("staging/production requires explicit NORN_GITHUB_ACTIONS_OIDC_* release identity allowlists and a valid NORN_GITHUB_ACTIONS_DEFAULT_BRANCH")
		}
		parsed, err := url.Parse(cfg.GitHubActionsOIDCJWKSURL)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "token.actions.githubusercontent.com" || parsed.Path != "/.well-known/jwks" {
			return fmt.Errorf("NORN_GITHUB_ACTIONS_OIDC_JWKS_URL must be the fixed GitHub issuer JWKS")
		}
	}
	if err := validateAllowedOrigins(cfg.AllowedOrigins, profile == "production"); err != nil {
		return err
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
		if backend.Backend == startup.BackendEtcd {
			if err := backend.ValidateEtcdProductionTransport(); err != nil {
				return err
			}
		}
		if cfg.ReleaseAdmissionMode != "keyless" {
			return fmt.Errorf("NORN_PROFILE=production requires NORN_RELEASE_ADMISSION_MODE=keyless")
		}
		if strings.TrimSpace(cfg.ReleaseAttestationIssuer) == "" || len(cfg.ReleaseAttestationRepositories) == 0 || len(cfg.ReleaseAttestationWorkflowRefs) == 0 || !cfg.ReleaseRequireSBOM {
			return fmt.Errorf("NORN_PROFILE=production requires keyless release attestation issuer/repository/workflow and SBOM policy")
		}
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
		if backend.Backend != startup.BackendEtcd && !secureDatabaseDSN(cfg.DatabaseURL) {
			return fmt.Errorf("NORN_PROFILE=production requires PostgreSQL sslmode=verify-full")
		}
		if strings.TrimSpace(cfg.RegistryURL) == "" {
			return fmt.Errorf("NORN_PROFILE=production requires NORN_REGISTRY_URL")
		}
		if cfg.ReleaseAdmissionMode == "keyed" && strings.TrimSpace(cfg.ArtifactSigningPublicKey) == "" {
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

// validGitHubActionsDefaultBranch intentionally accepts a branch name only.
// Ref names are rejected so a config value cannot accidentally change the
// immutable refs/heads/ namespace used by the release lane matrix.
func validGitHubActionsDefaultBranch(value string) bool {
	branch := strings.TrimSpace(value)
	if branch == "" || len(branch) > 200 || strings.HasPrefix(branch, "refs/") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasPrefix(branch, ".") || strings.HasSuffix(branch, ".") || strings.HasSuffix(branch, ".lock") || strings.Contains(branch, "//") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") {
		return false
	}
	for _, r := range branch {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	return true
}

func qualificationKeyOverlapsAuditKeys(cfg *config.Config, qualificationKey string) bool {
	if cfg == nil || qualificationKey == "" {
		return false
	}
	auditKeys := make([]string, 0, 1+len(cfg.AuditPreviousSigningKeys))
	auditKeys = append(auditKeys, cfg.AuditSigningKey)
	auditKeys = append(auditKeys, cfg.AuditPreviousSigningKeys...)
	for _, auditKey := range auditKeys {
		if auditKey != "" && len(auditKey) == len(qualificationKey) && subtle.ConstantTimeCompare([]byte(auditKey), []byte(qualificationKey)) == 1 {
			return true
		}
	}
	return false
}

func validateAllowedOrigins(raw string, production bool) error {
	for _, value := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(value)
		if origin == "" {
			continue
		}
		if strings.Contains(origin, "*") {
			return fmt.Errorf("NORN_ALLOWED_ORIGINS cannot contain wildcards")
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("NORN_ALLOWED_ORIGINS entry %q must be an HTTP(S) origin without credentials, paths, queries, or fragments", origin)
		}
		if production && parsed.Scheme != "https" {
			host := strings.ToLower(parsed.Hostname())
			ip := net.ParseIP(host)
			if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return fmt.Errorf("NORN_PROFILE=production requires HTTPS allowed origins except for loopback development clients")
			}
		}
	}
	return nil
}

func secureEndpoint(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(parsed.Scheme, "https") && parsed.Host != "" && parsed.User == nil
}

func secureDatabaseDSN(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return (scheme == "postgres" || scheme == "postgresql") && parsed.Host != "" && strings.EqualFold(parsed.Query().Get("sslmode"), "verify-full")
}

func bearerAuth(token string, h *handler.Handler, requireExplicit bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if publicControlPathForMode(r.URL.Path, requireExplicit) || publicEnrollmentRequest(r) {
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
					Subject: subject, Scopes: []string{handler.ScopeAdmin}, Source: handler.AccessPrincipalSourceCloudflareAccess,
				}))
				return
			}
			authorization := r.Header.Get("Authorization")
			if token != "" && strings.HasPrefix(authorization, "Bearer ") && subtle.ConstantTimeCompare([]byte(authorization[7:]), []byte(token)) == 1 {
				next.ServeHTTP(w, handler.WithAccessPrincipal(r, &handler.AccessPrincipal{
					Subject: "control-plane", Scopes: []string{handler.ScopeAdmin}, Legacy: true, Source: handler.AccessPrincipalSourceSharedAPI,
				}))
				return
			}
			if strings.HasPrefix(authorization, "Bearer ") && h != nil {
				if principal, ok := h.VerifyAccessToken(authorization[7:]); ok {
					if !principal.Allows(requiredScope) && !allowsScopedRead(principal, r, requiredScope) && !allowsReleaseOperatorWrite(principal, r, requiredScope) && !allowsFleetOperatorWrite(principal, r, requiredScope) {
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
	return publicControlPathForMode(path, false)
}

func publicControlPathForMode(path string, requireExplicit bool) bool {
	if path == "/api/health" || path == "/api/version" || path == "/api/v1/openapi.yaml" || path == "/api/v1/capabilities" || path == "/api/v1/auth/github-actions/exchange" {
		return true
	}
	if requireExplicit && path == "/metrics" {
		return false
	}
	// Development keeps local discovery and scrape compatibility. In explicit
	// auth mode these endpoints expose host, process, and service inventory, so
	// Prometheus and operators must authenticate like every other control client.
	if !requireExplicit && (path == "/metrics" || path == "/api/metrics" || path == "/api/services/manifest") {
		return true
	}
	return path == "/api/webhooks/github" || path == "/api/webhooks/gitea" ||
		strings.HasPrefix(path, "/api/a/") || strings.HasPrefix(path, "/api/wake-gateway/") ||
		path == "/api/access/cloudflare/logpush" || (!strings.HasPrefix(path, "/api/") && path != "/ws")
}

func controlScopeForRequest(r *http.Request) string {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/releases/preflight") || strings.HasSuffix(path, "/releases/deployments"):
		return handler.ScopeReleaseStage
	case strings.HasSuffix(path, "/qualifications") && r.Method == http.MethodPost:
		return handler.ScopeReleaseQualify
	case strings.HasSuffix(path, "/promotions"):
		return handler.ScopeReleasePromote
	case strings.HasSuffix(path, "/releases/rollbacks"):
		return handler.ScopeReleaseRollback
	case strings.HasPrefix(path, "/api/v1/fleet/") && r.Method != http.MethodGet && r.Method != http.MethodHead:
		return handler.ScopeFleetOperate
	case r.Method == http.MethodGet && (strings.Contains(path, "/api/v1/fleet/plans/") && (strings.HasSuffix(path, "/reconciliations") || strings.Contains(path, "/attempts"))):
		return handler.ScopeFleetOperate
	case r.Method == http.MethodGet && isSingleV1OperationPath(path):
		return handler.ScopeAPIRead
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

// Workload tokens are intentionally not global api:read credentials. The
// narrow GET allowance only reaches handlers that re-check plan/object binding.
func allowsScopedRead(principal *handler.AccessPrincipal, r *http.Request, requiredScope string) bool {
	if principal == nil || r.Method != http.MethodGet {
		return false
	}
	if requiredScope == handler.ScopeFleetOperate {
		return principal.Allows(handler.ScopeAPIRead)
	}
	return requiredScope == handler.ScopeAPIRead && isSingleV1OperationPath(r.URL.Path) && (principal.Allows(handler.ScopeReleaseStage) || principal.Allows(handler.ScopeReleaseQualify) || principal.Allows(handler.ScopeReleasePromote) || principal.Allows(handler.ScopeReleaseRollback))
}
func allowsReleaseOperatorWrite(principal *handler.AccessPrincipal, r *http.Request, requiredScope string) bool {
	return principal != nil && r.Method != http.MethodGet && principal.CI == nil && principal.Allows(handler.ScopeAPIWrite) && (requiredScope == handler.ScopeReleaseStage || requiredScope == handler.ScopeReleaseQualify || requiredScope == handler.ScopeReleasePromote || requiredScope == handler.ScopeReleaseRollback)
}
func allowsFleetOperatorWrite(principal *handler.AccessPrincipal, r *http.Request, requiredScope string) bool {
	return principal != nil && r.Method != http.MethodGet && principal.CI == nil && principal.Allows(handler.ScopeAPIWrite) && requiredScope == handler.ScopeFleetOperate
}

func isSingleV1OperationPath(path string) bool {
	prefix := "/api/v1/operations/"
	id := strings.TrimPrefix(path, prefix)
	return strings.HasPrefix(path, prefix) && id != "" && !strings.Contains(id, "/")
}

func writeControlCapabilities(w http.ResponseWriter, cfg *config.Config) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"protocolVersion": 1,
		"serverVersion":   Version,
		"environment":     map[string]string{"id": cfg.EnvironmentID(), "profile": cfg.ProfileID()},
		"features": []string{
			"durable-operations", "event-cursor-replay", "platform-preflight", "platform-upgrade",
			"platform-rollback", "platform-smoke", "host-assurance", "scoped-access-tokens",
			"openapi-3.1", "standard-problems", "event-stream-info", "event-gap-detection",
			"event-heartbeat", "event-subscriptions", "operation-cancellation", "typed-operation-receipts",
			"versioned-resources", "device-enrollment", "token-rotation", "token-revocation", "device-listing",
			"device-key-step-up", "exec-sessions", "exec-audit", "exec-session-expiry", "exec-protocol-v1", "host-metrics", "app-creation", "durable-app-recovery-v1", "durable-snapshots", "standalone-migrations", "regional-deployments", "consul-traefik-ingress", "production-readiness", "durable-mutation-audit", "production-mutation-admission", "recovery-drill-receipts", "document-validation", "fleet-v1", "fleet-inventory", "durable-fleet-capacity-plans", "fleet-reconciliation-v1", "fleet-runner-attempts-v1", "fleet-github-app-v1", "release-provenance-v1", "release-qualifications-v1", "release-qualifications-v2", "release-promotions-v1", "release-rollback-v1", "github-actions-oidc-exchange-v1", "server-environment-identity-v1",
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
		// #nosec G101 -- this map advertises endpoint paths; it contains no credentials.
		"endpoints": map[string]string{
			"events": "/api/v1/events", "operations": "/api/v1/operations/{id}",
			"platformPreflights": "/api/v1/platform/preflights", "platformUpgrades": "/api/v1/platform/upgrades",
			"platformRollbacks": "/api/v1/platform/rollbacks", "platformSmoke": "/api/v1/platform/smoke", "hostAssurances": "/api/v1/host/assurances",
			"openapi": "/api/v1/openapi.yaml", "eventInfo": "/api/v1/events/info", "apps": "/api/v1/apps", "appCreation": "/api/v1/apps", "appDeployment": "/api/v1/apps/{id}/deployment",
			"appSnapshots": "/api/v1/apps/{id}/snapshots", "appSnapshotRetention": "/api/v1/apps/{id}/snapshots/retention", "appSnapshotRestore": "/api/v1/apps/{id}/snapshots/{snapshot}/restore", "appMigrations": "/api/v1/apps/{id}/migrations", "appRollbacks": "/api/v1/apps/{id}/rollbacks",
			"appDatabaseHealth": "/api/v1/apps/{id}/databases/health", "databaseCatalog": "/api/v1/database/catalog", "databaseCatalogActivations": "/api/v1/database/catalog/activations",
			"releases": "/api/v1/releases", "hostStatus": "/api/v1/host/status", "hostMetrics": "/api/v1/host/metrics", "productionReadiness": "/api/v1/production/readiness", "recoveryDrills": "/api/v1/production/drills", "mutationAudit": "/api/v1/audit/mutations", "enrollments": "/api/v1/enrollments",
			"devices": "/api/v1/devices", "tokenRotate": "/api/v1/auth/rotate", "tokenRevoke": "/api/v1/auth/revoke",
			"stepUpChallenges": "/api/v1/auth/step-up/challenges", "execSessions": "/api/v1/exec-sessions",
			"infraSpecValidation": "/api/v1/validate/infraspec", "fleetValidation": "/api/v1/fleet/validate", "fleetNodePools": "/api/v1/fleet/node-pools", "fleetPlans": "/api/v1/fleet/plans", "fleetReconciliations": "/api/v1/fleet/plans/{planID}/reconciliations", "fleetRunnerAttempts": "/api/v1/fleet/plans/{planID}/attempts", "fleetGitHub": "/api/v1/fleet/github", "fleetGitHubPullRequest": "/api/v1/fleet/plans/{planID}/github/pull-request", "fleetGitHubDispatch": "/api/v1/fleet/plans/{planID}/github/dispatch", "fleetGitHubReconcile": "/api/v1/fleet/plans/{planID}/github/reconcile",
			"releasePreflight": "/api/v1/apps/{id}/releases/preflight", "releaseDeployments": "/api/v1/apps/{id}/releases/deployments", "releaseQualifications": "/api/v1/apps/{id}/qualifications", "releasePromotions": "/api/v1/apps/{id}/promotions", "releaseRollback": "/api/v1/apps/{id}/releases/rollbacks",
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
