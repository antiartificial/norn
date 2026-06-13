package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	platformRepo   string
	platformScript string
)

func init() {
	rootCmd.AddCommand(platformCmd)
	platformCmd.PersistentFlags().StringVar(&platformRepo, "repo", os.Getenv("NORN_PLATFORM_REPO"), "Norn repo path for local platform upgrades")
	platformCmd.PersistentFlags().StringVar(&platformScript, "script", os.Getenv("NORN_PLATFORM_SCRIPT"), "platform-upgrade script path")
	platformCmd.AddCommand(platformPreflightCmd)
	platformCmd.AddCommand(platformUpgradeCmd)
	platformCmd.AddCommand(platformReleasesCmd)
	platformCmd.AddCommand(platformRollbackCmd)
	platformCmd.AddCommand(platformProxyPlanCmd)
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
		return runPlatformUpgradeScript("upgrade", ref)
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

var platformProxyPlanCmd = &cobra.Command{
	Use:   "proxy-plan",
	Short: "Print a no-blip local reverse-proxy cutover plan",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPlatformUpgradeScript("proxy-plan", "")
	},
}

func runPlatformUpgradeScript(mode, ref string) error {
	script, err := resolvePlatformScript()
	if err != nil {
		return err
	}
	args := []string{mode}
	if ref != "" {
		args = append(args, ref)
	}
	command := exec.Command(script, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	command.Env = os.Environ()
	if platformRepo != "" {
		command.Env = append(command.Env, "NORN_PLATFORM_REPO="+platformRepo)
	}
	return command.Run()
}

func resolvePlatformScript() (string, error) {
	if platformScript != "" {
		return platformScript, nil
	}
	candidates := []string{}
	if platformRepo != "" {
		candidates = append(candidates, filepath.Join(platformRepo, "v2", "scripts", "platform-upgrade"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "v2", "scripts", "platform-upgrade"),
			filepath.Join(cwd, "scripts", "platform-upgrade"),
		)
	}
	candidates = append(candidates, "/Users/0xadb/projects/norn/v2/scripts/platform-upgrade")
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("platform-upgrade script not found; set --repo or NORN_PLATFORM_SCRIPT")
}
