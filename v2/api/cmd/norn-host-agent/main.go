package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"norn/v2/api/config"
	"norn/v2/api/startup"
	"norn/v2/api/store"
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
	if err := startup.RequireRuntimeCapabilities(backendCfg); err != nil {
		log.Fatalf("control backend: %v", err)
	}
	if backendCfg.Backend == startup.BackendEtcd {
		log.Fatalf("control backend: norn-host-agent has no etcd execution lane")
	}
	if startupCfg.StartupMode == startup.ModePassive {
		log.Fatalf("startup configuration: %s=passive is supported only by norn-api", startup.StartupModeEnv)
	}
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
	migrator, err := store.NewControlSchemaMigrator(db)
	if err != nil {
		log.Fatalf("schema migration catalog: %v", err)
	}
	status, err := startup.ApplySchemaMode(context.Background(), migrator, startupCfg)
	if err != nil {
		log.Fatalf("schema: %v", err)
	}
	if startupCfg.SchemaMode == startup.SchemaModeMigrateOnly {
		log.Printf("schema migration complete at version %d", status.CurrentMigrationVersion)
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	executor := &worker.CommandMaintenanceExecutor{Repo: *repo, PlatformScript: *platformScript, HostScript: *hostScript}
	worker.NewMaintenanceWorker(db, db, executor).Run(ctx)
}
