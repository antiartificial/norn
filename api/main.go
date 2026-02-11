package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"norn/api/config"
	"norn/api/handler"
	"norn/api/hub"
	"norn/api/k8s"
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

	ws := hub.New()
	go ws.Run()

	h := handler.New(db, kube, ws, cfg)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173", "http://localhost:3000"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type"},
		AllowCredentials: true,
	}))

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", h.Health)
		r.Post("/webhooks/push", h.WebhookPush)
		r.Get("/apps", h.ListApps)
		r.Get("/apps/{id}", h.GetApp)
		r.Get("/apps/{id}/logs", h.StreamLogs)
		r.Post("/apps/{id}/deploy", h.Deploy)
		r.Post("/apps/{id}/forge", h.Forge)
		r.Post("/apps/{id}/teardown", h.Teardown)
		r.Get("/apps/{id}/forge-state", h.GetForgeState)
		r.Post("/apps/{id}/restart", h.Restart)
		r.Post("/apps/{id}/rollback", h.Rollback)
		r.Get("/apps/{id}/artifacts", h.ListArtifacts)
		r.Get("/apps/{id}/secrets", h.ListSecrets)
		r.Put("/apps/{id}/secrets", h.UpdateSecrets)
		r.Get("/apps/{id}/snapshots", h.ListSnapshots)
		r.Post("/apps/{id}/snapshots/{ts}/restore", h.RestoreSnapshot)
	})

	r.Get("/ws", ws.HandleConnect)

	// Serve UI static files in production
	if cfg.UIDir != "" {
		fileServer(r, cfg.UIDir)
	}

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
	}

	go func() {
		log.Printf("norn listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
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
