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
	"path/filepath"
	"strconv"
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
	"norn/v2/api/connector"
	"norn/v2/api/consul"
	"norn/v2/api/contract"
	"norn/v2/api/engine"
	"norn/v2/api/fleet"
	"norn/v2/api/githubattestation"
	"norn/v2/api/handler"
	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/observe"
	"norn/v2/api/pipeline"
	"norn/v2/api/privateattestation"
	"norn/v2/api/redpanda"
	containerruntime "norn/v2/api/runtime"
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
	var privateAttestationVerifier *githubattestation.Verifier
	var nornPrivateSigner privateattestation.Signer
	var nornPrivateVerifier *privateattestation.Verifier
	var err error
	if cfg.ReleaseAttestationTrustMode == "github-private" {
		privateAttestationVerifier, err = githubattestation.New(githubattestation.Config{
			AppID: cfg.ReleaseAttestationGitHubAppID, InstallationID: cfg.ReleaseAttestationGitHubInstallationID,
			PrivateKeyFile: cfg.ReleaseAttestationGitHubPrivateKeyFile, RegistryAuthFile: cfg.ReleaseAttestationRegistryAuthFile,
			APIBaseURL: cfg.ReleaseAttestationGitHubAPIBaseURL, GHPath: cfg.ReleaseAttestationGHPath,
		}, nil)
		if err != nil {
			log.Fatalf("private GitHub attestation verifier: %v", err)
		}
	}
	if cfg.ReleaseAttestationTrustMode == "norn-signed-private" && (cfg.EnvironmentID() == "staging" || cfg.EnvironmentID() == "production") {
		nornPrivateVerifier, err = privateattestation.NewVerifier(cfg.ReleasePrivateTrustedSigningKeys)
		if err != nil {
			log.Fatalf("Norn private attestation verifier: %v", err)
		}
		if cfg.EnvironmentID() == "staging" {
			switch cfg.ReleasePrivateSigningBackend {
			case "local":
				nornPrivateSigner, err = privateattestation.NewLocalSigner(cfg.ReleasePrivateSigningKeyFile)
			case "kms-helper":
				nornPrivateSigner, err = privateattestation.NewHelperSigner(cfg.ReleasePrivateKMSHelper, cfg.ReleasePrivateKMSKeyID)
			default:
				err = fmt.Errorf("unsupported signing backend %q", cfg.ReleasePrivateSigningBackend)
			}
			if err != nil {
				log.Fatalf("Norn private attestation signer: %v", err)
			}
			if !nornPrivateVerifier.Trusts(nornPrivateSigner.KeyID()) {
				log.Fatalf("Norn private attestation signer key is not in NORN_RELEASE_PRIVATE_TRUSTED_SIGNING_KEYS")
			}
		}
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
	cloudflared.SetBinaryPath(cfg.CloudflaredBinary)
	cloudflared.SetLaunchLabel(cfg.CloudflaredLaunchLabel)

	// Database
	db, err := store.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	if err := store.Migrate(db); err != nil {
		log.Fatalf("migration: %v", err)
	}
	if cfg.IsFleetAuthorityOnly() {
		serveFleetAuthorityOnly(cfg, db)
		return
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

	// Workload connector. Selection is explicit: production and existing hosts
	// stay on Nomad/Consul unless an operator opts a development Mac into the
	// Apple Container connector.
	var localEngine *engine.Engine
	var workloads connector.Connector
	var runtimeBackend containerruntime.Backend
	if cfg.WorkloadConnector == connector.AppleContainer {
		runtimeBackend = containerruntime.AppleContainer
		runtimeCtx, runtimeCancel := context.WithTimeout(context.Background(), 60*time.Second)
		err = engine.EnsureRuntime(runtimeCtx)
		runtimeCancel()
		if err != nil {
			log.Fatalf("apple-container runtime: %v", err)
		}
		localEngine, err = engine.New(db, beaconSvc, cfg.AppsDir)
		if err != nil {
			log.Fatalf("apple-container connector: %v", err)
		}
		localEngine.SetEnvironmentProvider(sec.EnvMap)
		workloads = connector.NewApple(localEngine)
	} else {
		runtimeBackend = containerruntime.Docker
		workloads = connector.NewNomadConsul(nomadClient, consulClient)
	}
	containerRuntime := containerruntime.New(runtimeBackend, cfg.RegistryURL)
	if err := workloads.Validate(&model.InfraSpec{}, cfg.Production()); err != nil && cfg.WorkloadConnector == connector.AppleContainer {
		log.Fatalf("workload connector: %v", err)
	}
	log.Printf("workload connector: %s (container runtime: %s)", workloads.Name(), containerRuntime.Backend())

	// Deploy pipeline
	pipe := &pipeline.Pipeline{
		DB:                              db,
		Nomad:                           nomadClient,
		Consul:                          consulClient,
		Workloads:                       workloads,
		ContainerRuntime:                containerRuntime,
		WS:                              ws,
		SagaStore:                       sagaStore,
		Secrets:                         sec,
		AppsDir:                         cfg.AppsDir,
		GitToken:                        cfg.GitToken,
		GitSSHKey:                       cfg.GitSSHKey,
		RegistryURL:                     cfg.RegistryURL,
		NetworkMode:                     cfg.NetworkMode,
		IngressURL:                      cfg.IngressURL,
		ExternalIngress:                 cfg.ExternalIngress,
		Production:                      cfg.Production(),
		StrictSecrets:                   cfg.StrictSecrets,
		ArtifactSigningPublicKey:        cfg.ArtifactSigningPublicKey,
		ArtifactDenySeverities:          cfg.ArtifactDenySeverities,
		CosignPath:                      cfg.CosignPath,
		TrivyPath:                       cfg.TrivyPath,
		ReleaseAdmissionMode:            cfg.ReleaseAdmissionMode,
		ReleaseEnvironment:              cfg.EnvironmentID(),
		ReleaseAttestationIssuer:        cfg.ReleaseAttestationIssuer,
		ReleaseAttestationTrustMode:     cfg.ReleaseAttestationTrustMode,
		ReleaseRegistryAuthFile:         cfg.ReleaseAttestationRegistryAuthFile,
		ReleaseRegistryNodePullReady:    cfg.ReleaseRegistryNodePullReady,
		ReleaseAttestationRepositories:  cfg.ReleaseAttestationRepositories,
		ReleaseAttestationWorkflowRefs:  cfg.ReleaseAttestationWorkflowRefs,
		ExternalFleetBootstrapSignerRef: externalFleetBootstrapSignerForPipeline(cfg),
		ReleaseRequireSBOM:              cfg.ReleaseRequireSBOM,
		TrustedQualificationSigningKeys: cfg.TrustedQualificationSigningKeys,
		Beacon:                          beaconSvc,
		Storage:                         s3Client,
		Redpanda:                        redpandaClient,
	}
	if privateAttestationVerifier != nil {
		pipe.VerifyPrivateKeylessAttestations = privateAttestationVerifier.Verify
	}
	if nornPrivateVerifier != nil {
		pipe.VerifyNornPrivateAttestations = nornPrivateVerifier.Verify
	}

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	if localEngine != nil {
		localEngine.Start(workerCtx)
		defer localEngine.Stop()
	}
	if os.Getenv("NORN_SKIP_OPERATION_WORKER") == "true" {
		log.Println("operation worker skipped")
	} else {
		opWorker := worker.NewOperationWorker(db, pipe)
		go opWorker.Run(workerCtx)
	}
	if cfg.WorkloadConnector == connector.AppleContainer {
		if os.Getenv("NORN_SKIP_ENGINE_WATCHER") == "true" {
			log.Println("apple-container watcher skipped")
		} else {
			engineWatcher := watch.NewEngineWatcher(localEngine, beaconSvc, cfg.AppsDir)
			go engineWatcher.Run(workerCtx)
		}
	} else if os.Getenv("NORN_SKIP_NOMAD_WATCHER") == "true" {
		log.Println("nomad allocation watcher skipped")
	} else {
		nomadWatcher := watch.NewNomadAllocationWatcher(nomadClient, consulClient, beaconSvc, cfg.AppsDir)
		go nomadWatcher.Run(workerCtx)
	}

	// Handler
	h := handler.New(db, nomadClient, consulClient, ws, cfg, pipe, beaconSvc, sec, sagaStore, s3Client, redpandaClient)
	h.ConfigureWorkloads(workloads, localEngine, containerRuntime)
	h.ConfigurePrivateReleaseSigner(nornPrivateSigner)
	if cfg.EnvironmentID() == "staging" && externalFleetVerifierRequested(cfg) {
		verifier, verifierErr := handler.ExternalFleetDeploymentVerifierFromConfig(cfg)
		if verifierErr != nil {
			log.Fatalf("external Fleet deployment verifier: %v", verifierErr)
		}
		h.ConfigureExternalFleetDeploymentVerifier(verifier)
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
	r.Use(h.AccessMiddleware)
	r.Get("/metrics", h.Metrics)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", h.Health)
		r.Get("/metrics", h.Metrics)
		r.Get("/runtime", h.RuntimeInfo)
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
			writeControlCapabilitiesForConfig(cfg, w, r)
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
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/private-attestations", h.CreatePrivateReleaseAttestation)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/releases/deployments", h.QueueReleaseDeployment)
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/external-deployments", h.AdmitExternalFleetDeployment)
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
		r.With(handler.ValidateAppID).Post("/v1/apps/{id}/rollbacks", h.QueueAppRollback)
		r.Get("/v1/deployments", h.ListDeploymentsV1)
		r.Get("/v1/deployments/{id}", h.GetDeploymentV1)
		r.Get("/v1/deployments/{id}/steps", h.ListDeploymentStepsV1)
		r.Get("/v1/services/manifest", h.ServiceManifestV1)
		r.Post("/v1/validate/infraspec", h.ValidateInfraSpecDocument)
		r.Post("/v1/fleet/validate", h.ValidateFleetDocument)
		r.Get("/v1/fleet/node-pools", h.FleetInventory)
		r.Get("/v1/fleet/plans", h.ListFleetPlans)
		r.Get("/v1/fleet/github", h.FleetGitHubStatus)
		r.Get("/v1/fleet/plans/{planID}/reconciliations", h.ListFleetReconciliations)
		r.Post("/v1/fleet/plans/{planID}/reconciliations", h.RecordFleetReconciliation)
		r.Get("/v1/fleet/plans/{planID}/attempts", h.ListFleetRunnerAttempts)
		r.Post("/v1/fleet/plans/{planID}/attempts", h.StartFleetRunnerAttempt)
		r.Get("/v1/fleet/plans/{planID}/attempts/{attemptID}", h.GetFleetRunnerAttempt)
		r.Post("/v1/fleet/plans/{planID}/attempts/{attemptID}/heartbeat", h.HeartbeatFleetRunnerAttempt)
		r.Post("/v1/fleet/plans/{planID}/attempts/{attemptID}/advance", h.AdvanceFleetRunnerAttempt)
		r.Post("/v1/fleet/plans/{planID}/attempts/{attemptID}/cancel", h.CancelFleetRunnerAttempt)
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
		r.Get("/v1/host/runtime", h.RuntimeInfo)
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
	profile, environment, err := validateProfileEnvironment(cfg)
	if err != nil {
		return err
	}
	if cfg.IsFleetAuthorityOnly() {
		return validateFleetAuthorityOnly(cfg, profile, environment)
	}
	fleetGitHubConfigured := strings.TrimSpace(cfg.FleetGitHubAppID) != "" || cfg.FleetGitHubInstallationID != 0 || strings.TrimSpace(cfg.FleetGitHubPrivateKeyFile) != "" || strings.TrimSpace(cfg.FleetGitHubRepository) != "" || strings.TrimSpace(cfg.FleetGitHubConfigPath) != "" || strings.TrimSpace(cfg.FleetGitHubEnvironment) != ""
	if fleetGitHubConfigured {
		if cfg.FleetGitHubEnvironment != "staging" && cfg.FleetGitHubEnvironment != "production" {
			return fmt.Errorf("Fleet GitHub bridge requires NORN_FLEET_GITHUB_ENVIRONMENT=staging or production")
		}
		if cfg.FleetGitHubEnvironment != environment {
			return fmt.Errorf("NORN_FLEET_GITHUB_ENVIRONMENT must match NORN_ENVIRONMENT for a Fleet GitHub control plane")
		}
	}
	if cfg.ReleaseAdmissionMode == "" {
		cfg.ReleaseAdmissionMode = "keyed"
	}
	if cfg.ReleaseAttestationTrustMode == "" {
		cfg.ReleaseAttestationTrustMode = "github-public"
	}
	if cfg.ReleaseAdmissionMode != "keyed" && cfg.ReleaseAdmissionMode != "keyless" && cfg.ReleaseAdmissionMode != "attested" {
		return fmt.Errorf("NORN_RELEASE_ADMISSION_MODE must be keyed, keyless, or attested")
	}
	if cfg.ReleaseAttestationTrustMode != "github-public" && cfg.ReleaseAttestationTrustMode != "github-private" && cfg.ReleaseAttestationTrustMode != "norn-signed-private" {
		return fmt.Errorf("NORN_RELEASE_ATTESTATION_TRUST_MODE must be github-public, github-private, or norn-signed-private")
	}
	if cfg.ReleaseAttestationTrustMode == "github-private" && cfg.ReleaseAdmissionMode != "keyless" {
		return fmt.Errorf("github-private attestation trust requires NORN_RELEASE_ADMISSION_MODE=keyless")
	}
	if cfg.ReleaseAttestationTrustMode == "norn-signed-private" && cfg.ReleaseAdmissionMode != "attested" {
		return fmt.Errorf("norn-signed-private attestation trust requires NORN_RELEASE_ADMISSION_MODE=attested")
	}
	if (cfg.ReleaseAdmissionMode == "keyless" || cfg.ReleaseAdmissionMode == "attested") && (strings.TrimSpace(cfg.ReleaseAttestationIssuer) != "https://token.actions.githubusercontent.com" || len(cfg.ReleaseAttestationRepositories) == 0 || len(cfg.ReleaseAttestationWorkflowRefs) == 0 || !cfg.ReleaseRequireSBOM) {
		return fmt.Errorf("attestation-based release admission requires a GitHub issuer, repository and signer workflow policies, and SBOM verification")
	}
	if cfg.ReleaseAttestationTrustMode == "github-private" {
		if !cfg.ReleaseRegistryNodePullReady {
			return fmt.Errorf("github-private attestation trust requires NORN_RELEASE_REGISTRY_NODE_PULL_READY=true after scheduler/node pull credentials are provisioned")
		}
		if _, err := githubattestation.New(githubattestation.Config{AppID: cfg.ReleaseAttestationGitHubAppID, InstallationID: cfg.ReleaseAttestationGitHubInstallationID, PrivateKeyFile: cfg.ReleaseAttestationGitHubPrivateKeyFile, RegistryAuthFile: cfg.ReleaseAttestationRegistryAuthFile, APIBaseURL: cfg.ReleaseAttestationGitHubAPIBaseURL, GHPath: cfg.ReleaseAttestationGHPath}, nil); err != nil {
			return fmt.Errorf("NORN_RELEASE_ATTESTATION_TRUST_MODE=github-private requires a complete isolated private verifier: %w", err)
		}
	}
	if cfg.ReleaseAttestationTrustMode == "norn-signed-private" && (environment == "staging" || environment == "production") {
		if !cfg.ReleaseRegistryNodePullReady {
			return fmt.Errorf("norn-signed-private attestation trust requires NORN_RELEASE_REGISTRY_NODE_PULL_READY=true after scheduler/node pull credentials are provisioned")
		}
		if err := privateattestation.ValidateOwnerOnlyFile(cfg.ReleaseAttestationRegistryAuthFile); err != nil {
			return fmt.Errorf("norn-signed-private attestation trust requires an owner-only registry auth file: %w", err)
		}
		if _, err := privateattestation.NewVerifier(cfg.ReleasePrivateTrustedSigningKeys); err != nil {
			return fmt.Errorf("norn-signed-private trusted key configuration: %w", err)
		}
		if environment == "production" {
			if cfg.ReleasePrivateSigningBackend != "" || cfg.ReleasePrivateSigningKeyFile != "" || cfg.ReleasePrivateKMSHelper != "" || cfg.ReleasePrivateKMSKeyID != "" {
				return fmt.Errorf("production norn-signed-private verifier must not be configured with signing authority")
			}
		} else {
			var signer privateattestation.Signer
			var signerErr error
			switch cfg.ReleasePrivateSigningBackend {
			case "local":
				signer, signerErr = privateattestation.NewLocalSigner(cfg.ReleasePrivateSigningKeyFile)
			case "kms-helper":
				signer, signerErr = privateattestation.NewHelperSigner(cfg.ReleasePrivateKMSHelper, cfg.ReleasePrivateKMSKeyID)
			default:
				return fmt.Errorf("staging norn-signed-private trust requires NORN_RELEASE_PRIVATE_SIGNING_BACKEND=local or kms-helper")
			}
			if signerErr != nil {
				return fmt.Errorf("norn-signed-private signer: %w", signerErr)
			}
			verifier, _ := privateattestation.NewVerifier(cfg.ReleasePrivateTrustedSigningKeys)
			if !verifier.Trusts(signer.KeyID()) {
				return fmt.Errorf("norn-signed-private signer key must be present in NORN_RELEASE_PRIVATE_TRUSTED_SIGNING_KEYS")
			}
		}
	}
	if (environment == "staging" || environment == "production") && (strings.TrimSpace(cfg.GitHubActionsOIDCAudience) == "" || len(cfg.GitHubActionsReleaseBindings) == 0 || len(cfg.GitHubActionsAllowedWorkflowRefs) == 0 || len(cfg.GitHubActionsAllowedRefs) == 0 || len(cfg.GitHubActionsAllowedEvents) == 0 || len(cfg.GitHubActionsAllowedEnvironments) == 0 || cfg.GitHubActionsDefaultBranch == "") {
		return fmt.Errorf("staging/production requires exact GitHub Actions app-to-repository bindings plus workflow and lane allowlists")
	}
	for _, binding := range cfg.GitHubActionsReleaseBindings {
		if !validGitHubActionsReleaseBinding(binding) {
			return fmt.Errorf("NORN_GITHUB_ACTIONS_RELEASE_BINDINGS entries must be app=owner/repo@repositoryID@ownerID")
		}
	}
	if environment == "staging" || environment == "production" {
		for _, workflowRef := range cfg.GitHubActionsAllowedWorkflowRefs {
			if !immutableGitHubWorkflowRef(workflowRef) {
				return fmt.Errorf("NORN_GITHUB_ACTIONS_ALLOWED_WORKFLOW_REFS entries must be exact workflow paths pinned to a full lowercase commit SHA")
			}
		}
		for _, workflowRef := range cfg.ReleaseAttestationWorkflowRefs {
			if !immutableGitHubWorkflowRef(workflowRef) {
				return fmt.Errorf("NORN_RELEASE_ATTESTATION_WORKFLOW_REFS entries must be exact workflow paths pinned to a full lowercase commit SHA")
			}
		}
		for _, workflowRef := range cfg.GitHubActionsFleetAllowedWorkflowRefs {
			if !immutableGitHubWorkflowRef(workflowRef) {
				return fmt.Errorf("NORN_GITHUB_ACTIONS_FLEET_ALLOWED_WORKFLOW_REFS entries must be exact workflow paths pinned to a full lowercase commit SHA")
			}
		}
	}
	if environment == "staging" {
		if err := handler.ValidateQualificationSigningConfiguration(cfg.QualificationSigningKey, nil, true); err != nil {
			return fmt.Errorf("staging requires a valid qualification signing key: %w", err)
		}
	}
	if environment == "production" {
		if strings.TrimSpace(cfg.QualificationSigningKey) != "" {
			return fmt.Errorf("production must not be configured with the staging qualification signing key")
		}
		if len(cfg.TrustedQualificationSigningKeys) == 0 {
			return fmt.Errorf("production requires a trusted staging qualification signing key")
		}
		if err := handler.ValidateQualificationSigningConfiguration("", cfg.TrustedQualificationSigningKeys, false); err != nil {
			return fmt.Errorf("production qualification signing configuration: %w", err)
		}
	}
	workloadConnector := cfg.WorkloadConnector
	if workloadConnector == "" {
		workloadConnector = connector.NomadConsul
	}
	if workloadConnector != connector.NomadConsul && workloadConnector != connector.AppleContainer {
		return fmt.Errorf("NORN_WORKLOAD_CONNECTOR must be nomad-consul or apple-container")
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
		if !filepath.IsAbs(strings.TrimSpace(cfg.CosignPath)) || !filepath.IsAbs(strings.TrimSpace(cfg.TrivyPath)) {
			return fmt.Errorf("NORN_PROFILE=production requires absolute NORN_COSIGN_PATH and NORN_TRIVY_PATH; PATH lookup is not permitted")
		}
		if workloadConnector != connector.NomadConsul {
			return fmt.Errorf("NORN_PROFILE=production requires NORN_WORKLOAD_CONNECTOR=nomad-consul")
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
		if !secureDatabaseDSN(cfg.DatabaseURL) {
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

func validateProfileEnvironment(cfg *config.Config) (string, string, error) {
	if cfg == nil {
		return "", "", fmt.Errorf("configuration is required")
	}
	profile := strings.ToLower(strings.TrimSpace(cfg.Profile))
	if profile == "" {
		profile = "development"
	}
	if profile != "development" && profile != "production" {
		return "", "", fmt.Errorf("NORN_PROFILE must be development or production")
	}
	environment := cfg.EnvironmentID()
	if environment != "development" && environment != "staging" && environment != "production" {
		return "", "", fmt.Errorf("NORN_ENVIRONMENT must be development, staging, or production")
	}
	if profile == "production" {
		if !cfg.EnvironmentExplicit {
			return "", "", fmt.Errorf("NORN_PROFILE=production requires explicit NORN_ENVIRONMENT=staging or production before this control plane can start; set the release lane during migration")
		}
		if environment != "staging" && environment != "production" {
			return "", "", fmt.Errorf("NORN_PROFILE=production requires NORN_ENVIRONMENT=staging or production; development is not a safe production-profile lane")
		}
	}
	// A production lane must retain the existing production substrate and
	// mutation hardening. Staging may deliberately run either profile.
	if environment == "production" && profile != "production" {
		return "", "", fmt.Errorf("NORN_ENVIRONMENT=production requires NORN_PROFILE=production")
	}
	return profile, environment, nil
}

// validateFleetAuthorityOnly keeps a staging Fleet control plane strongly
// authenticated and durably auditable without requiring an application release
// lane or a Nomad/Consul substrate it intentionally does not operate.
func validateFleetAuthorityOnly(cfg *config.Config, profile, environment string) error {
	if profile != "development" || !cfg.EnvironmentExplicit || environment != "staging" {
		return fmt.Errorf("NORN_FLEET_AUTHORITY_ONLY=true requires NORN_PROFILE=development and explicit NORN_ENVIRONMENT=staging")
	}
	if !cfg.RequireExplicitAuth || len(cfg.APIToken) < 32 {
		return fmt.Errorf("Fleet authority-only mode requires NORN_REQUIRE_EXPLICIT_AUTH=true and a 32-byte NORN_API_TOKEN")
	}
	if !cfg.IsAppCatalogReadOnly() {
		return fmt.Errorf("Fleet authority-only mode requires NORN_APP_CATALOG_READ_ONLY=true")
	}
	if !secureDatabaseDSN(cfg.DatabaseURL) {
		return fmt.Errorf("Fleet authority-only mode requires PostgreSQL sslmode=verify-full")
	}
	if len(cfg.AuditSigningKey) < 32 || cfg.AuditRetentionDays < 90 {
		return fmt.Errorf("Fleet authority-only mode requires a 32-byte NORN_AUDIT_SIGNING_KEY and NORN_AUDIT_RETENTION_DAYS of at least 90")
	}
	if strings.TrimSpace(cfg.AllowedOrigins) == "" || validateAllowedOrigins(cfg.AllowedOrigins, true) != nil {
		return fmt.Errorf("Fleet authority-only mode requires explicit HTTPS NORN_ALLOWED_ORIGINS")
	}
	if strings.TrimSpace(cfg.FleetConfig) == "" || strings.TrimSpace(cfg.FleetGitHubAppID) == "" || cfg.FleetGitHubInstallationID <= 0 || strings.TrimSpace(cfg.FleetGitHubPrivateKeyFile) == "" || strings.TrimSpace(cfg.FleetGitHubRepository) == "" || strings.TrimSpace(cfg.FleetGitHubConfigPath) == "" || cfg.FleetGitHubEnvironment != "staging" || strings.TrimRight(strings.TrimSpace(cfg.FleetGitHubAPIBaseURL), "/") != "https://api.github.com" {
		return fmt.Errorf("Fleet authority-only mode requires a complete staging Fleet GitHub App and NORN_FLEET_CONFIG")
	}
	if strings.TrimSpace(cfg.GitHubActionsOIDCAudience) == "" || strings.TrimSpace(cfg.GitHubActionsOIDCJWKSURL) != "https://token.actions.githubusercontent.com/.well-known/jwks" || strings.TrimSpace(cfg.GitHubActionsFleetAllowedRepository) == "" || len(cfg.GitHubActionsFleetAllowedWorkflowRefs) == 0 || !exactStringSet(cfg.GitHubActionsAllowedRefs, "refs/heads/main") || !exactStringSet(cfg.GitHubActionsAllowedEvents, "push", "workflow_dispatch") || !exactStringSet(cfg.GitHubActionsFleetAllowedEnvironments, "staging") || !exactStringSet(cfg.GitHubActionsFleetAllowedIntents, "apply", "recover") {
		return fmt.Errorf("Fleet authority-only mode requires exact GitHub Actions Fleet OIDC repository/workflow, protected main ref, push/workflow_dispatch events, staging environment, and apply/recover intents")
	}
	fleetDocument, err := os.ReadFile(cfg.FleetConfig)
	if err != nil {
		return fmt.Errorf("Fleet authority-only mode cannot read NORN_FLEET_CONFIG: %w", err)
	}
	fleetConfig, fleetReport := fleet.ParseAndValidate(fleetDocument)
	if !fleetReport.Valid || fleetConfig == nil || strings.TrimSpace(fleetConfig.Metadata.Repository) != cfg.FleetGitHubRepository || fleetConfig.Metadata.Environment != "staging" || !fleetRepositoryTupleMatches(cfg.GitHubActionsFleetAllowedRepository, cfg.FleetGitHubRepository) {
		return fmt.Errorf("Fleet authority-only mode requires NORN_FLEET_CONFIG metadata and the Fleet OIDC repository tuple to match NORN_FLEET_GITHUB_REPOSITORY")
	}
	workflowURL, err := url.Parse(strings.TrimSpace(fleetConfig.Metadata.WorkflowURL))
	if err != nil || workflowURL.Scheme != "https" || workflowURL.Host != "github.com" || workflowURL.Path != "/"+cfg.FleetGitHubRepository+"/actions/workflows/"+cfg.FleetGitHubApplyWorkflow || strings.TrimSpace(cfg.FleetGitHubDefaultBranch) != "main" {
		return fmt.Errorf("Fleet authority-only mode requires NORN_FLEET_CONFIG workflowURL and Fleet GitHub apply workflow to target protected main in NORN_FLEET_GITHUB_REPOSITORY")
	}
	for _, workflowRef := range cfg.GitHubActionsFleetAllowedWorkflowRefs {
		if !immutableGitHubWorkflowRef(workflowRef) || !strings.HasPrefix(workflowRef, cfg.FleetGitHubRepository+"/.github/workflows/") {
			return fmt.Errorf("NORN_GITHUB_ACTIONS_FLEET_ALLOWED_WORKFLOW_REFS entries must be exact pinned workflows in NORN_FLEET_GITHUB_REPOSITORY")
		}
	}
	if (cfg.ReleaseAdmissionMode != "" && cfg.ReleaseAdmissionMode != "keyed") || (cfg.ReleaseAttestationTrustMode != "" && cfg.ReleaseAttestationTrustMode != "github-public") || len(cfg.GitHubActionsReleaseBindings) != 0 || len(cfg.GitHubActionsAllowedWorkflowRefs) != 0 || len(cfg.GitHubActionsAllowedEnvironments) != 0 || strings.TrimSpace(cfg.QualificationSigningKey) != "" || len(cfg.TrustedQualificationSigningKeys) != 0 || strings.TrimSpace(cfg.ReleasePrivateSigningBackend) != "" || strings.TrimSpace(cfg.ReleasePrivateSigningKeyFile) != "" || strings.TrimSpace(cfg.ReleasePrivateKMSHelper) != "" || strings.TrimSpace(cfg.ReleasePrivateKMSKeyID) != "" {
		return fmt.Errorf("Fleet authority-only mode must not configure application release or private signing authority")
	}
	return nil
}

func exactStringSet(values []string, expected ...string) bool {
	if len(values) != len(expected) {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return false
		}
		seen[value] = true
	}
	for _, value := range expected {
		if !seen[value] {
			return false
		}
	}
	return true
}

func fleetRepositoryTupleMatches(value, repository string) bool {
	parts := strings.Split(strings.TrimSpace(value), "@")
	if len(parts) != 3 || parts[0] != repository {
		return false
	}
	for _, id := range parts[1:] {
		parsed, err := strconv.ParseInt(id, 10, 64)
		if err != nil || parsed <= 0 {
			return false
		}
	}
	return true
}

func validGitHubActionsReleaseBinding(value string) bool {
	app, tuple, ok := strings.Cut(strings.TrimSpace(value), "=")
	if !ok || app == "" || tuple == "" || strings.ContainsAny(app, "@/") {
		return false
	}
	parts := strings.Split(tuple, "@")
	if len(parts) != 3 || !strings.Contains(parts[0], "/") || strings.TrimSpace(parts[0]) == "" {
		return false
	}
	for _, id := range parts[1:] {
		parsed, err := strconv.ParseInt(id, 10, 64)
		if err != nil || parsed <= 0 {
			return false
		}
	}
	return true
}

// immutableGitHubWorkflowRef accepts only a concrete workflow path whose ref is
// a full commit SHA. Runtime claim authorization separately binds this suffix
// to GitHub's workflow_sha/job_workflow_sha claim.
func immutableGitHubWorkflowRef(value string) bool {
	value = strings.TrimSpace(value)
	separator := strings.LastIndexByte(value, '@')
	if separator <= 0 || separator == len(value)-1 {
		return false
	}
	workflowPath, sha := value[:separator], value[separator+1:]
	if strings.ContainsAny(workflowPath, "*?[") || !strings.Contains(workflowPath, "/.github/workflows/") || (!strings.HasSuffix(workflowPath, ".yml") && !strings.HasSuffix(workflowPath, ".yaml")) || len(sha) != 40 {
		return false
	}
	for _, character := range sha {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
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
			// Capabilities stay publicly discoverable, but a client that supplies a
			// credential receives its own granted scopes. Invalid supplied
			// credentials fail closed rather than silently degrading to anonymous.
			if r.URL.Path == "/api/v1/capabilities" {
				if claims, ok := auth.CFAccessClaimsFromRequest(r); ok {
					subject := strings.TrimSpace(claims.Email)
					if subject == "" {
						subject = strings.TrimSpace(claims.Subject)
					}
					next.ServeHTTP(w, handler.WithAccessPrincipal(r, &handler.AccessPrincipal{Subject: subject, Scopes: []string{handler.ScopeAdmin}}))
					return
				}
				authorization := r.Header.Get("Authorization")
				if authorization == "" {
					next.ServeHTTP(w, r)
					return
				}
				if token != "" && strings.HasPrefix(authorization, "Bearer ") && subtle.ConstantTimeCompare([]byte(authorization[7:]), []byte(token)) == 1 {
					next.ServeHTTP(w, handler.WithAccessPrincipal(r, &handler.AccessPrincipal{Subject: "control-plane", Scopes: []string{handler.ScopeAdmin}, Legacy: true}))
					return
				}
				if strings.HasPrefix(authorization, "Bearer ") && h != nil {
					if principal, ok := h.VerifyAccessToken(authorization[7:]); ok {
						next.ServeHTTP(w, handler.WithAccessPrincipal(r, principal))
						return
					}
				}
				handler.WriteControlProblem(w, r, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
				return
			}
			if publicControlPathForMode(r.URL.Path, requireExplicit) || publicEnrollmentRequest(r) || publicGitHubActionsExchangeRequest(r) {
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

// The exchange accepts a GitHub-issued bearer assertion, rather than a Norn
// token, and validates it in the dedicated handler. Keep this method/path
// exception exact so other auth routes retain normal control-plane admission.
func publicGitHubActionsExchangeRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/github-actions/exchange"
}

func publicControlPath(path string) bool {
	return publicControlPathForMode(path, false)
}

func publicControlPathForMode(path string, requireExplicit bool) bool {
	if path == "/api/health" || path == "/api/version" || path == "/api/v1/openapi.yaml" || path == "/api/v1/capabilities" {
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
	case path == "/ws" || path == "/api/v1/events":
		return handler.ScopeEventsRead
	case path == "/api/v1/events/info":
		return handler.ScopeEventsRead
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/cancel") && strings.HasPrefix(path, "/api/v1/operations/"):
		return ""
	case (r.Method == http.MethodGet || r.Method == http.MethodHead) && exactOperationPath(path):
		// Workflow tokens may poll only their own app/lane operation. The handler
		// loads the operation and applies that binding; this is not generic read.
		return ""
	case (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.HasPrefix(path, "/api/v1/fleet/plans/") && (strings.Contains(path, "/attempts/") || strings.HasSuffix(path, "/reconciliations")):
		// A fleet:operate OIDC token is allowed only through the handler's exact
		// runner-attempt ownership checks; generic API reads still use api:read.
		return ""
	case path == "/api/v1/auth/rotate" || path == "/api/v1/auth/revoke":
		return ""
	case r.Method == http.MethodPost && (strings.HasSuffix(path, "/releases/preflight") || strings.HasSuffix(path, "/releases/deployments") || strings.HasSuffix(path, "/releases/rollbacks") || strings.HasSuffix(path, "/private-attestations") || strings.HasSuffix(path, "/qualifications") || strings.HasSuffix(path, "/promotions") || strings.HasSuffix(path, "/external-deployments")):
		// The global middleware authenticates the Norn token but the release
		// handlers own their exact scope plus app/environment/CI binding.
		return ""
	case strings.HasPrefix(path, "/api/v1/auth/step-up/") || strings.HasPrefix(path, "/api/v1/exec-sessions") || strings.HasSuffix(path, "/exec-sessions"):
		return handler.ScopeAppsExec
	case path == "/api/v1/enrollments" || path == "/api/v1/enrollments/approve" || strings.HasPrefix(path, "/api/v1/devices"):
		return handler.ScopeAdmin
	case path == "/api/v1/validate/infraspec" || path == "/api/v1/fleet/validate":
		return handler.ScopeAPIRead
	case r.Method != http.MethodGet && r.Method != http.MethodHead && strings.HasPrefix(path, "/api/v1/fleet/") && (strings.Contains(path, "/attempts") || strings.HasSuffix(path, "/reconciliations")):
		// Fleet handlers require the dedicated fleet:operate scope and exact
		// bound GitHub Actions identity. Authentication still happens here; the
		// handler performs the final ownership authorization decision.
		return ""
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

func exactOperationPath(path string) bool {
	id := strings.TrimPrefix(path, "/api/v1/operations/")
	return id != path && id != "" && !strings.Contains(id, "/")
}

func writeControlCapabilities(w http.ResponseWriter, r *http.Request) {
	writeControlCapabilitiesForConfig(nil, w, r)
}

func writeControlCapabilitiesForConfig(cfg *config.Config, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	profile := "development"
	if cfg != nil && strings.TrimSpace(cfg.Profile) != "" {
		profile = strings.ToLower(strings.TrimSpace(cfg.Profile))
	}
	principalInfo := map[string]interface{}{"authenticated": false, "scopes": []string{}}
	if principal, ok := handler.AccessPrincipalFromRequest(r); ok {
		w.Header().Set("Cache-Control", "private, no-store")
		principalInfo = map[string]interface{}{
			"authenticated": true, "subject": principal.Subject,
			"scopes": append([]string(nil), principal.Scopes...), "legacy": principal.Legacy,
		}
		if principal.DeviceID != "" {
			principalInfo["deviceId"] = principal.DeviceID
		}
		if !principal.ExpiresAt.IsZero() {
			principalInfo["expiresAt"] = principal.ExpiresAt.UTC()
		}
	}
	if cfg != nil && cfg.IsFleetAuthorityOnly() {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"protocolVersion": 1, "serverVersion": Version,
			"environment": map[string]string{"id": cfg.EnvironmentID(), "profile": profile},
			"authority":   "fleet-only",
			"features":    []string{"fleet-authority-only-v1", "device-enrollment", "token-rotation", "token-revocation", "device-listing", "principal-scope-discovery-v1", "scoped-access-tokens", "durable-operations", "durable-mutation-audit", "fleet-v1", "fleet-inventory", "durable-fleet-capacity-plans", "fleet-reconciliation-v1", "fleet-runner-attempts-v1", "fleet-github-app-v1", "github-actions-oidc-exchange-v1"},
			"auth": map[string]interface{}{
				"scopes": handler.AccessTokenScopeNames(), "principal": principalInfo,
				"githubActionsExchange": "/api/v1/auth/github-actions/exchange",
				"enrollmentScopes":      []string{handler.ScopeAPIRead, handler.ScopeAPIWrite},
				"websocketBearerHeader": false, "websocketQueryToken": false, "deviceEnrollment": true,
			},
			"endpoints": map[string]string{
				"operationList": "/api/operations", "activeOperations": "/api/operations/active",
				"enrollments": "/api/v1/enrollments", "devices": "/api/v1/devices", "tokenRotate": "/api/v1/auth/rotate", "tokenRevoke": "/api/v1/auth/revoke",
				"operations": "/api/v1/operations/{id}", "mutationAudit": "/api/v1/audit/mutations", "fleetValidation": "/api/v1/fleet/validate", "fleetNodePools": "/api/v1/fleet/node-pools", "fleetPlans": "/api/v1/fleet/plans", "fleetReconciliations": "/api/v1/fleet/plans/{planID}/reconciliations", "fleetRunnerAttempts": "/api/v1/fleet/plans/{planID}/attempts", "fleetGitHub": "/api/v1/fleet/github", "fleetGitHubPullRequest": "/api/v1/fleet/plans/{planID}/github/pull-request", "fleetGitHubDispatch": "/api/v1/fleet/plans/{planID}/github/dispatch",
			},
		})
		return
	}
	releaseConfigured := releasePipelineConfigured(cfg)
	features := []string{
		"durable-operations", "event-cursor-replay", "platform-preflight", "platform-upgrade",
		"platform-rollback", "platform-smoke", "host-assurance", "scoped-access-tokens",
		"openapi-3.1", "standard-problems", "event-stream-info", "event-gap-detection",
		"event-heartbeat", "event-subscriptions", "operation-cancellation", "typed-operation-receipts",
		"versioned-resources", "device-enrollment", "token-rotation", "token-revocation", "device-listing",
		"device-key-step-up", "exec-sessions", "exec-audit", "exec-session-expiry", "exec-protocol-v1", "host-metrics", "host-runtime", "workload-connectors-v1", "apple-container-local", "app-creation", "durable-app-recovery-v1", "durable-snapshots", "standalone-migrations", "regional-deployments", "versioned-deployment-history-v1", "service-instance-placement-v2", "principal-scope-discovery-v1", "consul-traefik-ingress", "production-readiness", "durable-mutation-audit", "production-mutation-admission", "recovery-drill-receipts", "document-validation", "fleet-v1", "fleet-inventory", "durable-fleet-capacity-plans", "fleet-reconciliation-v1", "fleet-runner-attempts-v1", "fleet-github-app-v1",
	}
	if releaseConfigured {
		features = append(features, "release-provenance-v1", "release-qualifications-v2", "release-promotions-v1", "github-actions-oidc-exchange-v1", "release-app-repository-bindings-v1")
		if cfg.ReleaseAttestationTrustMode == "norn-signed-private" {
			features = append(features, "norn-signed-private-v1")
		}
	}
	endpoints := map[string]string{
		"events": "/api/v1/events", "operations": "/api/v1/operations/{id}",
		"platformPreflights": "/api/v1/platform/preflights", "platformUpgrades": "/api/v1/platform/upgrades",
		"platformRollbacks": "/api/v1/platform/rollbacks", "platformSmoke": "/api/v1/platform/smoke", "hostAssurances": "/api/v1/host/assurances",
		"openapi": "/api/v1/openapi.yaml", "eventInfo": "/api/v1/events/info", "apps": "/api/v1/apps", "appCreation": "/api/v1/apps", "appDeployment": "/api/v1/apps/{id}/deployment", "deployments": "/api/v1/deployments", "deployment": "/api/v1/deployments/{id}", "deploymentSteps": "/api/v1/deployments/{id}/steps", "serviceManifest": "/api/v1/services/manifest",
		"appSnapshots": "/api/v1/apps/{id}/snapshots", "appSnapshotRetention": "/api/v1/apps/{id}/snapshots/retention", "appSnapshotRestore": "/api/v1/apps/{id}/snapshots/{snapshot}/restore", "appMigrations": "/api/v1/apps/{id}/migrations", "appRollbacks": "/api/v1/apps/{id}/rollbacks",
		"releases": "/api/v1/releases", "hostStatus": "/api/v1/host/status", "hostMetrics": "/api/v1/host/metrics", "hostRuntime": "/api/v1/host/runtime", "productionReadiness": "/api/v1/production/readiness", "recoveryDrills": "/api/v1/production/drills", "mutationAudit": "/api/v1/audit/mutations", "enrollments": "/api/v1/enrollments",
		"devices": "/api/v1/devices", "tokenRotate": "/api/v1/auth/rotate", "tokenRevoke": "/api/v1/auth/revoke",
		"stepUpChallenges": "/api/v1/auth/step-up/challenges", "execSessions": "/api/v1/exec-sessions",
		"infraSpecValidation": "/api/v1/validate/infraspec", "fleetValidation": "/api/v1/fleet/validate", "fleetNodePools": "/api/v1/fleet/node-pools", "fleetPlans": "/api/v1/fleet/plans", "fleetReconciliations": "/api/v1/fleet/plans/{planID}/reconciliations", "fleetRunnerAttempts": "/api/v1/fleet/plans/{planID}/attempts", "fleetGitHub": "/api/v1/fleet/github", "fleetGitHubPullRequest": "/api/v1/fleet/plans/{planID}/github/pull-request", "fleetGitHubDispatch": "/api/v1/fleet/plans/{planID}/github/dispatch",
	}
	if releaseConfigured {
		endpoints["githubActionsExchange"] = "/api/v1/auth/github-actions/exchange"
		endpoints["releasePreflight"] = "/api/v1/apps/{id}/releases/preflight"
		if cfg.ReleaseAttestationTrustMode == "norn-signed-private" {
			endpoints["privateReleaseAttestations"] = "/api/v1/apps/{id}/private-attestations"
		}
		endpoints["releaseDeployments"] = "/api/v1/apps/{id}/releases/deployments"
		endpoints["releaseQualifications"] = "/api/v1/apps/{id}/qualifications"
		endpoints["releasePromotions"] = "/api/v1/apps/{id}/promotions"
		endpoints["releaseRollbacks"] = "/api/v1/apps/{id}/releases/rollbacks"
	}
	if externalFleetVerifierConfigured(cfg) {
		features = append(features, "external-fleet-deployment-admission-v1")
		endpoints["externalFleetDeployments"] = "/api/v1/apps/{id}/external-deployments"
	}
	if cfg.IsAppCatalogReadOnly() {
		features = withoutCapability(features, "app-creation")
		features = append(features, "app-catalog-read-only-v1")
		delete(endpoints, "appCreation")
		delete(endpoints, "appDeployment")
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"protocolVersion": 1,
		"serverVersion":   Version,
		"environment":     map[string]string{"id": cfg.EnvironmentID(), "profile": profile},
		"features":        features,
		"auth": map[string]interface{}{
			"scopes":                handler.AccessTokenScopeNames(),
			"websocketBearerHeader": true,
			"websocketQueryToken":   false,
			"deviceEnrollment":      true,
			"principal":             principalInfo,
			"stepUp": map[string]interface{}{
				"purposes": []string{"exec"}, "algorithm": "ES256", "publicKeyFormat": "P-256-X9.63",
				"challengeTTLSeconds": 120, "header": "X-Norn-Step-Up",
			},
			"tokens": map[string]interface{}{
				"deviceTTLSeconds": 2592000, "rotation": "atomic", "revocation": "registry",
			},
		},
		// #nosec G101 -- this map advertises endpoint paths; it contains no credentials.
		"endpoints": endpoints,
	})
}

func withoutCapability(features []string, capability string) []string {
	filtered := features[:0]
	for _, feature := range features {
		if feature != capability {
			filtered = append(filtered, feature)
		}
	}
	return filtered
}

// releasePipelineConfigured makes the advertised release surface truthful. It
// is intentionally conservative: a server must have an explicit release lane,
// app-to-repository binding, and the lane's signing/trust material before the
// UI exposes release monitoring or governance affordances.
func releasePipelineConfigured(cfg *config.Config) bool {
	if cfg == nil || (cfg.EnvironmentID() != "staging" && cfg.EnvironmentID() != "production") || len(cfg.GitHubActionsReleaseBindings) == 0 || len(cfg.GitHubActionsAllowedWorkflowRefs) == 0 || len(cfg.GitHubActionsAllowedRefs) == 0 || len(cfg.GitHubActionsAllowedEvents) == 0 || len(cfg.GitHubActionsAllowedEnvironments) == 0 || strings.TrimSpace(cfg.GitHubActionsOIDCAudience) == "" || strings.TrimSpace(cfg.GitHubActionsDefaultBranch) == "" {
		return false
	}
	if cfg.ReleaseAdmissionMode != "keyed" && cfg.ReleaseAdmissionMode != "keyless" && cfg.ReleaseAdmissionMode != "attested" {
		return false
	}
	if cfg.EnvironmentID() == "staging" {
		return strings.TrimSpace(cfg.QualificationSigningKey) != ""
	}
	return len(cfg.TrustedQualificationSigningKeys) > 0
}

// externalFleetBootstrapSignerForPipeline is deliberately stricter than a
// lone workflow-ref environment value. The release verifier can recognize the
// first-image signer only when the entire disabled-by-default external bridge
// is server-pinned; an incomplete bridge cannot widen normal release trust.
func externalFleetBootstrapSignerForPipeline(cfg *config.Config) string {
	if !externalFleetBridgeConfigured(cfg) {
		return ""
	}
	return cfg.ExternalFleetAdmissionBootstrapSignerRef
}

func externalFleetBridgeConfigured(cfg *config.Config) bool {
	return cfg != nil && (cfg.EnvironmentID() == "staging" || cfg.EnvironmentID() == "production") && strings.TrimSpace(cfg.ExternalFleetAdmissionApp) != "" && strings.TrimSpace(cfg.ExternalFleetAdmissionNamespace) != "" && strings.TrimSpace(cfg.ExternalFleetAdmissionMigrationJobID) != "" && strings.TrimSpace(cfg.ExternalFleetAdmissionRuntimeJobID) != "" && cfg.ExternalFleetAdmissionMigrationJobID != cfg.ExternalFleetAdmissionRuntimeJobID && lowerSHA256(cfg.ExternalFleetAdmissionMigrationHCLSHA256) && lowerSHA256(cfg.ExternalFleetAdmissionRuntimeHCLSHA256) && cfg.ExternalFleetAdmissionMigrationHCLSHA256 != cfg.ExternalFleetAdmissionRuntimeHCLSHA256 && externalFleetBootstrapSignerRef(cfg.ExternalFleetAdmissionBootstrapSignerRef) && !externalFleetSignerInNormalAllowlist(cfg.ExternalFleetAdmissionBootstrapSignerRef, cfg.ReleaseAttestationWorkflowRefs)
}

func externalFleetVerifierRequested(cfg *config.Config) bool {
	return cfg != nil && (strings.TrimSpace(cfg.ExternalFleetVerifierURL) != "" || strings.TrimSpace(cfg.ExternalFleetVerifierTokenFile) != "" || strings.TrimSpace(cfg.ExternalFleetGitHubVerifierAppID) != "" || cfg.ExternalFleetGitHubVerifierInstallationID != 0 || strings.TrimSpace(cfg.ExternalFleetGitHubVerifierPrivateKeyFile) != "" || len(cfg.ExternalFleetGitHubVerifierRepositoryIDs) != 0 || strings.TrimSpace(cfg.ExternalFleetGitHubCLIPath) != "" || strings.TrimSpace(cfg.ExternalFleetPublicBaseURL) != "" || len(cfg.ExternalFleetEvidenceAllowedCIDRs) != 0)
}

func externalFleetVerifierConfigured(cfg *config.Config) bool {
	return cfg != nil && cfg.EnvironmentID() == "staging" && externalFleetBridgeConfigured(cfg) && externalFleetVerifierRequested(cfg) && strings.TrimSpace(cfg.ExternalFleetVerifierURL) != "" && strings.TrimSpace(cfg.ExternalFleetVerifierTokenFile) != "" && strings.TrimSpace(cfg.ExternalFleetGitHubVerifierAppID) != "" && cfg.ExternalFleetGitHubVerifierInstallationID > 0 && strings.TrimSpace(cfg.ExternalFleetGitHubVerifierPrivateKeyFile) != "" && len(cfg.ExternalFleetGitHubVerifierRepositoryIDs) > 0 && strings.TrimSpace(cfg.ExternalFleetGitHubCLIPath) != "" && strings.TrimSpace(cfg.ExternalFleetPublicBaseURL) != "" && len(cfg.ExternalFleetEvidenceAllowedCIDRs) > 0
}

func externalFleetSignerInNormalAllowlist(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func externalFleetBootstrapSignerRef(value string) bool {
	path, _, found := strings.Cut(strings.TrimSpace(value), "@")
	return found && strings.HasSuffix(path, ".github/workflows/hello-norn-mysql-bootstrap-image.yml") && immutableGitHubWorkflowRef(value)
}

func lowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// fleetAuthorityOnlyRouter is intentionally separate from the general router.
// Keeping the allowlist here makes an accidental workload, host-maintenance,
// or release route visible in review instead of relying on a denylist.
func fleetAuthorityOnlyRouter(cfg *config.Config, db *store.DB) http.Handler {
	h := handler.New(db, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil)
	allowedOrigins := []string{}
	for _, origin := range strings.Split(cfg.AllowedOrigins, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowedOrigins = append(allowedOrigins, origin)
		}
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: allowedOrigins, AllowedMethods: []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Content-Type", "Authorization", "Idempotency-Key"}, AllowCredentials: true,
	}))
	r.Use(bearerAuth(cfg.APIToken, h, true))
	r.Use(h.MutationAuditMiddleware)

	r.Get("/api/health", h.FleetAuthorityHealth)
	r.Get("/api/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": Version})
	})
	r.Get("/api/operations", h.ListOperations)
	r.Get("/api/operations/active", h.ActiveOperations)
	r.Get("/api/operations/{id}", h.GetOperation)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/capabilities", func(w http.ResponseWriter, r *http.Request) { writeControlCapabilitiesForConfig(cfg, w, r) })
		r.Post("/enrollments", h.StartDeviceEnrollment)
		r.Get("/enrollments", h.ListDeviceEnrollments)
		r.Post("/enrollments/approve", h.ApproveDeviceEnrollment)
		r.Post("/enrollments/{id}/exchange", h.ExchangeDeviceEnrollment)
		r.Get("/devices", h.ListDevices)
		r.Delete("/devices/{id}", h.RevokeDevice)
		r.Post("/auth/github-actions/exchange", h.ExchangeGitHubActionsOIDC)
		r.Post("/auth/rotate", h.RotateCurrentToken)
		r.Post("/auth/revoke", h.RevokeCurrentToken)
		r.Get("/audit/mutations", h.MutationAuditEvents)
		r.Get("/operations/{id}", h.GetOperation)
		r.Post("/operations/{id}/cancel", h.CancelOperation)
		r.Post("/fleet/validate", h.ValidateFleetDocument)
		r.Get("/fleet/node-pools", h.FleetInventory)
		r.Get("/fleet/plans", h.ListFleetPlans)
		r.Get("/fleet/github", h.FleetGitHubStatus)
		r.Get("/fleet/plans/{planID}/reconciliations", h.ListFleetReconciliations)
		r.Post("/fleet/plans/{planID}/reconciliations", h.RecordFleetReconciliation)
		r.Get("/fleet/plans/{planID}/attempts", h.ListFleetRunnerAttempts)
		r.Post("/fleet/plans/{planID}/attempts", h.StartFleetRunnerAttempt)
		r.Get("/fleet/plans/{planID}/attempts/{attemptID}", h.GetFleetRunnerAttempt)
		r.Post("/fleet/plans/{planID}/attempts/{attemptID}/heartbeat", h.HeartbeatFleetRunnerAttempt)
		r.Post("/fleet/plans/{planID}/attempts/{attemptID}/advance", h.AdvanceFleetRunnerAttempt)
		r.Post("/fleet/plans/{planID}/attempts/{attemptID}/cancel", h.CancelFleetRunnerAttempt)
		r.Post("/fleet/plans/{planID}/github/pull-request", h.CreateFleetGitHubPullRequest)
		r.Post("/fleet/plans/{planID}/github/dispatch", h.DispatchFleetGitHubApply)
		r.Post("/fleet/node-pools/{pool}/plan", h.PlanFleetCapacity)
	})
	// The static management shell contains no credentials. API access remains
	// independently authenticated; enabling the UI never enables runtime workers.
	if cfg.UIDir != "" {
		fileServer(r, cfg.UIDir)
	}
	return r
}

func serveFleetAuthorityOnly(cfg *config.Config, db *store.DB) {
	srv := &http.Server{
		Addr: cfg.BindAddr + ":" + cfg.Port, Handler: otelhttp.NewHandler(fleetAuthorityOnlyRouter(cfg, db), "norn.fleet-authority"),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20,
	}
	go func() {
		log.Printf("norn Fleet authority %s listening on %s:%s", Version, cfg.BindAddr, cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
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
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
			http.NotFound(w, r)
			return
		}
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
