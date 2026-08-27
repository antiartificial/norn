package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	platformRepo          string
	platformScript        string
	platformProxy         bool
	platformRebuildVerify bool
)

func init() {
	rootCmd.AddCommand(platformCmd)
	platformCmd.PersistentFlags().StringVar(&platformRepo, "repo", os.Getenv("NORN_PLATFORM_REPO"), "Norn repo path for local platform upgrades")
	platformCmd.PersistentFlags().StringVar(&platformScript, "script", os.Getenv("NORN_PLATFORM_SCRIPT"), "platform-upgrade script path")
	platformCmd.AddCommand(platformPreflightCmd)
	platformCmd.AddCommand(platformUpgradeCmd)
	platformCmd.AddCommand(platformRebuildCmd)
	platformCmd.AddCommand(platformReleasesCmd)
	platformCmd.AddCommand(platformRollbackCmd)
	platformCmd.AddCommand(platformSmokeCmd)
	platformCmd.AddCommand(platformEnvCmd)
	platformCmd.AddCommand(platformProxyPlanCmd)
	platformCmd.AddCommand(platformProxyStatusCmd)
	platformCmd.AddCommand(platformProxyRenderCmd)
	platformCmd.AddCommand(platformProxySwitchCmd)
	platformUpgradeCmd.Flags().BoolVar(&platformProxy, "proxy", false, "Use managed proxy cutover mode instead of LaunchAgent restart")
	platformRebuildCmd.Flags().BoolVar(&platformRebuildVerify, "verify", false, "Require immutable release artifact verification before rebuilding")
}

var platformCmd = &cobra.Command{
	Use:   "platform",
	Short: "Manage the Norn control plane itself",
}

var platformPreflightCmd = &cobra.Command{
	Use:   "preflight [ref]",
	Short: "Build and health-check a candidate Norn platform release",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := "HEAD"
		if len(args) == 1 {
			ref = args[0]
		}
		return runPlatformUpgradeScript("preflight", ref)
	},
}

var platformUpgradeCmd = &cobra.Command{
	Use:   "upgrade [ref]",
	Short: "Promote a candidate Norn platform release with postflight rollback",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := "HEAD"
		if len(args) == 1 {
			ref = args[0]
		}
		if platformProxy {
			return runPlatformUpgradeScriptEnv([]string{"NORN_PLATFORM_UPGRADE_MODE=proxy"}, "upgrade", ref)
		}
		return runPlatformUpgradeScript("upgrade", ref)
	},
}

var platformRebuildCmd = &cobra.Command{
	Use:   "rebuild <full-sha> --verify",
	Short: "Rebuild a verified immutable platform release from its full commit SHA",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		arguments, err := platformRebuildArguments(args[0], platformRebuildVerify)
		if err != nil {
			return err
		}
		return runPlatformUpgradeScriptArgs(arguments...)
	},
}

var platformReleasesCmd = &cobra.Command{
	Use:   "releases",
	Short: "List local Norn platform releases",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlatformUpgradeScript("releases", "")
	},
}

var platformRollbackCmd = &cobra.Command{
	Use:   "rollback <sha-prefix>",
	Short: "Rollback to a previous local Norn platform release",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlatformUpgradeScript("rollback", args[0])
	},
}

var platformSmokeCmd = &cobra.Command{
	Use:   "smoke",
	Short: "Run authenticated platform smoke using the API runtime env",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlatformUpgradeScript("smoke", "")
	},
}

var platformEnvCmd = &cobra.Command{
	Use:                "env -- <command> [args...]",
	Short:              "Run a command with the API runtime env loaded",
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		return runPlatformUpgradeScriptArgs(append([]string{"env-exec"}, args...)...)
	},
}

var platformProxyPlanCmd = &cobra.Command{
	Use:   "proxy-plan",
	Short: "Print a no-blip local reverse-proxy cutover plan",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlatformUpgradeScript("proxy-plan", "")
	},
}

var platformProxyStatusCmd = &cobra.Command{
	Use:   "proxy-status",
	Short: "Show managed local proxy state",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlatformUpgradeScript("proxy-status", "")
	},
}

var platformProxyRenderCmd = &cobra.Command{
	Use:   "proxy-render",
	Short: "Render the managed local proxy config",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlatformUpgradeScript("proxy-render", "")
	},
}

var platformProxySwitchCmd = &cobra.Command{
	Use:   "proxy-switch <port|host:port>",
	Short: "Switch the managed local proxy upstream",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlatformUpgradeScriptArgs("proxy-switch", args[0])
	},
}

func runPlatformUpgradeScript(mode, ref string) error {
	args := []string{mode}
	if ref != "" {
		args = append(args, ref)
	}
	return runPlatformUpgradeScriptArgs(args...)
}

func platformRebuildArguments(sha string, verify bool) ([]string, error) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if !isFullCommitSHA(sha) {
		return nil, fmt.Errorf("rebuild requires a full 40-character commit SHA")
	}
	if !verify {
		return nil, fmt.Errorf("rebuild requires --verify to validate the immutable release artifact")
	}
	return []string{"rebuild", sha, "--verify"}, nil
}

func isFullCommitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func runPlatformUpgradeScriptArgs(args ...string) error {
	return runPlatformUpgradeScriptEnv(nil, args...)
}

func runPlatformUpgradeScriptEnv(extraEnv []string, args ...string) error {
	script, err := resolvePlatformScript()
	if err != nil {
		return err
	}
	command := exec.Command(script, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	command.Env = replaceEnvironmentValue(os.Environ(), "PATH", platformExecutionPath(os.Getenv("PATH")))
	if platformRepo != "" {
		command.Env = append(command.Env, "NORN_PLATFORM_REPO="+platformRepo)
	}
	command.Env = append(command.Env, extraEnv...)
	return command.Run()
}

func platformExecutionPath(current string) string {
	candidates := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if strings.TrimSpace(current) == "" {
		current = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	candidates = append(candidates, filepath.SplitList(current)...)
	seen := map[string]bool{}
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		if candidate == "/opt/homebrew/bin" || candidate == "/usr/local/bin" {
			if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
				continue
			}
		}
		seen[candidate] = true
		result = append(result, candidate)
	}
	return strings.Join(result, string(os.PathListSeparator))
}

func replaceEnvironmentValue(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func resolvePlatformScript() (string, error) {
	if platformScript != "" {
		return platformScript, nil
	}
	cwd, _ := os.Getwd()
	executable, _ := os.Executable()
	home, _ := os.UserHomeDir()
	candidates := platformScriptCandidates(platformRepo, cwd, executable, home)
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("platform-upgrade script not found; set --repo or NORN_PLATFORM_SCRIPT")
}

func platformScriptCandidates(repo, cwd, executable, home string) []string {
	candidates := []string{}
	if repo != "" {
		candidates = append(candidates, filepath.Join(repo, "v2", "scripts", "platform-upgrade"))
	}
	if cwd != "" {
		candidates = append(candidates,
			filepath.Join(cwd, "v2", "scripts", "platform-upgrade"),
			filepath.Join(cwd, "scripts", "platform-upgrade"),
		)
	}
	if executable != "" {
		executableDir := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(executableDir, "platform-upgrade"),
			filepath.Join(executableDir, "..", "scripts", "platform-upgrade"),
		)
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".config", "norn", "host", "bin", "platform-upgrade"),
			filepath.Join(home, "projects", "norn", "v2", "scripts", "platform-upgrade"),
		)
	}
	return candidates
}
