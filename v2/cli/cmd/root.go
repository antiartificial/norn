package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
)

var (
	apiURL string
	client *api.Client
)

var rootCmd = &cobra.Command{
	Use:   "norn",
	Short: "Control plane CLI for self-hosted infrastructure",
	Long: `Norn v2 — a personal control plane for self-hosted infrastructure.

Built on Nomad, Consul, and Tailscale for federated clusters
with minimal resource overhead.`,
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
