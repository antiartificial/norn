package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"norn/v2/api/config"
	"norn/v2/api/effect"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

const (
	buildTestLegacyUnfenced = "legacy-unfenced"
	buildTestSupervised     = "supervised"
	effectRunnerName        = "norn-effect-runner"
)

type effectBackendFactory func(cgroupRoot, runnerBinary, runnerSHA256 string, signingKey []byte) (supervisor.Backend, error)

// configureBuildTestEffects returns nil only for the explicit legacy mode.
// Supervised mode either assembles every fenced component or fails startup;
// it never degrades to direct execution.
func configureBuildTestEffects(cfg *config.Config, db *store.DB, newBackend effectBackendFactory) (*pipeline.BuildTestEffects, error) {
	switch cfg.BuildTestExecution {
	case buildTestLegacyUnfenced:
		return nil, nil
	case buildTestSupervised:
	default:
		return nil, fmt.Errorf("NORN_BUILD_TEST_EXECUTION must be %q or %q", buildTestLegacyUnfenced, buildTestSupervised)
	}
	if !filepath.IsAbs(cfg.EffectSupervisorDir) {
		return nil, fmt.Errorf("supervised build.test requires an absolute NORN_EFFECT_SUPERVISOR_ROOT")
	}
	if len(cfg.EffectSigningKey) < 32 {
		return nil, fmt.Errorf("supervised build.test requires NORN_EFFECT_SIGNING_KEY of at least 32 bytes")
	}
	if cfg.EffectCgroupRoot == "" {
		return nil, fmt.Errorf("supervised build.test requires NORN_EFFECT_CGROUP_ROOT")
	}
	if cfg.BuildTestTimeout <= 0 || cfg.BuildTestTimeout > supervisor.MaxBuildTestTimeout {
		return nil, fmt.Errorf("NORN_BUILD_TEST_TIMEOUT must be positive and at most %s", supervisor.MaxBuildTestTimeout)
	}
	if strings.TrimSpace(cfg.BuildTestPath) == "" || strings.ContainsAny(cfg.BuildTestPath, "\x00\r\n") {
		return nil, fmt.Errorf("NORN_BUILD_TEST_PATH is invalid")
	}
	runner := cfg.EffectRunnerBinary
	if runner == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate %s beside the API binary: %w", effectRunnerName, err)
		}
		runner = filepath.Join(filepath.Dir(executable), effectRunnerName)
	}
	key := []byte(cfg.EffectSigningKey)
	backend, err := newBackend(cfg.EffectCgroupRoot, runner, cfg.EffectRunnerSHA256, key)
	if err != nil {
		return nil, fmt.Errorf("supervised build.test backend: %w", err)
	}
	manager, err := supervisor.NewManager(cfg.EffectSupervisorDir, key, backend)
	if err != nil {
		return nil, err
	}
	verifier, err := supervisor.NewVerifier(manager)
	if err != nil {
		return nil, err
	}
	effectStore, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	environment := []string{"PATH=" + cfg.BuildTestPath}
	if home := os.Getenv("HOME"); home != "" && !strings.ContainsAny(home, "\x00\r\n") {
		environment = append(environment, "HOME="+home)
	}
	return &pipeline.BuildTestEffects{
		Executor:    &effect.Executor{Store: effectStore, Supervisor: manager, Verifier: verifier},
		Store:       effectStore,
		Supervisor:  effectRunnerName,
		Descriptor:  manager.BuildTestDescriptor,
		Environment: environment,
		Timeout:     cfg.BuildTestTimeout,
	}, nil
}

func configureSnapshotEffects(cfg *config.Config, db *store.DB, newBackend effectBackendFactory) (*pipeline.SnapshotEffects, error) {
	if cfg.SnapshotExecution == "" {
		return nil, nil
	}
	if cfg.SnapshotExecution != "supervised" {
		return nil, fmt.Errorf("NORN_SNAPSHOT_EXECUTION must be supervised when set")
	}
	if !filepath.IsAbs(cfg.EffectSupervisorDir) || len(cfg.EffectSigningKey) < 32 || cfg.EffectCgroupRoot == "" || !filepath.IsAbs(cfg.SnapshotPGDumpPath) || len(cfg.SnapshotPGDumpSHA256) != 64 || cfg.SnapshotTimeout <= 0 || cfg.SnapshotTimeout > supervisor.MaxSnapshotTimeout || cfg.SnapshotArtifactBudgetBytes < supervisor.MaxSnapshotArtifactBytes {
		return nil, fmt.Errorf("supervised app.snapshot configuration is incomplete")
	}
	backend, err := newBackend(cfg.EffectCgroupRoot, cfg.EffectRunnerBinary, cfg.EffectRunnerSHA256, []byte(cfg.EffectSigningKey))
	if err != nil {
		return nil, err
	}
	manager, err := supervisor.NewManager(filepath.Join(cfg.EffectSupervisorDir, "snapshots"), []byte(cfg.EffectSigningKey), backend)
	if err != nil {
		return nil, err
	}
	if err := manager.SetSnapshotArtifactBudget(cfg.SnapshotArtifactBudgetBytes); err != nil {
		return nil, err
	}
	effects, err := pipeline.NewSnapshotEffects(db, manager, cfg.SnapshotPGDumpPath, cfg.SnapshotPGDumpSHA256, cfg.SnapshotTimeout)
	if err != nil {
		return nil, err
	}
	if err := effects.ReconcilePublishedArtifacts(context.Background()); err != nil {
		return nil, fmt.Errorf("reconcile supervised snapshot artifacts: %w", err)
	}
	return effects, nil
}
