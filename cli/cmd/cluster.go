package cmd

import "github.com/spf13/cobra"

var clusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Manage k3s cluster nodes",
	Long:  "Provision, monitor, and manage k3s cluster nodes across cloud providers.",
}

func init() {
	rootCmd.AddCommand(clusterCmd)
}
