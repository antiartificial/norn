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

	"norn/api/auth"
	"norn/api/config"
	ncron "norn/api/cron"
	"norn/api/function"
	"norn/api/handler"
	"norn/api/storage"
	"norn/api/health"
	"norn/api/hub"
	"norn/api/k8s"
	"norn/api/model"
	"norn/api/runtime"
	"norn/api/store"
)

func main() {
	cfg := config.Load()

	db, err := store.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	if err := store.Migrate(db); err != nil {
		log.Fatalf("migration: %v", err)
	}

	if err := db.RecoverInFlightForges(context.Background()); err != nil {
		log.Printf("WARNING: forge recovery: %v", err)
	}

	kube, err := k8s.NewClient()
	if err != nil {
		log.Printf("WARNING: k8s unavailable (%v) — running in local-only mode", err)
	}

	var s3Client *storage.Client
	if cfg.S3Endpoint != "" {
		var err error
		s3Client, err = storage.NewClient(storage.Config{
			Endpoint:  cfg.S3Endpoint,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
			Region:    cfg.S3Region,
			UseSSL:    cfg.S3UseSSL,
		})
		if err != nil {
			log.Printf("WARNING: S3 storage unavailable (%v)", err)
		} else {
			log.Println("S3 storage connected at " + cfg.S3Endpoint)
		}
	}

	// Parse allowed origins: always include localhost, plus configured extras.
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

	poller := &health.Poller{
		DB:      db,
		WS:      ws,
		AppsDir: cfg.AppsDir,
	}

	pollerCtx, pollerCancel := context.WithCancel(context.Background())
	defer pollerCancel()
	go poller.Run(pollerCtx)

	// Create cron runtime and scheduler
	var runner runtime.Runner
	switch cfg.CronRuntime {
	default:
		runner = runtime.NewDockerRunner()
	}
	scheduler := ncron.New(runner, db, ws)
	scheduler.Start()

	// Discover apps and sync cron schedules
	if specs, err := model.DiscoverApps(cfg.AppsDir); err == nil {
		scheduler.Sync(specs)
	}

	funcExec := function.NewExecutor(runner, db, ws)

	h := handler.New(db, kube, ws, cfg, scheduler, funcExec, s3Client)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "Cf-Access-Jwt-Assertion"},
		AllowCredentials: true,
	}))

	// CF Access JWT validation (when configured)
	if cfg.CFAccessTeamDomain != "" && cfg.CFAccessAUD != "" {
		cfValidator := auth.NewCFAccessValidator(cfg.CFAccessTeamDomain, cfg.CFAccessAUD)
		r.Use(cfValidator.Middleware)
		log.Println("CF Access auth enabled")
	}

	// Optional bearer token auth when NORN_API_TOKEN is set
	if cfg.APIToken != "" {
		r.Use(bearerAuth(cfg.APIToken))
		log.Println("API token auth enabled")
	}

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", h.Health)
		r.Get("/stats", h.GetStats)
		r.Get("/version", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"version": Version})
		})
		r.Post("/webhooks/push", h.WebhookPush)
		r.Get("/apps", h.ListApps)
		r.Get("/validate", h.ValidateAllApps)
		r.Get("/deployments", h.ListAllDeployments)
		r.Route("/apps/{id}", func(r chi.Router) {
			r.Use(handler.ValidateAppID)
			r.Get("/", h.GetApp)
			r.Get("/logs", h.StreamLogs)
			r.Post("/deploy", h.Deploy)
			r.Post("/forge", h.Forge)
			r.Post("/teardown", h.Teardown)
			r.Get("/health-checks", h.GetHealthHistory)
			r.Get("/forge-state", h.GetForgeState)
			r.Post("/restart", h.Restart)
			r.Post("/rollback", h.Rollback)
			r.Get("/artifacts", h.ListArtifacts)
			r.Get("/secrets", h.ListSecrets)
			r.Put("/secrets", h.UpdateSecrets)
			r.Get("/snapshots", h.ListSnapshots)
			r.Post("/snapshots/{ts}/restore", h.RestoreSnapshot)
			r.Post("/scale", h.Scale)
			r.Get("/cron/history", h.CronHistory)
			r.Post("/cron/trigger", h.CronTrigger)
			r.Post("/cron/pause", h.CronPause)
			r.Post("/cron/resume", h.CronResume)
			r.Put("/cron/schedule", h.CronUpdateSchedule)
			r.Post("/invoke", h.FuncInvoke)
			r.Get("/function/history", h.FuncHistory)
			r.Get("/validate", h.ValidateApp)
		})
		r.Route("/cluster", func(r chi.Router) {
			r.Get("/nodes", h.ListClusterNodes)
			r.Post("/nodes", h.AddClusterNode)
			r.Get("/nodes/{nodeId}", h.GetClusterNode)
			r.Delete("/nodes/{nodeId}", h.RemoveClusterNode)
		})
	})

	r.Get("/ws", ws.HandleConnect)

	// Serve UI static files in production
	if cfg.UIDir != "" {
		fileServer(r, cfg.UIDir)
	}

	srv := &http.Server{
		Addr:    cfg.BindAddr + ":" + cfg.Port,
		Handler: r,
	}

	go func() {
		log.Printf("norn %s listening on %s:%s", Version, cfg.BindAddr, cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	scheduler.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func bearerAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip auth for WebSocket upgrade and health check
			if r.URL.Path == "/ws" || r.URL.Path == "/api/health" || r.URL.Path == "/api/version" || r.URL.Path == "/api/webhooks/push" {
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
