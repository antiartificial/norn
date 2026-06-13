package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
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

	// Saga store
	sagaStore := saga.NewPostgresStore(db.Pool)

	// Secrets manager
	sec := secrets.NewManager(cfg.AppsDir)

	// Deploy pipeline
	pipe := &pipeline.Pipeline{
		DB:          db,
		Nomad:       nomadClient,
		WS:          ws,
		SagaStore:   sagaStore,
		Secrets:     sec,
		AppsDir:     cfg.AppsDir,
		GitToken:    cfg.GitToken,
		GitSSHKey:   cfg.GitSSHKey,
		RegistryURL: cfg.RegistryURL,
		NetworkMode: cfg.NetworkMode,
		Beacon:      beaconSvc,
		Storage:     s3Client,
		Redpanda:    redpandaClient,
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
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "Cf-Access-Jwt-Assertion"},
		AllowCredentials: true,
	}))

	// CF Access auth
	if cfg.CFAccessTeamDomain != "" && cfg.CFAccessAUD != "" {
		cfValidator := auth.NewCFAccessValidator(cfg.CFAccessTeamDomain, cfg.CFAccessAUD)
		r.Use(cfValidator.Middleware)
		log.Println("CF Access auth enabled")
	}

	// Bearer token auth
	if cfg.APIToken != "" {
		r.Use(bearerAuth(cfg.APIToken))
		log.Println("API token auth enabled")
	}
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
		r.Get("/observability/prometheus.yml", h.PrometheusConfig)
		r.Get("/services/manifest", h.ServiceManifest)
		r.Get("/ops/platform", h.PlatformOps)
		r.Get("/platform/releases", h.PlatformReleases)
		r.Post("/platform/releases/{sha}/rollback", h.PlatformRollbackRelease)
		r.Get("/ops/contextdb", h.ContextDBOps)
		r.Post("/ops/contextdb/feedback/{eventID}/rollback", h.ContextDBRollbackFeedback)
		r.Get("/apps", h.ListApps)
		r.Get("/deployments", h.ListDeployments)
		r.Get("/deployments/{id}/steps", h.ListDeploymentSteps)
		r.Get("/operations", h.ListOperations)
		r.Get("/operations/active", h.ActiveOperations)
		r.Get("/alerts/rules", h.AlertRules)
		r.Get("/events", h.ListEvents)
		r.Post("/events", h.CreateEvent)
		r.Get("/events/{id}", h.GetEvent)
		r.Post("/events/{id}/ack", h.AcknowledgeEvent)
		r.Post("/events/{id}/snooze", h.SnoozeEvent)
		r.Post("/events/{id}/open", h.OpenEvent)
		r.Get("/events/sinks", h.EventSinks)
		r.Post("/events/test", h.TestEvent)
		r.Get("/validate", h.ValidateAll)
		r.Get("/validate/{id}", h.ValidateApp)
		r.Get("/secrets/status", h.SecretsStatusAll)
		r.Get("/saga", h.ListRecentSaga)
		r.Get("/saga/{sagaId}", h.GetSagaEvents)
		r.Get("/cloudflared/ingress", h.CloudflaredIngress)
		r.Get("/access/events", h.AccessEvents)

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
		Addr:    cfg.BindAddr + ":" + cfg.Port,
		Handler: otelhttp.NewHandler(r, "norn.api"),
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

func bearerAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ws" || r.URL.Path == "/metrics" || r.URL.Path == "/api/metrics" || r.URL.Path == "/api/health" || r.URL.Path == "/api/version" || r.URL.Path == "/api/webhooks/github" || r.URL.Path == "/api/webhooks/gitea" || strings.HasSuffix(r.URL.Path, "/exec") {
				next.ServeHTTP(w, r)
				return
			}
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if subtle.ConstantTimeCompare([]byte(auth[7:]), []byte(token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func fileServer(r chi.Router, dir string) {
	fs := http.FileServer(http.Dir(dir))
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(dir + r.URL.Path); os.IsNotExist(err) {
			http.ServeFile(w, r, dir+"/index.html")
			return
		}
		fs.ServeHTTP(w, r)
	})
}
