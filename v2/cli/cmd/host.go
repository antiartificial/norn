package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	hostRepo   string
	hostScript string
)

func init() {
	rootCmd.AddCommand(hostCmd)
	hostCmd.PersistentFlags().StringVar(&hostRepo, "repo", os.Getenv("NORN_HOST_REPO"), "Norn repo path for host runtime management")
	hostCmd.PersistentFlags().StringVar(&hostScript, "script", os.Getenv("NORN_HOST_SCRIPT"), "host-runtime script path")
	hostCmd.AddCommand(hostInstallCmd)
	hostCmd.AddCommand(hostDoctorCmd)
	hostCmd.AddCommand(hostAssureCmd)
	hostCmd.AddCommand(hostRecoverCmd)
	hostCmd.AddCommand(hostRenderCmd)
	hostCmd.AddCommand(hostMigrateStateCmd)
	hostCmd.AddCommand(hostStatusCmd)
}

var hostCmd = &cobra.Command{
	Use:   "host",
	Short: "Install and recover the local Norn host runtime",
}

func hostScriptCommand(use, short, mode string) *cobra.Command {
	return &cobra.Command{
		Use:                use,
		Short:              short,
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHostScript(append([]string{mode}, args...)...)
		},
	}
}

var hostInstallCmd = hostScriptCommand("install [host-runtime flags]", "Install persistent configs and launchd jobs without interrupting live services", "install")
var hostDoctorCmd = hostScriptCommand("doctor [host-runtime flags]", "Check host dependencies, managed files, launchd state, and runtime health", "doctor")
var hostAssureCmd = hostScriptCommand("assure [host-runtime flags]", "Repair required apps and routes, then probe user-facing endpoints", "assure")
var hostRecoverCmd = hostScriptCommand("recover [host-runtime flags]", "Start dependencies in order and recover the Norn API", "recover")
var hostRenderCmd = hostScriptCommand("render [host-runtime flags]", "Render persistent Nomad and Consul configs for the current host address", "render")
var hostMigrateStateCmd = hostScriptCommand("migrate-state [host-runtime flags]", "Copy stopped Nomad and Consul state into persistent managed directories", "migrate-state")
var hostStatusCmd = hostScriptCommand("status [host-runtime flags]", "Show concise launchd and runtime health", "status")

func runHostScript(args ...string) error {
	repo := hostRepo
	if repo == "" {
		repo = hostRepoFromArgs(args)
	}
	script, err := resolveHostScriptForRepo(repo)
	if err != nil {
		return err
	}
	command := exec.Command(script, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	command.Env = os.Environ()
	if executable, executableErr := os.Executable(); executableErr == nil {
		command.Env = append(command.Env, "NORN_HOST_CLI="+executable)
	}
	if repo != "" {
		command.Env = append(command.Env, "NORN_HOST_REPO="+repo)
	}
	return command.Run()
}

func resolveHostScript() (string, error) {
	return resolveHostScriptForRepo(hostRepo)
}

func resolveHostScriptForRepo(repo string) (string, error) {
	if hostScript != "" {
		return hostScript, nil
	}
	candidates := []string{}
	if repo != "" {
		candidates = append(candidates, filepath.Join(repo, "v2", "scripts", "host-runtime"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "v2", "scripts", "host-runtime"),
			filepath.Join(cwd, "scripts", "host-runtime"),
			filepath.Join(cwd, "..", "scripts", "host-runtime"),
		)
	}
	if executable, err := os.Executable(); err == nil {
		executableDir := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(executableDir, "..", "scripts", "host-runtime"),
			filepath.Join(executableDir, "host-runtime"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".config", "norn", "host", "bin", "host-runtime"),
			filepath.Join(home, "projects", "norn", "v2", "scripts", "host-runtime"),
		)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("host-runtime script not found; set --repo or NORN_HOST_SCRIPT")
}

func hostRepoFromArgs(args []string) string {
	for i, arg := range args {
		if arg == "--repo" && i+1 < len(args) {
			return args[i+1]
		}
		const prefix = "--repo="
		if len(arg) > len(prefix) && arg[:len(prefix)] == prefix {
			return arg[len(prefix):]
		}
	}
	return ""
}
