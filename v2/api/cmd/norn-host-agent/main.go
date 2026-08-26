package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"norn/v2/api/config"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

func main() {
	repo := flag.String("repo", os.Getenv("NORN_PLATFORM_REPO"), "Norn repository path")
	platformScript := flag.String("platform-script", os.Getenv("NORN_PLATFORM_SCRIPT"), "platform-upgrade script path")
	hostScript := flag.String("host-script", os.Getenv("NORN_HOST_SCRIPT"), "host-runtime script path")
	flag.Parse()

	cfg := config.Load()
	db, err := store.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		log.Fatalf("migration: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	executor := &worker.CommandMaintenanceExecutor{Repo: *repo, PlatformScript: *platformScript, HostScript: *hostScript}
	worker.NewMaintenanceWorker(db, executor).Run(ctx)
}
