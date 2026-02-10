package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"norn/cli/api"
)

var (
	apiURL  string
	client  *api.Client
)

var rootCmd = &cobra.Command{
	Use:   "norn",
	Short: "Control plane CLI for self-hosted infrastructure",
	Long: `Norn — a personal control plane for self-hosted infrastructure.

Named after the three Norse fates: Urd (past), Verdandi (present), and Skuld (future).

Discover apps, check health, deploy, restart, rollback, and stream logs — all from the terminal.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		client = api.New(apiURL)
	},
	SilenceUsage: true,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	defaultURL := os.Getenv("NORN_URL")
	if defaultURL == "" {
		defaultURL = "http://localhost:8800"
	}
	rootCmd.PersistentFlags().StringVar(&apiURL, "api", defaultURL, "Norn API URL")
}
