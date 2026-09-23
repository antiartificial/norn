package main

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/config"
	"norn/v2/api/effect"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/store"
)

type startupBackend struct{}

func (startupBackend) Start(context.Context, supervisor.BackendExecution, effect.LaunchMaterial) error {
	return errors.New("not used")
}
func (startupBackend) Observe(context.Context, supervisor.BackendExecution) (supervisor.BackendState, error) {
	return supervisor.BackendState{Phase: effect.SupervisorUnknown}, nil
}
func (startupBackend) Revoke(context.Context, supervisor.BackendExecution) (supervisor.BackendState, error) {
	return supervisor.BackendState{Phase: effect.SupervisorUnknown}, nil
}
func (startupBackend) RetrieveResult(context.Context, supervisor.BackendExecution, string) ([]byte, error) {
	return nil, errors.New("not used")
}

func supervisedConfig(t *testing.T) *config.Config {
	return &config.Config{
		BuildTestExecution: buildTestSupervised, BuildTestTimeout: 30 * time.Minute, BuildTestPath: "/usr/bin:/bin",
		EffectSupervisorDir: filepath.Join(t.TempDir(), "supervisor"), EffectSigningKey: strings.Repeat("k", 32),
		EffectCgroupRoot: "/sys/fs/cgroup/norn-effects", EffectRunnerBinary: "/opt/norn/bin/norn-effect-runner",
	}
}

func TestBuildTestExecutionModeIsExplicitWithoutFallback(t *testing.T) {
	fakeDB := &store.DB{}
	succeed := func(string, string, string, []byte) (supervisor.Backend, error) { return startupBackend{}, nil }

	if effects, err := configureBuildTestEffects(&config.Config{BuildTestExecution: buildTestLegacyUnfenced}, fakeDB, succeed); err != nil || effects != nil {
		t.Fatalf("legacy mode = %+v, %v", effects, err)
	}
	if _, err := configureBuildTestEffects(&config.Config{BuildTestExecution: "auto"}, fakeDB, succeed); err == nil {
		t.Fatal("unknown execution mode accepted")
	}
	for name, mutate := range map[string]func(*config.Config){
		"relative root":  func(c *config.Config) { c.EffectSupervisorDir = "supervisor" },
		"short key":      func(c *config.Config) { c.EffectSigningKey = "short" },
		"missing cgroup": func(c *config.Config) { c.EffectCgroupRoot = "" },
		"no timeout":     func(c *config.Config) { c.BuildTestTimeout = 0 },
		"huge timeout":   func(c *config.Config) { c.BuildTestTimeout = 48 * time.Hour },
		"bad path":       func(c *config.Config) { c.BuildTestPath = "/bin\nX=1" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := supervisedConfig(t)
			mutate(cfg)
			if effects, err := configureBuildTestEffects(cfg, fakeDB, succeed); err == nil || effects != nil {
				t.Fatalf("incomplete supervised configuration = %+v, %v", effects, err)
			}
		})
	}
	backendErr := errors.New("runner handshake failed")
	if effects, err := configureBuildTestEffects(supervisedConfig(t), fakeDB, func(string, string, string, []byte) (supervisor.Backend, error) {
		return nil, backendErr
	}); !errors.Is(err, backendErr) || effects != nil {
		t.Fatalf("backend failure = %+v, %v", effects, err)
	}
}

func TestSupervisedBuildTestWiresRunnerBesideBinaryAndPrivateEnvironment(t *testing.T) {
	cfg := supervisedConfig(t)
	cfg.EffectRunnerBinary = ""
	t.Setenv("HOME", "/home/norn")
	t.Setenv("NORN_DATABASE_URL", "postgres://secret@db/norn")
	// pgxpool.New is lazy; nothing in this test connects to a database.
	pool, err := pgxpool.New(context.Background(), "postgres://unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var gotRunner, gotRoot string
	effects, err := configureBuildTestEffects(cfg, &store.DB{Pool: pool}, func(root, runner, _ string, _ []byte) (supervisor.Backend, error) {
		gotRoot, gotRunner = root, runner
		return startupBackend{}, nil
	})
	if err != nil || effects == nil || effects.Executor == nil || effects.Store == nil || effects.Descriptor == nil {
		t.Fatalf("supervised assembly = %+v, %v", effects, err)
	}
	if gotRoot != cfg.EffectCgroupRoot || filepath.Base(gotRunner) != effectRunnerName || !filepath.IsAbs(gotRunner) {
		t.Fatalf("backend root=%q runner=%q", gotRoot, gotRunner)
	}
	if strings.Join(effects.Environment, "\n") != "PATH=/usr/bin:/bin\nHOME=/home/norn" || effects.Timeout != cfg.BuildTestTimeout || effects.Supervisor != effectRunnerName {
		t.Fatalf("command environment/timeout = %v %s", effects.Environment, effects.Timeout)
	}
	if runtime.GOOS != "linux" {
		if _, err := supervisor.NewCgroupBackend(cfg.EffectCgroupRoot, gotRunner, "", []byte(cfg.EffectSigningKey)); err == nil {
			t.Fatal("cgroup backend constructed on a platform without qualified containment")
		}
	}
}
